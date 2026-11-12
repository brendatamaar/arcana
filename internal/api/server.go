package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
)

// Options supplies optional later-phase services.
type Options struct {
	CreatePerMinute int
	// MaxFork bounds one fork request; the orchestrator enforces it again. Zero means 10.
	MaxFork    int
	RateClock  RateClock
	Audit      *audit.Writer
	Middleware func(http.Handler) http.Handler
}

// Server implements the generated API contract.
type Server struct {
	limiter   *creationLimiter
	audit     *audit.Writer
	keys      *auth.Service
	logger    *slog.Logger
	lifecycle *orchestrator.Service
	maxFork   int
	streams   chan struct{}
	streamMu  sync.Mutex
	streamWG  sync.WaitGroup
	closing   bool
}

// New builds the middleware stack and generated standard-library router.
func New(keys *auth.Service, logger *slog.Logger, files config.Files, lifecycle *orchestrator.Service, options ...Options) http.Handler {
	var recorder *audit.Writer
	if len(options) > 0 {
		recorder = options[0].Audit
	}
	rate := 30
	var clock RateClock
	if len(options) > 0 {
		if options[0].CreatePerMinute != 0 {
			rate = options[0].CreatePerMinute
		}
		clock = options[0].RateClock
	}
	maxFork := 10
	if len(options) > 0 && options[0].MaxFork > 0 {
		maxFork = options[0].MaxFork
	}
	server := &Server{limiter: newCreationLimiter(rate, clock), maxFork: maxFork, audit: recorder, keys: keys, logger: logger, lifecycle: lifecycle, streams: make(chan struct{}, 1000)}
	handler := gen.HandlerWithOptions(server, gen.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		server.failure(w, r, apierr.NewInvalidRequest("invalid request parameters"))
	}})
	stack := apierr.RequestIDs(server.recoverPanics(keys.Middleware(bodyLimits(handler, files))))
	if len(options) > 0 && options[0].Middleware != nil {
		stack = options[0].Middleware(stack)
	}
	return &managedHandler{Handler: stack, server: server}
}

type managedHandler struct {
	http.Handler
	server *Server
}

func (h *managedHandler) beginShutdown() {
	h.server.streamMu.Lock()
	h.server.closing = true
	h.server.streamMu.Unlock()
}

func (h *managedHandler) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { h.server.streamWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				s.logger.ErrorContext(r.Context(), "request panic recovered", "request_id", apierr.RequestID(r.Context()))
				s.failure(w, r, apierr.NewInternal("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func bodyLimits(next http.Handler, files config.Files) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(65536)
		if r.Method == http.MethodPut {
			if strings.HasSuffix(r.URL.Path, "/files/archive") {
				limit = files.MaxArchiveBytes
			} else if strings.HasSuffix(r.URL.Path, "/files") {
				limit = files.MaxFileBytes
			}
		}
		if r.ContentLength > limit {
			_ = apierr.Write(w, r, apierr.NewPayloadTooLarge("request body exceeds limit"))
			return
		}
		readLimit := limit
		if r.Method == http.MethodPut && (strings.HasSuffix(r.URL.Path, "/files") || strings.HasSuffix(r.URL.Path, "/files/archive")) {
			readLimit++
		}
		r.Body = http.MaxBytesReader(w, r.Body, readLimit)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) failure(w http.ResponseWriter, r *http.Request, e *apierr.Error) {
	if err := apierr.Write(w, r, e); err != nil {
		s.logger.DebugContext(r.Context(), "error response write failed", "request_id", apierr.RequestID(r.Context()))
	}
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.logger.DebugContext(r.Context(), "response write failed", "request_id", apierr.RequestID(r.Context()))
	}
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	if media, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); media != "application/json" {
		s.failure(w, r, apierr.NewInvalidRequest("Content-Type must be application/json"))
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(destination)
	if err == nil {
		var extra any
		if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
			err = extraErr
			if extraErr == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err == nil {
		return true
	}
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		s.failure(w, r, apierr.NewPayloadTooLarge("request body exceeds limit"))
	} else {
		s.failure(w, r, apierr.NewInvalidRequest("invalid JSON body"))
	}
	return false
}

func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, http.StatusNotImplemented, apierr.Envelope{Error: apierr.Error{Code: apierr.Internal, Message: "operation not implemented", RequestID: apierr.RequestID(r.Context())}})
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) bool {
	principal, ok := auth.FromContext(r.Context())
	if !ok {
		s.failure(w, r, apierr.NewUnauthorized("authentication required"))
		return false
	}
	if !principal.Admin {
		s.failure(w, r, apierr.NewForbidden("admin key required"))
		return false
	}
	return true
}

// Health is public liveness.
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, 200, map[string]string{"status": "ok"})
}

// CreateKey creates a key and returns its secret once to an admin.
func (s *Server) CreateKey(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	var request gen.CreateKey
	if !s.decode(w, r, &request) {
		return
	}
	if len(request.Name) == 0 || len(request.Name) > 128 || strings.ContainsRune(request.Name, 0) {
		s.failure(w, r, apierr.NewInvalidRequest("name must contain 1..128 bytes without NUL"))
		return
	}
	admin := request.IsAdmin != nil && *request.IsAdmin
	key, secret, err := s.keys.Create(r.Context(), request.Name, admin)
	if err != nil {
		s.failure(w, r, apierr.NewInternal("creating key failed"))
		return
	}
	s.respond(w, r, 201, map[string]any{"key": key, "secret": secret})
}

// ListKeys returns a page of secret-free key metadata to an admin.
func (s *Server) ListKeys(w http.ResponseWriter, r *http.Request, params gen.ListKeysParams) {
	if !s.admin(w, r) {
		return
	}
	limit := int64(100)
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 1000 {
		s.failure(w, r, apierr.NewInvalidRequest("limit must be 1..1000"))
		return
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	if len(cursor) > 128 {
		s.failure(w, r, apierr.NewInvalidRequest("cursor too long"))
		return
	}
	keys, err := s.keys.List(r.Context(), cursor, limit)
	if err != nil {
		s.failure(w, r, apierr.NewInternal("listing keys failed"))
		return
	}
	var next *string
	if int64(len(keys)) == limit {
		id := keys[len(keys)-1].ID
		next = &id
	}
	s.respond(w, r, 200, map[string]any{"items": keys, "next_cursor": next})
}

// RevokeKey revokes a key for subsequent requests.
func (s *Server) RevokeKey(w http.ResponseWriter, r *http.Request, id string) {
	if !s.admin(w, r) {
		return
	}
	if err := s.keys.Revoke(r.Context(), id); err != nil {
		s.failure(w, r, apierr.NewInternal("revoking key failed"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
