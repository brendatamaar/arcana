package api

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/stream"
)

type streamRunner struct {
	service   *orchestrator.Service
	principal auth.Principal
	id        string
}

func (r streamRunner) Run(ctx context.Context, request orchestrator.ExecRequest, options orchestrator.StreamOptions) (orchestrator.ExecResult, error) {
	return r.service.StreamExec(ctx, r.principal, r.id, request, options)
}

// Keep failed handshakes in the API error envelope. Successful hijacking is
// delegated unchanged, including the library's default same-origin policy.
type handshakeWriter struct {
	http.ResponseWriter
	status int
}

func (w *handshakeWriter) WriteHeader(status int) {
	if status >= 400 {
		w.status = status
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *handshakeWriter) Write(data []byte) (int, error) {
	if w.status >= 400 {
		return len(data), nil
	}
	return w.ResponseWriter.Write(data)
}

func (w *handshakeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

// ExecStream authenticates ownership before upgrading, then owns supervised streaming.
func (s *Server) ExecStream(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	principal, _ := auth.FromContext(r.Context())
	if _, err := s.lifecycle.Activity(r.Context(), principal, id); err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.streamMu.Lock()
	if s.closing {
		s.streamMu.Unlock()
		s.failure(w, r, apierr.NewShuttingDown("server is shutting down"))
		return
	}
	select {
	case s.streams <- struct{}{}:
		s.streamWG.Add(1)
		s.streamMu.Unlock()
		defer func() { <-s.streams; s.streamWG.Done() }()
	default:
		s.streamMu.Unlock()
		s.failure(w, r, apierr.NewExecLimitReached("stream connection limit reached"))
		return
	}
	writer := &handshakeWriter{ResponseWriter: w}
	connection, err := websocket.Accept(writer, r, nil)
	if err != nil {
		w.Header().Del("Upgrade")
		w.Header().Del("Connection")
		failure := apierr.NewInvalidRequest("valid WebSocket upgrade required")
		if writer.status == http.StatusForbidden {
			failure = apierr.NewForbidden("WebSocket origin rejected")
		}
		s.failure(w, r, failure)
		return
	}
	stream.Serve(r.Context(), connection, streamRunner{s.lifecycle, principal, id}, orchestrator.SystemClock{}, func(err error) (string, string) {
		failure := sandboxError(err)
		if errors.Is(err, stream.ErrInvalid) || errors.Is(err, stream.ErrInputLimit) {
			failure = apierr.NewInvalidRequest("invalid stream message")
		}
		if errors.Is(err, stream.ErrControl) {
			failure = apierr.NewRuntimeUnavailable("terminal control failed")
		}
		return string(failure.Code), failure.Message
	})
}
