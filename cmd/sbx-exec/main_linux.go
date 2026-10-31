// Command sbx-exec is the Phase 0 prototype for sandbox process groups.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

type result struct {
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Signal   string `json:"signal"`
}

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
	data, err := io.ReadAll(io.LimitReader(file, 257))
	if len(data) > 256 {
		return nil, errors.New("metadata exceeds 256 bytes")
	}
	return data, err
}

func execute(args []string, dir string) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("expected run, kill or status")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	id := fs.String("id", "", "exec ID")
	timeout := fs.Int("timeout", 120, "deadline in seconds")
	cwd := fs.String("cwd", "/tmp", "working directory")
	tty := fs.Bool("tty", false, "set foreground terminal process group")
	grace := fs.Duration("grace", 2*time.Second, "termination grace")
	if err := fs.Parse(args[1:]); err != nil {
		return 0, err
	}
	if !validID(*id) {
		return 0, errors.New("invalid exec ID")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	pidPath := *id + ".pid"
	statusPath := *id + ".json"
	switch args[0] {
	case "status":
		data, err := readMetadata(root, statusPath)
		if err != nil {
			return 0, err
		}
		_, err = os.Stdout.Write(data)
		return 0, err
	case "kill":
		if *grace < 0 || *grace > 10*time.Second {
			return 0, errors.New("grace must be between 0 and 10s")
		}
		data, err := readMetadata(root, pidPath)
		if err != nil {
			return 0, err
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || pid <= 1 {
			return 0, errors.New("invalid process group")
		}
		if err = syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return 0, err
		}
		timer := time.NewTimer(*grace)
		defer timer.Stop()
		<-timer.C
		err = syscall.Kill(-pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			err = nil
		}
		return 0, err
	case "run":
		if *timeout < 1 || *timeout > 3600 || fs.NArg() == 0 {
			return 0, errors.New("run requires argv and timeout between 1 and 3600s")
		}
		//nolint:gosec // The sandbox helper intentionally executes caller argv without building a shell string.
		command := exec.Command(fs.Arg(0), fs.Args()[1:]...)
		command.Dir = *cwd
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Foreground: *tty, Ctty: 0}
		if err := command.Start(); err != nil {
			return 0, err
		}
		pid := command.Process.Pid
		if err := root.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600); err != nil {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = command.Wait()
			return 0, err
		}
		defer func() { _ = root.Remove(pidPath) }()
		waited := make(chan error, 1)
		go func() { waited <- command.Wait() }()
		timer := time.NewTimer(time.Duration(*timeout) * time.Second)
		defer timer.Stop()
		out := result{}
		select {
		case <-waited:
		case <-timer.C:
			out.TimedOut = true
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return 0, err
			}
			<-waited
		}
		out.ExitCode = command.ProcessState.ExitCode()
		if state, ok := command.ProcessState.Sys().(syscall.WaitStatus); ok && state.Signaled() {
			out.Signal = state.Signal().String()
			out.ExitCode = 128 + int(state.Signal())
		}
		data, err := json.Marshal(out)
		if err != nil {
			return 0, err
		}
		if err = root.WriteFile(statusPath, data, 0o600); err != nil {
			return 0, err
		}
		return out.ExitCode, nil
	default:
		return 0, errors.New("unknown subcommand")
	}
}
