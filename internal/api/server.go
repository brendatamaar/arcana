package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
)

// Server implements the generated v0.1 API contract.
type Server struct {
	keys      *auth.Service
	logger    *slog.Logger
	lifecycle *orchestrator.Service
}

// New builds the middleware stack and generated standard-library router.
func New(keys *auth.Service, logger *slog.Logger, files config.Files, lifecycle *orchestrator.Service) http.Handler {
	server := &Server{keys: keys, logger: logger, lifecycle: lifecycle}
	handler := gen.HandlerWithOptions(server, gen.StdHTTPServerOptions{ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
		server.failure(w, r, apierr.NewInvalidRequest("invalid request parameters"))
	}})
	return apierr.RequestIDs(server.recoverPanics(keys.Middleware(bodyLimits(handler, files))))
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
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
		r.Body = http.MaxBytesReader(w, r.Body, limit)
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
