package orchestrator

import (
	"context"
	"io"

	"github.com/brendatamaar/arcana/internal/auth"
)

// StreamControls exposes only operations valid during an attached exec.
type StreamControls struct{ Resize func(cols, rows uint16) error }

// StreamObserver receives started before any output. Output must copy its bytes
// before returning and must never block; false reports dropped output.
type StreamObserver interface {
	Started(id string, controls StreamControls)
	Output(stderr bool, data []byte) bool
}

// StreamOptions supplies bounded transport capabilities. Stdin.Close must unblock Read.
type StreamOptions struct {
	TTY        bool
	Cols, Rows uint16
	Stdin      io.ReadCloser
	Observer   StreamObserver
}

type streamInput struct {
	io.Reader
	io.Closer
}

// StreamExec uses the same persistence, limits, cancellation and cleanup proof as sync exec.
func (s *Service) StreamExec(ctx context.Context, p auth.Principal, id string, request ExecRequest, options StreamOptions) (ExecResult, error) {
	if options.TTY && (options.Cols == 0 || options.Rows == 0) || options.Observer == nil {
		return ExecResult{}, ErrInvalid
	}
	if options.TTY && !s.driver.Capabilities().SupportsTTY {
		return ExecResult{}, ErrInvalid
	}
	return s.execute(ctx, p, id, request, &options)
}
