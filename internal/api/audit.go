package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/audit"
)

// ListAudit exposes the global audit log only to administrators.
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, params gen.ListAuditParams) {
	if !s.admin(w, r) {
		return
	}
	if s.audit == nil {
		s.notImplemented(w, r)
		return
	}
	filter := audit.Filter{Limit: 100}
	if params.Limit != nil {
		if *params.Limit < 1 || *params.Limit > 1000 {
			s.failure(w, r, apierr.NewInvalidRequest("limit must be 1..1000"))
			return
		}
		filter.Limit = *params.Limit
	}
	if params.Cursor != nil {
		filter.Cursor = *params.Cursor
	}
	if params.SandboxId != nil {
		filter.SandboxID = *params.SandboxId
	}
	if params.KeyId != nil {
		filter.KeyID = *params.KeyId
	}
	if params.Action != nil {
		filter.Action = *params.Action
	}
	if params.Since != nil {
		filter.Since = *params.Since
	}
	if params.Until != nil {
		filter.Until = *params.Until
	}
	page, err := s.audit.List(r.Context(), filter)
	if err != nil {
		if errors.Is(err, audit.ErrFilter) {
			s.failure(w, r, apierr.NewInvalidRequest("invalid audit filters"))
		} else {
			s.failure(w, r, apierr.NewInternal("querying audit failed"))
		}
		return
	}
	result := gen.AuditList{Items: make([]gen.AuditEvent, 0, len(page.Items))}
	if page.NextCursor != "" {
		result.NextCursor = &page.NextCursor
	}
	for _, event := range page.Items {
		details := make(map[string]any)
		if err = json.Unmarshal(event.Details, &details); err != nil {
			s.failure(w, r, apierr.NewInternal("invalid audit metadata"))
			return
		}
		result.Items = append(result.Items, gen.AuditEvent{Id: event.ID, Ts: event.TS, KeyId: event.KeyID, SandboxId: event.SandboxID, Action: event.Action, Details: details})
	}
	s.respond(w, r, http.StatusOK, result)
}
