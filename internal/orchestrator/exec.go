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
	plan, err := resolveExec(s.cfg, request)
	if err != nil {
		return ExecResult{}, err
	}
	row, err := s.Activity(ctx, p, id)
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
	if _, err = s.store.Queries.CreateExec(ctx, db.CreateExecParams{ID: execID, SandboxID: id, Command: plan.command, IsShell: shell, Cwd: sql.NullString{String: plan.cwd, Valid: true}, StartedAt: started}); err != nil {
		return ExecResult{}, fmt.Errorf("recording exec start: %w", err)
	}
	return s.superviseExec(ctx, p, row, execID, started, plan)
}

func (s *Service) superviseExec(ctx context.Context, p auth.Principal, row db.Sandbox, id string, started time.Time, plan execPlan) (ExecResult, error) {
	owned, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	timer := s.clock.NewTimer(time.Duration(plan.timeout+10) * time.Second)
	defer timer.Stop()
	output := &capture{limit: s.cfg.Limits.Exec.MaxOutputBytes}
	completed := make(chan jobResult, 1)
	go func() { completed <- s.runHelper(owned, row.ContainerID.String, id, plan, output) }()
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
				return s.finishExec(ctx, row, id, started, output, outcome, effect.reason == "exec_deadline")
			case persistExec:
				return s.finishExec(ctx, row, id, started, output, outcome, false)
			}
		}
	}
}

func (s *Service) finishExec(ctx context.Context, row db.Sandbox, id string, started time.Time, output *capture, outcome jobResult, deadline bool) (ExecResult, error) {
	stdout, stderr, outBytes, errBytes, truncated := output.result()
	ended := s.clock.Now()
	result := ExecResult{ID: id, ExitCode: -1, Stdout: stdout, Stderr: stderr, DurationMS: max(0, ended.Sub(started).Milliseconds()), Truncated: truncated, TimedOut: deadline, Cancelled: ctx.Err() != nil}
	if outcome.status != nil {
		result.ExitCode = outcome.status.ExitCode
		result.TimedOut = outcome.status.TimedOut
		result.Cancelled = outcome.status.Cancelled
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.store.Queries.FinishExec(recordCtx, db.FinishExecParams{ID: id, EndedAt: sql.NullTime{Time: ended, Valid: true}, ExitCode: sql.NullInt64{Int64: int64(result.ExitCode), Valid: outcome.status != nil}, TimedOut: flag(result.TimedOut), Cancelled: flag(result.Cancelled), Truncated: flag(result.Truncated), StdoutBytes: sql.NullInt64{Int64: outBytes, Valid: true}, StderrBytes: sql.NullInt64{Int64: errBytes, Valid: true}})
	activity := ended
	if activity.After(row.ExpiresAt) {
		activity = row.ExpiresAt
	}
	_, activityErr := s.store.Queries.TouchSandbox(recordCtx, db.TouchSandboxParams{ID: row.ID, LastActivityAt: activity})
	return result, errors.Join(outcome.err, err, activityErr, ctx.Err())
}

func flag(value bool) sql.NullInt64 {
	result := sql.NullInt64{Valid: true}
	if value {
		result.Int64 = 1
	}
	return result
}

func (s *Service) runHelper(ctx context.Context, instance, id string, plan execPlan, output *capture) jobResult {
	exit, err := s.transfer(ctx, instance, plan.request(id), plan.stdin, output)
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
	exit, err := s.transfer(ctx, instance, runtime.ExecRequest{Cmd: argv, Cwd: "/workspace"}, nil, output)
	if err != nil {
		return nil, exit, err
	}
	if exit.ExitCode != 0 || output.truncated {
		return nil, exit, errors.New("helper control operation failed")
	}
	return output.stdout.Bytes(), exit, nil
}

func (s *Service) transfer(ctx context.Context, instance string, request runtime.ExecRequest, stdin []byte, output *capture) (runtime.ExitInfo, error) {
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := s.driver.Exec(owned, instance, request)
	if err != nil {
		return runtime.ExitInfo{}, err
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
		_, _ = io.Copy(writer, bytes.NewReader(stdin))
		_ = writer.Close()
		return nil
	})
	if err = group.Wait(); err != nil {
		return runtime.ExitInfo{}, err
	}
	return session.Wait(owned)
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
