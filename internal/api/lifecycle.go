package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/store/db"
)

func (s *Server) sandboxFailure(w http.ResponseWriter, r *http.Request, err error) {
	s.failure(w, r, sandboxError(err))
}

func sandboxError(err error) *apierr.Error {
	e := apierr.NewInternal("sandbox operation failed")
	switch {
	case errors.Is(err, orchestrator.ErrInvalid):
		e = apierr.NewInvalidRequest("invalid sandbox options")
	case errors.Is(err, orchestrator.ErrTemplate):
		e = apierr.NewInvalidTemplate("unknown template")
	case errors.Is(err, orchestrator.ErrNotFound):
		e = apierr.NewSandboxNotFound("sandbox not found")
	case errors.Is(err, orchestrator.ErrNotRunning):
		e = apierr.NewSandboxNotRunning("sandbox not running")
	case errors.Is(err, orchestrator.ErrQuota):
		e = apierr.NewQuotaExceeded("sandbox quota exceeded")
	case errors.Is(err, orchestrator.ErrRuntime):
		e = apierr.NewRuntimeUnavailable("runtime operation failed")
	case errors.Is(err, orchestrator.ErrExecLimit):
		e = apierr.NewExecLimitReached("exec concurrency limit reached")
	case errors.Is(err, orchestrator.ErrPath):
		e = apierr.NewInvalidPath("path outside allowed roots")
	case errors.Is(err, orchestrator.ErrFileNotFound):
		e = apierr.NewFileNotFound("sandbox file not found")
	case errors.Is(err, orchestrator.ErrFileDenied):
		e = apierr.NewForbidden("sandbox file access denied")
	case errors.Is(err, orchestrator.ErrFileTooLarge):
		e = apierr.NewPayloadTooLarge("file operation exceeds limit")
	}
	return e
}

func sandboxModel(row db.Sandbox) gen.Sandbox {
	cpus := float32(row.Cpus.Float64)
	labels := map[string]string{}
	_ = json.Unmarshal([]byte(row.Labels.String), &labels)
	result := gen.Sandbox{Id: row.ID, Template: row.Template, Runtime: row.Runtime, State: gen.SandboxState(row.State), Source: gen.SandboxSource(row.Source), CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt, IdleTimeoutSeconds: &row.IdleTimeoutSeconds, LastActivityAt: &row.LastActivityAt, Labels: labels, Network: gen.Network{Mode: gen.NetworkMode(row.NetworkMode)}, Resources: gen.Resources{Cpus: &cpus, MemoryMb: &row.MemoryMb.Int64, DiskMb: &row.DiskMb.Int64, Pids: &row.Pids.Int64}}
	if row.EndedAt.Valid {
		result.EndedAt = &row.EndedAt.Time
	}
	if row.EndReason.Valid {
		result.EndReason = &row.EndReason.String
	}
	return result
}

// CreateSandbox resolves a template and atomically reserves lifecycle quota.
func (s *Server) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	var request gen.CreateSandbox
	if !s.decode(w, r, &request) {
		return
	}
	input := orchestrator.CreateRequest{Template: request.Template, TTLSeconds: request.TtlSeconds, IdleTimeoutSeconds: request.IdleTimeoutSeconds}
	if request.Env != nil {
		input.Env = *request.Env
	}
	if request.Labels != nil {
		input.Labels = *request.Labels
	}
	if request.Network != nil {
		input.NetworkMode = string(request.Network.Mode)
		if input.NetworkMode == "" {
			s.failure(w, r, apierr.NewInvalidRequest("network.mode is required"))
			return
		}
	}
	if request.Resources != nil {
		input.Resources = orchestrator.Overrides{MemoryMB: request.Resources.MemoryMb, DiskMB: request.Resources.DiskMb, PIDs: request.Resources.Pids}
		if request.Resources.Cpus != nil {
			value := float64(*request.Resources.Cpus)
			input.Resources.CPUs = &value
		}
	}
	p, _ := auth.FromContext(r.Context())
	row, err := s.lifecycle.Create(r.Context(), p, input)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respond(w, r, http.StatusCreated, sandboxModel(row))
}

// GetSandbox returns metadata after ownership verification.
func (s *Server) GetSandbox(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	row, err := s.lifecycle.Get(r.Context(), p, id)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respond(w, r, 200, sandboxModel(row))
}

// ListSandboxes filters owned records before applying a bounded page.
func (s *Server) ListSandboxes(w http.ResponseWriter, r *http.Request, params gen.ListSandboxesParams) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	input := orchestrator.ListRequest{Limit: 100}
	if params.Limit != nil {
		input.Limit = *params.Limit
	}
	if params.Cursor != nil {
		input.Cursor = *params.Cursor
	}
	if params.State != nil {
		input.State = string(*params.State)
	}
	if params.Label != nil {
		input.Label = *params.Label
	}
	p, _ := auth.FromContext(r.Context())
	rows, err := s.lifecycle.List(r.Context(), p, input)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	result := gen.SandboxList{Items: make([]gen.Sandbox, 0, len(rows))}
	for _, row := range rows {
		result.Items = append(result.Items, sandboxModel(row))
	}
	if int64(len(rows)) == input.Limit {
		cursor := rows[len(rows)-1].ID
		result.NextCursor = &cursor
	}
	s.respond(w, r, 200, result)
}

// DestroySandbox performs idempotent instance cleanup.
func (s *Server) DestroySandbox(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	if err := s.lifecycle.Destroy(r.Context(), p, id, "requested"); err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// KeepaliveSandbox resets idle activity without extending absolute TTL.
func (s *Server) KeepaliveSandbox(w http.ResponseWriter, r *http.Request, id string) {
	if s.lifecycle == nil {
		s.notImplemented(w, r)
		return
	}
	p, _ := auth.FromContext(r.Context())
	row, err := s.lifecycle.Activity(r.Context(), p, id)
	if err != nil {
		s.sandboxFailure(w, r, err)
		return
	}
	s.respond(w, r, 200, sandboxModel(row))
}
