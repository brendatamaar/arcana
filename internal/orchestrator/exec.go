package orchestrator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// ExecResult contains bounded output and the real command status.
type ExecResult struct {
	ID                             string
	ExitCode                       int
	Stdout, Stderr                 string
	DurationMS                     int64
	TimedOut, Truncated, Cancelled bool
}
type jobResult struct {
	status *protocol.Status
	err    error
}

func (s *Service) reserveExec(id string) (func(), error) {
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if s.activeExecs[id] >= s.cfg.Limits.Exec.MaxConcurrentPerSandbox {
		return nil, ErrExecLimit
	}
	s.activeExecs[id]++
	return func() {
		s.execMu.Lock()
		defer s.execMu.Unlock()
		s.activeExecs[id]--
		if s.activeExecs[id] == 0 {
			delete(s.activeExecs, id)
		}
	}, nil
}

// Exec runs one supervised command, always recording accepted execs without output content.
func (s *Service) Exec(ctx context.Context, p auth.Principal, id string, request ExecRequest) (ExecResult, error) {
	return s.execute(ctx, p, id, request, nil)
}

func (s *Service) execute(ctx context.Context, p auth.Principal, id string, request ExecRequest, stream *StreamOptions) (ExecResult, error) {
	plan, err := resolveExec(s.cfg, request)
	if err != nil {
		return ExecResult{}, err
	}
	return s.executePlan(ctx, p, id, plan, stream, nil)
}

func (s *Service) executePlan(ctx context.Context, p auth.Principal, id string, plan execPlan, stream *StreamOptions, files *fileIO) (ExecResult, error) {
	row, err := s.execActivity(ctx, p, id, plan.maintenance)
	if err != nil {
		return ExecResult{}, err
	}
	release, err := s.reserveExec(id)
	if err != nil {
		return ExecResult{}, err
	}
	defer release()
	execID, err := ids.New("exe")
	if err != nil {
		return ExecResult{}, err
	}
	started := s.clock.Now()
	shell := int64(0)
	if plan.shell {
		shell = 1
	}
	tty := int64(0)
	if stream != nil && stream.TTY {
		tty = 1
	}
	if _, err = s.store.Queries.CreateExec(ctx, db.CreateExecParams{ID: execID, SandboxID: id, Command: plan.command, IsShell: shell, Cwd: sql.NullString{String: plan.cwd, Valid: true}, Tty: tty, StartedAt: started}); err != nil {
		return ExecResult{}, fmt.Errorf("recording exec start: %w", err)
	}
	// argv is stored as JSON; a shell string is stored raw and must be quoted to be valid metadata,
	// otherwise the whole event is rejected and the command goes unaudited.
	commandJSON := json.RawMessage(plan.command)
	if plan.shell {
		commandJSON, _ = json.Marshal(plan.command)
	}
	s.audit.Record("exec.started", p.ID, id, map[string]any{"exec_id": execID, "command": commandJSON, "is_shell": plan.shell, "cwd": plan.cwd})
	// Live views cover user commands. File transfers and watchdog scans are recorded in the exec
	// table but never streamed: their bytes are file contents or internal bookkeeping.
	var tail *events.Tail
	if files == nil && !plan.maintenance {
		tail = s.tails.Open(execID, row.OwnerKeyID, id)
		s.events.Publish(events.ExecStarted, row.OwnerKeyID, id, map[string]any{"exec_id": execID, "is_shell": plan.shell, "cwd": plan.cwd, "tty": tty == 1, "started_at": started.UTC()})
	}
	return s.superviseExec(ctx, p, row, execID, started, plan, stream, files, tail)
}

func (s *Service) superviseExec(ctx context.Context, p auth.Principal, row db.Sandbox, id string, started time.Time, plan execPlan, stream *StreamOptions, files *fileIO, tail *events.Tail) (ExecResult, error) {
	owned, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	timer := s.clock.NewTimer(time.Duration(plan.timeout+10) * time.Second)
	defer timer.Stop()
	output := &capture{limit: s.cfg.Limits.Exec.MaxOutputBytes, tail: tail}
	if stream != nil {
		output.observer = stream.Observer
	}
	if files != nil {
		output.limit = files.limit
		output.sink = files.output
	}
	completed := make(chan jobResult, 1)
	go func() { completed <- s.runHelper(owned, row.ContainerID.String, id, plan, output, stream, files) }()
	var done <-chan jobResult = completed
	var cancellation <-chan error
	var cancelTimer Timer
	var cancelDeadline <-chan time.Time
	defer func() {
		if cancelTimer != nil {
			cancelTimer.Stop()
		}
	}()
	clientDone := ctx.Done()
	var state execState = execRunning{}
	var outcome jobResult
	var cancelErr error
	for {
		var input execInput
		select {
		case outcome = <-done:
			done = nil
			input = commandCompleted{}
			if outcome.err != nil {
				input = helperFailed{}
			}
		case err := <-cancellation:
			cancellation = nil
			cancelErr = err
			input = cancellationCompleted{}
			// Completion proof from the run may race status consumption with kill.
			// A failed cancel still has only ten seconds to obtain that proof.
		case <-clientDone:
			clientDone = nil
			input = clientCancelled{}
		case <-timer.Chan():
			input = hardDeadline{}
		case <-cancelDeadline:
			input = hardDeadline{}
		}
		// A completion racing an already-fired deadline cannot bypass the host guard.
		select {
		case <-timer.Chan():
			input = hardDeadline{}
		default:
		}
		select {
		case <-cancelDeadline:
			input = hardDeadline{}
		default:
		}
		var commands []execCommand
		state, commands = execStep(state, input)
		for _, command := range commands {
			switch effect := command.(type) {
			case cancelHelper:
				result := make(chan error, 1)
				cancellation = result
				go func() {
					_, _, err := s.control(owned, row.ContainerID.String, []string{helperPath, "kill", "--id", id, "--grace", "2s"})
					result <- err
				}()
			case scheduleCancelDeadline:
				cancelTimer = s.clock.NewTimer(effect.after)
				cancelDeadline = cancelTimer.Chan()
			case destroyExecSandbox:
				cancel()
				cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				destroyErr := s.Destroy(cleanup, p, row.ID, effect.reason)
				stop()
				if done != nil {
					outcome = <-done
					done = nil
				}
				if cancellation != nil {
					outcome.err = errors.Join(outcome.err, <-cancellation)
					cancellation = nil
				}
				outcome.status = nil
				outcome.err = errors.Join(fmt.Errorf("%w: %s", ErrRuntime, effect.reason), outcome.err, cancelErr, destroyErr)
				return s.finishExec(ctx, row, id, started, output, outcome, effect.reason == "exec_deadline", plan.maintenance)
			case persistExec:
				return s.finishExec(ctx, row, id, started, output, outcome, false, plan.maintenance)
			}
		}
	}
}

func (s *Service) finishExec(ctx context.Context, row db.Sandbox, id string, started time.Time, output *capture, outcome jobResult, deadline, maintenance bool) (ExecResult, error) {
	stdout, stderr, outBytes, errBytes, truncated := output.result()
	ended := s.clock.Now()
	result := ExecResult{ID: id, ExitCode: -1, Stdout: stdout, Stderr: stderr, DurationMS: max(0, ended.Sub(started).Milliseconds()), Truncated: truncated, TimedOut: deadline, Cancelled: ctx.Err() != nil}
	if outcome.status != nil {
		result.ExitCode = outcome.status.ExitCode
		result.TimedOut = outcome.status.TimedOut
		result.Cancelled = result.Cancelled || outcome.status.Cancelled
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.store.Queries.FinishExec(recordCtx, db.FinishExecParams{ID: id, EndedAt: sql.NullTime{Time: ended, Valid: true}, ExitCode: sql.NullInt64{Int64: int64(result.ExitCode), Valid: outcome.status != nil}, TimedOut: flag(result.TimedOut), Cancelled: flag(result.Cancelled), Truncated: flag(result.Truncated), StdoutBytes: sql.NullInt64{Int64: outBytes, Valid: true}, StderrBytes: sql.NullInt64{Int64: errBytes, Valid: true}})
	activity := ended
	if activity.After(row.ExpiresAt) {
		activity = row.ExpiresAt
	}
	var activityErr error
	if !maintenance {
		_, activityErr = s.store.Queries.TouchSandbox(recordCtx, db.TouchSandboxParams{ID: row.ID, LastActivityAt: activity})
	}
	kind := "ok"
	if result.ExitCode != 0 {
		kind = "nonzero"
	}
	if outcome.err != nil || err != nil {
		kind = "error"
	}
	if result.TimedOut {
		kind = "timeout"
	}
	if result.Cancelled {
		kind = "cancelled"
	}
	actor := row.OwnerKeyID
	if principal, ok := auth.FromContext(ctx); ok {
		actor = principal.ID
	}
	s.audit.Record("exec.ended", actor, row.ID, map[string]any{"exec_id": id, "template": row.Template, "result": kind, "duration_seconds": float64(result.DurationMS) / 1000})
	// File transfers (sink set) and watchdog scans never reach live views, matching executePlan.
	if !maintenance && output.sink == nil {
		end := events.End{ExitCode: result.ExitCode, Known: outcome.status != nil, TimedOut: result.TimedOut, Truncated: result.Truncated, Cancelled: result.Cancelled}
		s.tails.Finish(output.tail, end)
		s.events.Publish(events.ExecEnded, row.OwnerKeyID, row.ID, map[string]any{"exec_id": id, "result": kind, "exit_code": exitCodeOrNil(end), "timed_out": end.TimedOut, "cancelled": end.Cancelled, "truncated": end.Truncated, "duration_ms": result.DurationMS})
	}
	if failure := errors.Join(outcome.err, err, activityErr, output.sinkErr); failure != nil {
		return result, errors.Join(failure, ctx.Err())
	}
	return result, ctx.Err()
}

func flag(value bool) sql.NullInt64 {
	result := sql.NullInt64{Valid: true}
	if value {
		result.Int64 = 1
	}
	return result
}

func (s *Service) runHelper(ctx context.Context, instance, id string, plan execPlan, output *capture, stream *StreamOptions, files *fileIO) jobResult {
	request := plan.request(id)
	var stdin io.Reader = bytes.NewReader(plan.stdin)
	var attached func(runtime.ExecSession)
	if stream != nil {
		request.TTY = stream.TTY
		if stream.TTY {
			request.Cols = stream.Cols
			request.Rows = stream.Rows
			args := append([]string{}, request.Cmd[:8]...)
			args = append(args, "--tty")
			request.Cmd = append(args, request.Cmd[8:]...)
		}
		if stream.Stdin != nil {
			stop := context.AfterFunc(ctx, func() { _ = stream.Stdin.Close() })
			defer stop()
			defer func() { _ = stream.Stdin.Close() }()
			stdin = streamInput{Reader: io.MultiReader(stdin, stream.Stdin), Closer: stream.Stdin}
		}
		attached = func(session runtime.ExecSession) { stream.Observer.Started(id, StreamControls{Resize: session.Resize}) }
	}
	if files != nil && files.input != nil {
		stop := context.AfterFunc(ctx, func() { _ = files.input.Close() })
		defer stop()
		defer func() { _ = files.input.Close() }()
		stdin = files.input
	}
	exit, err := s.transfer(ctx, instance, request, stdin, output, attached)
	if err != nil {
		return jobResult{err: err}
	}
	if exit.ExitCode != 0 && exit.ExitCode != 124 {
		return jobResult{err: errors.New("helper transport did not confirm completion")}
	}
	data, _, err := s.control(ctx, instance, []string{helperPath, "status", "--id", id, "--consume"})
	if err != nil {
		return jobResult{err: err}
	}
	status, err := parseStatus(data, id, exit.ExitCode)
	if err != nil {
		return jobResult{err: err}
	}
	return jobResult{status: &status}
}

func (s *Service) control(ctx context.Context, instance string, argv []string) ([]byte, runtime.ExitInfo, error) {
	output := &capture{limit: 4096}
	exit, err := s.transfer(ctx, instance, runtime.ExecRequest{Cmd: argv, Cwd: "/workspace"}, bytes.NewReader(nil), output, nil)
	if err != nil {
		return nil, exit, err
	}
	if exit.ExitCode != 0 || output.truncated {
		return nil, exit, errors.New("helper control operation failed")
	}
	return output.stdout.Bytes(), exit, nil
}

func (s *Service) transfer(ctx context.Context, instance string, request runtime.ExecRequest, stdin io.Reader, output *capture, attached func(runtime.ExecSession)) (runtime.ExitInfo, error) {
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := s.driver.Exec(owned, instance, request)
	if err != nil {
		return runtime.ExitInfo{}, err
	}
	if attached != nil {
		attached(session)
	}
	var group errgroup.Group
	copyOutput := func(reader io.Reader, stderr bool) func() error {
		return func() error {
			_, err := io.Copy(captureWriter{output, stderr}, reader)
			if err != nil {
				cancel()
			}
			return err
		}
	}
	group.Go(copyOutput(session.Stdout(), false))
	if session.Stderr() != nil {
		group.Go(copyOutput(session.Stderr(), true))
	}
	group.Go(func() error {
		// A command may legitimately exit without consuming stdin (EPIPE).
		writer := session.Stdin()
		_, _ = io.Copy(writer, stdin)
		_ = writer.Close()
		return nil
	})
	type completion struct {
		exit runtime.ExitInfo
		err  error
	}
	completed := make(chan completion, 1)
	go func() {
		exit, err := session.Wait(owned)
		if err != nil {
			cancel()
		}
		// Process exit must release an interactive stdin reader even if the
		// client never sent stdin_close. Stream EOF is independent of stdin.
		if closer, ok := stdin.(io.Closer); ok {
			_ = closer.Close()
		}
		completed <- completion{exit, err}
	}()
	copyErr := group.Wait()
	result := <-completed
	return result.exit, errors.Join(copyErr, result.err)
}

func parseStatus(data []byte, id string, transport int) (protocol.Status, error) {
	if len(data) > 4096 {
		return protocol.Status{}, errors.New("helper status exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var status protocol.Status
	if err := decoder.Decode(&status); err != nil {
		return status, errors.New("invalid helper status")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return status, errors.New("trailing helper status data")
	}
	if status.ID != id || !status.CleanupComplete || status.ExitCode < 0 || status.ExitCode > 255 || protocol.TransportExit(status.ExitCode) != transport || status.TimedOut && status.Cancelled || len(status.Signal) > 128 {
		return status, errors.New("untrusted helper completion")
	}
	return status, nil
}

func exitCodeOrNil(end events.End) any {
	if !end.Known {
		return nil
	}
	return end.ExitCode
}
