package envd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/sbxexec"
)

const (
	readChunk       = 32 * 1024
	statusLimit     = 8192
	drainTimeout    = 10 * time.Second
	cancelTimeout   = 20 * time.Second
	cancelRetryStep = 20 * time.Millisecond
)

// sender serialises writes to a gRPC stream, which allows only one sender at a time.
type sender struct {
	mu     sync.Mutex
	stream envdpb.Envd_ExecServer
	failed bool
}

func (s *sender) send(message *envdpb.ExecServerMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return errors.New("stream closed")
	}
	if err := s.stream.Send(message); err != nil {
		s.failed = true
		return err
	}
	return nil
}

// running is one launched controller and the ends of its pipes this daemon owns.
type running struct {
	cmd    *exec.Cmd
	stdin  *os.File
	master *os.File // set in TTY mode, where it is also stdin and the only output
	out    []output
	done   chan struct{} // closed once the controller has been reaped
}

type output struct {
	stream envdpb.OutputStream
	file   *os.File
}

// Exec runs one command through a controller process, streaming output and input.
func (s *Server) Exec(stream envdpb.Envd_ExecServer) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "the first message must be start")
	}
	spec, err := ParseStart(first.GetStart(), s.cfg.Roots)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	slot, err := s.execs.acquire(spec.ID)
	if err != nil {
		return err
	}
	defer s.execs.release(spec.ID)
	ctx := stream.Context()
	started := s.cfg.Now()
	run, err := s.launch(spec, slot.done)
	if err != nil {
		close(slot.done)
		s.cfg.Logger.Error("starting exec controller failed", "exec_id", spec.ID, "error", err)
		return status.Error(codes.Internal, "could not start the command controller")
	}
	go func() {
		_ = run.cmd.Wait()
		close(slot.done)
	}()
	out := &sender{stream: stream}
	if err = out.send(&envdpb.ExecServerMessage{Message: &envdpb.ExecServerMessage_Started{Started: &envdpb.ExecStarted{ExecId: spec.ID}}}); err != nil {
		s.stopExec(spec.ID, slot, run, 0)
		return err
	}
	var readers sync.WaitGroup
	for _, o := range run.out {
		readers.Add(1)
		go func() {
			defer readers.Done()
			pump(o, out)
		}()
	}
	go s.receive(stream, spec, run, slot)

	cancelled := false
	select {
	case <-run.done:
	case <-ctx.Done():
		cancelled = true
		s.stopExec(spec.ID, slot, run, DefaultGrace)
		select {
		case <-run.done:
		case <-time.After(cancelTimeout):
			return status.Error(codes.Internal, "cancellation could not be confirmed")
		}
	}
	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainTimeout):
		// Descendants are gone, so a still-open pipe means a stuck reader, not a live writer.
		for _, o := range run.out {
			_ = o.file.Close()
		}
		<-drained
	}
	s.execs.close(spec.ID)
	exit := run.cmd.ProcessState.ExitCode()
	data, statusErr := s.consumeStatus(spec.ID)
	if run.stdin != nil {
		_ = run.stdin.Close()
	}
	for _, o := range run.out {
		_ = o.file.Close()
	}
	if cancelled {
		return status.FromContextError(ctx.Err()).Err()
	}
	if statusErr != nil {
		s.cfg.Logger.Error("exec status unavailable", "exec_id", spec.ID, "error", statusErr)
		return status.Error(codes.DataLoss, "exec completion could not be proven")
	}
	completion, err := ProveCompletion(spec.ID, data, exit)
	if err != nil {
		s.cfg.Logger.Error("exec completion rejected", "exec_id", spec.ID, "error", err)
		return status.Error(codes.DataLoss, "exec completion could not be proven")
	}
	return out.send(&envdpb.ExecServerMessage{Message: &envdpb.ExecServerMessage_Exit{Exit: &envdpb.ExecExit{
		ExitCode:       int32(completion.ExitCode), // #nosec G115 -- exit codes are 0..255 or 128+signal
		Signal:         completion.Signal,
		TimedOut:       completion.TimedOut,
		Cancelled:      completion.Cancelled,
		DurationMillis: s.cfg.Now().Sub(started).Milliseconds(),
	}}})
}

func pump(o output, out *sender) {
	buffer := make([]byte, readChunk)
	for {
		n, err := o.file.Read(buffer)
		if n > 0 {
			// A failed send means the client is gone; keep draining so the command never
			// blocks on a full pipe while cancellation proceeds.
			_ = out.send(&envdpb.ExecServerMessage{Message: &envdpb.ExecServerMessage_Output{Output: &envdpb.ExecOutput{Stream: o.stream, Data: buffer[:n]}}})
		}
		if err != nil {
			return
		}
	}
}

// receive applies client input until the stream ends. Cancel messages and disconnects
// cancel the exec; stdin back-pressure may delay later messages, which is why Signal
// exists as an out-of-band control.
func (s *Server) receive(stream envdpb.Envd_ExecServer, spec ExecSpec, run *running, slot *entry) {
	for {
		message, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) && !spec.TTY {
				_ = run.stdin.Close()
			}
			return
		}
		switch m := message.Message.(type) {
		case *envdpb.ExecClientMessage_Stdin:
			if len(m.Stdin) > MaxChunkBytes {
				go s.stopExec(spec.ID, slot, run, 0)
				return
			}
			if _, err = run.stdin.Write(m.Stdin); err != nil {
				return
			}
		case *envdpb.ExecClientMessage_StdinClose:
			if spec.TTY {
				_, _ = run.stdin.Write([]byte{4})
			} else {
				_ = run.stdin.Close()
			}
		case *envdpb.ExecClientMessage_Resize:
			cols, rows, err := parseSize(m.Resize.GetCols(), m.Resize.GetRows())
			if err == nil && run.master != nil {
				_ = resizePTY(run.master, cols, rows)
			}
		case *envdpb.ExecClientMessage_Cancel:
			grace, err := ParseGrace(m.Cancel.GetGraceMillis(), false)
			if err != nil {
				grace = 0
			}
			go s.stopExec(spec.ID, slot, run, grace)
		default:
		}
	}
}

// launch starts the controller (this binary's `run` command) with its pipes or pty.
func (s *Server) launch(spec ExecSpec, done chan struct{}) (*running, error) {
	args := []string{"run", "--id", spec.ID, "--timeout", strconv.Itoa(spec.TimeoutSeconds), "--cwd", spec.Cwd}
	if spec.TTY {
		args = append(args, "--tty")
	}
	args = append(args, "--")
	args = append(args, spec.Argv...)
	// #nosec G204 -- re-running this binary's controller with validated arguments
	cmd := exec.Command(s.cfg.Self, s.childArgs(args)...)
	cmd.Dir = "/"
	cmd.Env = append(append([]string(nil), s.cfg.BaseEnv...), spec.Env...)
	cmd.SysProcAttr = s.sysProc()
	run := &running{cmd: cmd, done: done}
	if spec.TTY {
		master, slave, err := openPTY(spec.Cols, spec.Rows, s.cfg.User)
		if err != nil {
			return nil, err
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		cmd.SysProcAttr.Setsid, cmd.SysProcAttr.Setctty, cmd.SysProcAttr.Ctty = true, true, 0
		if err = cmd.Start(); err != nil {
			_ = slave.Close()
			_ = master.Close()
			return nil, err
		}
		_ = slave.Close()
		run.master, run.stdin = master, master
		run.out = []output{{envdpb.OutputStream_OUTPUT_STREAM_STDOUT, master}}
		return run, nil
	}
	var closeAfter []*os.File
	pipe := func() (*os.File, *os.File, error) {
		r, w, err := os.Pipe()
		if err == nil {
			closeAfter = append(closeAfter, r, w)
		}
		return r, w, err
	}
	inR, inW, err := pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := pipe()
	if err != nil {
		closeAll(closeAfter)
		return nil, err
	}
	errR, errW, err := pipe()
	if err != nil {
		closeAll(closeAfter)
		return nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err = cmd.Start(); err != nil {
		closeAll(closeAfter)
		return nil, err
	}
	_, _, _ = inR.Close(), outW.Close(), errW.Close()
	run.stdin = inW
	run.out = []output{{envdpb.OutputStream_OUTPUT_STREAM_STDOUT, outR}, {envdpb.OutputStream_OUTPUT_STREAM_STDERR, errR}}
	return run, nil
}

func closeAll(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

// stopExec cancels the process tree of one exec through the controller's own
// cancellation protocol and returns once the controller has ended or the attempt failed.
func (s *Server) stopExec(id string, slot *entry, run *running, graceMillis int) {
	if _, ok := s.execs.beginCancel(id); !ok {
		return
	}
	defer slot.cancels.Done()
	if err := s.kill(id, graceMillis, run.done); err != nil {
		s.cfg.Logger.Error("cancelling exec failed", "exec_id", id, "error", err)
	}
}

// kill runs `kill` as the sandbox user. The controller may not have published its
// identity yet right after launch, so retry briefly until it has, or ends.
func (s *Server) kill(id string, graceMillis int, done <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	grace := (time.Duration(graceMillis) * time.Millisecond).String()
	for {
		_, exit, err := s.capture(ctx, nil, 0, "kill", "--id", id, "--grace", grace)
		if err == nil && exit == 0 {
			return nil
		}
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("cancellation unconfirmed: %w", errors.Join(err, ctx.Err()))
		case <-time.After(cancelRetryStep):
		}
	}
}

// Signal cancels a running exec's whole process tree.
func (s *Server) Signal(_ context.Context, request *envdpb.SignalRequest) (*envdpb.SignalResponse, error) {
	if request.GetSignal() != envdpb.Signal_SIGNAL_TERM && request.GetSignal() != envdpb.Signal_SIGNAL_KILL {
		return nil, status.Error(codes.InvalidArgument, "signal must be TERM or KILL")
	}
	grace := 0
	if request.GetSignal() == envdpb.Signal_SIGNAL_TERM {
		var err error
		if grace, err = ParseGrace(request.GetGraceMillis(), true); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	slot, ok := s.execs.beginCancel(request.GetExecId())
	if !ok {
		return nil, errExecUnknown
	}
	defer slot.cancels.Done()
	if err := s.kill(request.GetExecId(), grace, slot.done); err != nil {
		return nil, status.Error(codes.Internal, "cancellation could not be confirmed")
	}
	return &envdpb.SignalResponse{}, nil
}

// consumeStatus reads and removes the controller's completion record.
func (s *Server) consumeStatus(id string) (protocol.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelTimeout)
	defer cancel()
	out, exit, err := s.capture(ctx, nil, statusLimit, "status", "--id", id, "--consume")
	if err != nil || exit != 0 {
		return protocol.Status{}, errors.Join(err, fmt.Errorf("status exit %d", exit))
	}
	var result protocol.Status
	if err = json.NewDecoder(bytes.NewReader(out)).Decode(&result); err != nil {
		return protocol.Status{}, fmt.Errorf("decoding status: %w", err)
	}
	return result, nil
}

// capture runs a short helper subcommand as the sandbox user, bounding its output.
func (s *Server) capture(ctx context.Context, stdin io.Reader, limit int64, args ...string) ([]byte, int, error) {
	// #nosec G204 -- re-running this binary with fixed subcommands and validated arguments
	cmd := exec.CommandContext(ctx, s.cfg.Self, s.childArgs(args)...)
	cmd.Dir, cmd.Env, cmd.SysProcAttr, cmd.Stdin = "/", []string{}, s.sysProc(), stdin
	var buffer limitedBuffer
	buffer.limit = max(limit, 4096)
	cmd.Stdout = &buffer
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return buffer.Bytes(), 0, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return buffer.Bytes(), exitErr.ExitCode(), nil
	default:
		return nil, -1, err
	}
}

// limitedBuffer fails writes past its limit instead of growing without bound.
type limitedBuffer struct {
	bytes.Buffer
	limit int64
}

var errOutputLimit = errors.New("helper output exceeds limit")

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if int64(b.Len()+len(p)) > b.limit {
		return 0, errOutputLimit
	}
	return b.Buffer.Write(p)
}

// childArgs routes a child to this daemon's state directory when it is not the default.
func (s *Server) childArgs(args []string) []string {
	if s.cfg.StateDir == sbxexec.StateDir {
		return args
	}
	return append([]string{"--state-dir", s.cfg.StateDir}, args...)
}
