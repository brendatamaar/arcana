package docker

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
)

type activeSession struct {
	instance string
	cancel   context.CancelFunc
}

func (d *Driver) reserveSession(ctx context.Context, id string) (context.Context, func(), error) {
	d.sessionsMu.Lock()
	defer d.sessionsMu.Unlock()
	if d.closed || len(d.sessions) >= 1000 || d.nextSession == ^uint64(0) {
		return nil, nil, runtime.Invalid{Field: "exec session limit or closed driver"}
	}
	d.nextSession++
	token := d.nextSession
	owned, cancel := context.WithCancel(ctx)
	d.sessions[token] = activeSession{instance: id, cancel: cancel}
	d.sessionsWG.Add(1)
	var once sync.Once
	release := func() {
		once.Do(func() {
			d.sessionsMu.Lock()
			delete(d.sessions, token)
			d.sessionsMu.Unlock()
			cancel()
			d.sessionsWG.Done()
		})
	}
	return owned, release, nil
}

func (d *Driver) cancelSessions(id string) {
	d.sessionsMu.Lock()
	callbacks := make([]context.CancelFunc, 0)
	for _, session := range d.sessions {
		if session.instance == id {
			callbacks = append(callbacks, session.cancel)
		}
	}
	d.sessionsMu.Unlock()
	for _, cancel := range callbacks {
		cancel()
	}
}

// Exec attaches once, uses pipe backpressure and owns all stream cleanup.
func (d *Driver) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	if len(request.Cmd) == 0 || request.Cmd[0] == "" || len(request.Cmd) > commandArgLimit(request.Cmd) {
		return nil, runtime.Invalid{Field: "cmd"}
	}
	size := 0
	for _, arg := range request.Cmd {
		size += len(arg)
		if size > 131072 {
			return nil, runtime.Invalid{Field: "argv bytes"}
		}
	}
	env, err := environment(request.Env)
	if err != nil {
		return nil, err
	}
	owned, release, err := d.reserveSession(ctx, id)
	if err != nil {
		return nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()
	if _, err = d.inspect(owned, id); err != nil {
		return nil, err
	}
	created, err := d.client.ExecCreate(owned, id, client.ExecCreateOptions{Cmd: request.Cmd, Env: env, WorkingDir: request.Cwd, User: "1000:1000", TTY: request.TTY, ConsoleSize: client.ConsoleSize{Height: uint(request.Rows), Width: uint(request.Cols)}, AttachStdin: true, AttachStdout: true, AttachStderr: !request.TTY})
	if err != nil {
		return nil, fmt.Errorf("creating exec: %w", err)
	}
	attached, err := d.client.ExecAttach(owned, created.ID, client.ExecAttachOptions{TTY: request.TTY, ConsoleSize: client.ConsoleSize{Height: uint(request.Rows), Width: uint(request.Cols)}})
	if err != nil {
		return nil, fmt.Errorf("attaching exec: %w", err)
	}
	out, stdout := io.Pipe()
	errout, stderr := io.Pipe()
	session := &execSession{driver: d, id: created.ID, ctx: ctx, attached: attached, stdout: out, stderr: errout, tty: request.TTY, done: make(chan struct{})}
	stop := context.AfterFunc(owned, func() { attached.Close(); _ = out.CloseWithError(owned.Err()); _ = errout.CloseWithError(owned.Err()) })
	handedOff = true
	go func() {
		defer close(session.done)
		defer release()
		defer attached.Close()
		var copyErr error
		if request.TTY {
			_, copyErr = io.Copy(stdout, attached.Reader)
		} else {
			copyErr = demultiplex(stdout, stderr, attached.Reader)
		}
		session.copyErr = copyErr
		_ = stdout.CloseWithError(copyErr)
		_ = stderr.CloseWithError(copyErr)
		stop()
	}()
	return session, nil
}

// The fixed supervisor prefix is additional to the API's 256 user arguments.
// Only the canonical, bounded wrapper receives that allowance; raw exec stays capped.
func commandArgLimit(argv []string) int {
	const rawLimit = 256
	if len(argv) < 10 || argv[0] != "/opt/arcana/sbx-exec" || argv[1] != "run" || argv[2] != "--id" || len(argv[3]) == 0 || len(argv[3]) > 64 || strings.ContainsRune(argv[3], 0) || argv[4] != "--timeout" || argv[6] != "--cwd" || !path.IsAbs(argv[7]) || len(argv[7]) > 4096 || strings.ContainsRune(argv[7], 0) {
		return rawLimit
	}
	timeout, err := strconv.ParseUint(argv[5], 10, 16)
	if err != nil || timeout < 1 || timeout > protocol.MaxTimeoutSeconds {
		return rawLimit
	}
	separator := 8
	if argv[separator] == "--tty" {
		separator++
	}
	if len(argv) <= separator+1 || argv[separator] != "--" {
		return rawLimit
	}
	return rawLimit + separator + 1
}

// Stream each Docker frame with fixed-size copying rather than allocating its declared size.
func demultiplex(stdout, stderr io.Writer, source io.Reader) error {
	var header [8]byte
	for {
		n, err := io.ReadFull(source, header[:])
		if err == io.EOF && n == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading Docker stream header: %w", err)
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return fmt.Errorf("invalid Docker stream header")
		}
		var target io.Writer
		switch header[0] {
		case 1:
			target = stdout
		case 2:
			target = stderr
		default:
			return fmt.Errorf("unsupported Docker stream type")
		}
		if _, err = io.CopyN(target, source, int64(binary.BigEndian.Uint32(header[4:]))); err != nil {
			return fmt.Errorf("reading Docker stream frame: %w", err)
		}
	}
}

type execSession struct {
	driver    *Driver
	id        string
	ctx       context.Context
	attached  client.ExecAttachResult
	stdout    *io.PipeReader
	stderr    *io.PipeReader
	tty       bool
	done      chan struct{}
	copyErr   error
	stdinOnce sync.Once
	stdinErr  error
}

func (s *execSession) Stdin() io.WriteCloser { return stdinWriter{s} }
func (s *execSession) Stdout() io.Reader     { return s.stdout }
func (s *execSession) Stderr() io.Reader {
	if s.tty {
		return nil
	}
	return s.stderr
}

func (s *execSession) Resize(cols, rows uint16) error {
	if !s.tty {
		return runtime.Unsupported{Feature: "resize without TTY"}
	}
	if cols == 0 || rows == 0 {
		return runtime.Invalid{Field: "terminal size"}
	}
	if _, err := s.driver.client.ExecResize(s.ctx, s.id, client.ExecResizeOptions{Width: uint(cols), Height: uint(rows)}); err != nil {
		return fmt.Errorf("resizing exec: %w", err)
	}
	return nil
}

func (s *execSession) Wait(ctx context.Context) (runtime.ExitInfo, error) {
	select {
	case <-ctx.Done():
		return runtime.ExitInfo{}, ctx.Err()
	case <-s.ctx.Done():
		return runtime.ExitInfo{}, s.ctx.Err()
	case <-s.done:
	}
	if s.copyErr != nil {
		return runtime.ExitInfo{}, fmt.Errorf("reading exec streams: %w", s.copyErr)
	}
	for range 100 {
		inspected, err := s.driver.client.ExecInspect(ctx, s.id, client.ExecInspectOptions{})
		if err != nil {
			return runtime.ExitInfo{}, fmt.Errorf("inspecting exec exit: %w", err)
		}
		if !inspected.Running {
			return runtime.ExitInfo{ExitCode: inspected.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return runtime.ExitInfo{}, ctx.Err()
		case <-s.ctx.Done():
			return runtime.ExitInfo{}, s.ctx.Err()
		case <-s.driver.config.Clock.After(50 * time.Millisecond):
		}
	}
	return runtime.ExitInfo{}, fmt.Errorf("exec exit status unavailable after bounded polling")
}

type stdinWriter struct{ s *execSession }

func (w stdinWriter) Write(p []byte) (int, error) { return w.s.attached.Conn.Write(p) }
func (w stdinWriter) Close() error {
	w.s.stdinOnce.Do(func() { w.s.stdinErr = w.s.attached.CloseWrite() })
	return w.s.stdinErr
}
