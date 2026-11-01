// Command sbx-exec supervises one sandbox command and its adopted descendants.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
)

const metadataLimit = 4096

type clock interface {
	Now() time.Time
	NewTimer(time.Duration) timer
	NewTicker(time.Duration) ticker
}
type timer interface {
	Chan() <-chan time.Time
	Stop()
}
type ticker interface {
	Chan() <-chan time.Time
	Stop()
}
type (
	systemClock  struct{}
	systemTimer  struct{ value *time.Timer }
	systemTicker struct{ value *time.Ticker }
)

func (systemClock) Now() time.Time                   { return time.Now() }
func (systemClock) NewTimer(d time.Duration) timer   { return systemTimer{time.NewTimer(d)} }
func (systemClock) NewTicker(d time.Duration) ticker { return systemTicker{time.NewTicker(d)} }
func (t systemTimer) Chan() <-chan time.Time         { return t.value.C }
func (t systemTimer) Stop()                          { t.value.Stop() }
func (t systemTicker) Chan() <-chan time.Time        { return t.value.C }
func (t systemTicker) Stop()                         { t.value.Stop() }

func main() {
	code, err := execute(os.Args[1:], "/run/sbx")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 125
	}
	os.Exit(code)
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func readMetadata(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, metadataLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > metadataLimit {
		return nil, errors.New("metadata exceeds limit")
	}
	return data, nil
}

func atomicMetadata(root *os.Root, name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding metadata: %w", err)
	}
	if len(data) > metadataLimit {
		return errors.New("metadata exceeds limit")
	}
	temporary := name + ".tmp." + strconv.Itoa(os.Getpid())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("creating metadata: %w", err)
	}
	defer func() { _ = root.Remove(temporary) }()
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	if err = root.Rename(temporary, name); err != nil {
		return fmt.Errorf("committing metadata: %w", err)
	}
	return nil
}

func execute(args []string, dir string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("expected run, kill or status")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "exec ID")
	timeout := flags.Int("timeout", 120, "deadline in seconds")
	cwd := flags.String("cwd", "/workspace", "working directory")
	tty := flags.Bool("tty", false, "set foreground terminal process group")
	grace := flags.Duration("grace", 2*time.Second, "termination grace")
	consume := flags.Bool("consume", false, "remove status after reading")
	if err := flags.Parse(args[1:]); err != nil {
		return 0, errors.New("invalid helper arguments")
	}
	if !validID(*id) {
		return 0, errors.New("invalid exec ID")
	}
	if *timeout < 1 || *timeout > protocol.MaxTimeoutSeconds {
		return 0, errors.New("timeout must be 1..3600 seconds")
	}
	if *grace < 0 || *grace > 10*time.Second {
		return 0, errors.New("grace must be between 0 and 10s")
	}
	if args[0] == "run" && flags.NArg() == 0 {
		return 0, errors.New("run requires argv")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, fmt.Errorf("opening helper state: %w", err)
	}
	defer func() { _ = root.Close() }()
	switch args[0] {
	case "status":
		data, err := readMetadata(root, *id+".json")
		if err != nil {
			return 0, fmt.Errorf("reading exec status: %w", err)
		}
		var status protocol.Status
		if err = json.Unmarshal(data, &status); err != nil || status.ID != *id || !status.CleanupComplete {
			return 0, errors.New("invalid exec status")
		}
		if _, err = os.Stdout.Write(data); err != nil {
			return 0, err
		}
		if *consume {
			if err = root.Remove(*id + ".json"); err != nil {
				return 0, err
			}
		}
		return 0, nil
	case "kill":
		return cancelCommand(root, *id, *grace, systemClock{})
	case "run":
		code, runErr := runCommand(root, *id, flags.Args(), *cwd, *timeout, *tty, systemClock{})
		if runErr != nil {
			return 0, runErr
		}
		return protocol.TransportExit(code), nil
	default:
		return 0, errors.New("unknown helper subcommand")
	}
}

func cancelCommand(root *os.Root, id string, grace time.Duration, c clock) (int, error) {
	data, err := readMetadata(root, id+".pid")
	if errors.Is(err, os.ErrNotExist) {
		status, readErr := readMetadata(root, id+".json")
		var result protocol.Status
		if readErr == nil && json.Unmarshal(status, &result) == nil && result.ID == id && result.CleanupComplete {
			return 0, nil
		}
	}
	if err != nil {
		return 0, fmt.Errorf("reading controller identity: %w", err)
	}
	var controller protocol.Controller
	if err = json.Unmarshal(data, &controller); err != nil || controller.ID != id || controller.PID <= 1 || controller.StartTicks == 0 {
		return 0, errors.New("invalid controller identity")
	}
	procRoot, err := os.OpenRoot("/proc")
	if err != nil {
		return 0, err
	}
	defer func() { _ = procRoot.Close() }()
	info, err := readProcess(procRoot, controller.PID)
	if err != nil || info.StartTicks != controller.StartTicks {
		return 0, errors.New("controller identity changed")
	}
	request := protocol.Cancellation{ID: id, PID: controller.PID, StartTicks: controller.StartTicks, GraceNanos: int64(grace)}
	if err = atomicMetadata(root, id+".cancel", request); err != nil {
		return 0, err
	}
	deadline := c.NewTimer(grace + 6*time.Second)
	defer deadline.Stop()
	poll := c.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		current, readErr := readProcess(procRoot, controller.PID)
		if errors.Is(readErr, os.ErrNotExist) || readErr == nil && current.StartTicks != controller.StartTicks {
			data, statusErr := readMetadata(root, id+".json")
			var status protocol.Status
			if statusErr == nil && json.Unmarshal(data, &status) == nil && status.ID == id && status.CleanupComplete {
				return 0, nil
			}
			return 0, errors.New("controller ended without cleanup proof")
		}
		if readErr != nil {
			return 0, fmt.Errorf("checking controller: %w", readErr)
		}
		select {
		case <-deadline.Chan():
			return 0, errors.New("controller cancellation deadline exceeded")
		case <-poll.Chan():
		}
	}
}

func runCommand(root *os.Root, id string, argv []string, cwd string, timeout int, tty bool, c clock) (code int, runErr error) {
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); errno != 0 {
		return 0, fmt.Errorf("enabling subreaper: %w", errno)
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, 4, 0, 0, 0, 0, 0); errno != 0 {
		return 0, fmt.Errorf("protecting controller: %w", errno)
	}
	procRoot, err := os.OpenRoot("/proc")
	if err != nil {
		return 0, err
	}
	defer func() { _ = procRoot.Close() }()
	if err = checkProcessHandles(os.Getpid()); err != nil {
		return 0, err
	}
	identity, err := readProcess(procRoot, os.Getpid())
	if err != nil {
		return 0, err
	}
	lock, err := root.OpenFile(id+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, errors.New("exec ID already in use")
	}
	if err = lock.Close(); err != nil {
		return 0, err
	}
	defer func() { _ = root.Remove(id + ".lock") }()
	if _, err = root.Stat(id + ".json"); err == nil {
		return 0, errors.New("exec ID already completed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	controller := protocol.Controller{ID: id, PID: identity.PID, StartTicks: identity.StartTicks}
	if err = atomicMetadata(root, id+".pid", controller); err != nil {
		return 0, err
	}
	defer func() { _ = root.Remove(id + ".pid"); _ = root.Remove(id + ".cancel") }()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGALRM, syscall.SIGPIPE, syscall.SIGSEGV, syscall.SIGBUS, syscall.SIGFPE, syscall.SIGILL, syscall.SIGSYS, syscall.SIGTRAP)
	defer signal.Stop(signals)
	program, err := exec.LookPath(argv[0])
	if err != nil {
		return finishWithoutProcess(root, id, 127, "command executable not found")
	}
	// #nosec G702 -- running caller-provided argv inside the sandbox is this helper's purpose; no shell string is constructed
	process, err := os.StartProcess(program, argv, &os.ProcAttr{Dir: cwd, Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setpgid: true, Foreground: tty, Ctty: 0}})
	if err != nil {
		return finishWithoutProcess(root, id, 126, "command could not start")
	}
	defer func() { _ = process.Release() }()
	defer func() {
		if runErr != nil {
			runErr = errors.Join(runErr, emergencyCleanup(procRoot, os.Getpid(), c))
		}
	}()
	deadline := c.NewTimer(time.Duration(timeout) * time.Second)
	defer deadline.Stop()
	poll := c.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	var graceTimer timer
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	var graceEvents <-chan time.Time
	var cleanupDeadline time.Time
	var state State = Running{}
	interpret := func(input Input) (bool, error) {
		var commands []Command
		state, commands = Step(state, input)
		for _, command := range commands {
			switch effect := command.(type) {
			case SignalTree:
				sig := syscall.SIGTERM
				if effect.Signal == killTree {
					sig = syscall.SIGKILL
					if cleanupDeadline.IsZero() {
						cleanupDeadline = c.Now().Add(5 * time.Second)
					}
				}
				if err := signalDescendants(procRoot, os.Getpid(), sig); err != nil {
					return false, err
				}
			case ScheduleGrace:
				if graceTimer != nil {
					graceTimer.Stop()
				}
				graceTimer = c.NewTimer(effect.After)
				graceEvents = graceTimer.Chan()
			case PersistOutcome:
				status := protocol.Status{ID: id, ExitCode: effect.Outcome.Exit.Code, Signal: effect.Outcome.Exit.Signal, TimedOut: effect.Outcome.Reason == timeoutExit, Cancelled: effect.Outcome.Reason == cancelledExit, CleanupComplete: true}
				if err := atomicMetadata(root, id+".json", status); err != nil {
					return false, err
				}
				code = status.ExitCode
				return true, nil
			}
		}
		return false, nil
	}
	for {
		exits, empty, err := reapChildren(process.Pid)
		if err != nil {
			return 0, err
		}
		for _, exit := range exits {
			if _, err = interpret(MainExited{Exit: exit}); err != nil {
				return 0, err
			}
		}
		if empty {
			done, err := interpret(TreeEmpty{})
			if err != nil {
				return 0, err
			}
			if done {
				return code, nil
			}
		} else if _, ok := state.(Draining); ok {
			if _, err = interpret(TreePresent{}); err != nil {
				return 0, err
			}
		}
		if !cleanupDeadline.IsZero() && !c.Now().Before(cleanupDeadline) {
			return 0, errors.New("process tree cleanup deadline exceeded")
		}
		if data, err := readMetadata(root, id+".cancel"); err == nil {
			var request protocol.Cancellation
			if err = json.Unmarshal(data, &request); err != nil || request.ID != id || request.PID != controller.PID || request.StartTicks != controller.StartTicks || request.GraceNanos < 0 || request.GraceNanos > int64(10*time.Second) {
				return 0, errors.New("invalid cancellation request")
			}
			if err = root.Remove(id + ".cancel"); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
			if _, err = interpret(CancellationRequested{Grace: time.Duration(request.GraceNanos)}); err != nil {
				return 0, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
		select {
		case <-deadline.Chan():
			if _, err = interpret(DeadlineExpired{}); err != nil {
				return 0, err
			}
		case <-graceEvents:
			graceEvents = nil
			if _, err = interpret(GraceExpired{}); err != nil {
				return 0, err
			}
		case <-signals:
			if _, err = interpret(CancellationRequested{Grace: 2 * time.Second}); err != nil {
				return 0, err
			}
		case <-poll.Chan():
		}
	}
}

func finishWithoutProcess(root *os.Root, id string, code int, message string) (int, error) {
	_, empty, err := reapChildren(-1)
	if err != nil {
		return 0, err
	}
	if !empty {
		return 0, errors.New("unexpected child before command start")
	}
	status := protocol.Status{ID: id, ExitCode: code, CleanupComplete: true}
	if err = atomicMetadata(root, id+".json", status); err != nil {
		return 0, err
	}
	if _, err = fmt.Fprintln(os.Stderr, message); err != nil {
		return 0, err
	}
	return code, nil
}
