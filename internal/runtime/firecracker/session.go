package firecracker

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd"
	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// execSession adapts one envd Exec stream to runtime.ExecSession.
type execSession struct {
	stream  envdpb.Envd_ExecClient
	cancel  context.CancelFunc
	tty     bool
	sendMu  sync.Mutex
	stdin   *stdinWriter
	stdout  *io.PipeReader
	stderr  *io.PipeReader
	done    chan struct{}
	exit    runtime.ExitInfo
	exitErr error
}

type stdinWriter struct{ s *execSession }

func (w stdinWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), envd.MaxChunkBytes)
		if err := w.s.send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Stdin{Stdin: append([]byte(nil), p[:n]...)}}); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (w stdinWriter) Close() error {
	return w.s.send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_StdinClose{StdinClose: &envdpb.StdinClose{}}})
}

func (s *execSession) send(message *envdpb.ExecClientMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(message)
}

// newExecSession starts the receive loop. Cancelling ctx disconnects the stream, which
// makes envd cancel the command and its whole process tree.
func newExecSession(stream envdpb.Envd_ExecClient, cancel context.CancelFunc, tty bool) *execSession {
	s := &execSession{stream: stream, cancel: cancel, tty: tty, done: make(chan struct{})}
	s.stdin = &stdinWriter{s}
	outR, outW := io.Pipe()
	s.stdout = outR
	var errW *io.PipeWriter
	if !tty {
		s.stderr, errW = io.Pipe()
	}
	go s.receive(outW, errW)
	return s
}

func (s *execSession) receive(stdout, stderr *io.PipeWriter) {
	defer close(s.done)
	finish := func(err error) {
		_ = stdout.CloseWithError(err)
		if stderr != nil {
			_ = stderr.CloseWithError(err)
		}
	}
	for {
		message, err := s.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("envd closed the stream without an exit")
			}
			s.exitErr = err
			finish(err)
			return
		}
		switch m := message.Message.(type) {
		case *envdpb.ExecServerMessage_Output:
			target := stdout
			if m.Output.GetStream() == envdpb.OutputStream_OUTPUT_STREAM_STDERR && stderr != nil {
				target = stderr
			}
			if _, err = target.Write(m.Output.GetData()); err != nil {
				// The reader closed; keep draining the stream until the exit.
				continue
			}
		case *envdpb.ExecServerMessage_Exit:
			s.exit = runtime.ExitInfo{ExitCode: int(m.Exit.GetExitCode()), TimedOut: m.Exit.GetTimedOut(), Signal: m.Exit.GetSignal()}
			finish(nil)
			return
		}
	}
}

func (s *execSession) Stdin() io.WriteCloser { return s.stdin }
func (s *execSession) Stdout() io.Reader     { return s.stdout }
func (s *execSession) Stderr() io.Reader {
	if s.stderr == nil {
		return nil
	}
	return s.stderr
}

func (s *execSession) Resize(cols, rows uint16) error {
	if !s.tty {
		return runtime.Unsupported{Feature: "resize without a terminal"}
	}
	return s.send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Resize{Resize: &envdpb.Resize{Cols: uint32(cols), Rows: uint32(rows)}}})
}

// Wait returns the exit once envd has proven the command's tree is gone. A cancelled
// context disconnects the stream, which cancels the command.
func (s *execSession) Wait(ctx context.Context) (runtime.ExitInfo, error) {
	select {
	case <-s.done:
	case <-ctx.Done():
		s.cancel()
		<-s.done
		return runtime.ExitInfo{}, ctx.Err()
	}
	s.cancel()
	if s.exitErr != nil {
		// A stream torn down by the caller's own cancellation reports a gRPC Canceled; the caller asked
		// for it and expects its context's error.
		if ctx.Err() != nil && status.Code(s.exitErr) == codes.Canceled {
			return runtime.ExitInfo{}, ctx.Err()
		}
		return runtime.ExitInfo{}, s.exitErr
	}
	return s.exit, nil
}
