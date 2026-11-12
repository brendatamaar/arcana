package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// WithEvents attaches the dashboard's event bus and per-exec output rings. Output is kept only in
// memory, sized by limits.exec.tail_buffer_bytes. Call it before the service handles requests.
func (s *Service) WithEvents(bus *events.Bus) *Service {
	s.events = bus
	e := s.cfg.Limits.Exec
	s.tails = events.NewTails(s.clock, e.TailBufferBytes, time.Duration(e.TailRetentionSeconds)*time.Second, e.MaxTailSubscribers)
	return s
}

// Events returns the event bus, or nil when the dashboard backend is off.
func (s *Service) Events() *events.Bus { return s.events }

// TailSubscribers reports active exec-output readers, for metrics.
func (s *Service) TailSubscribers() int { return s.tails.Subscribers() }

// ExecOutput resolves an exec's live output ring after checking sandbox ownership. A nil tail with
// a nil error means the exec exists but its output is no longer retained.
func (s *Service) ExecOutput(ctx context.Context, p auth.Principal, sandboxID, execID string) (*events.Tail, db.Exec, error) {
	if _, err := s.Get(ctx, p, sandboxID); err != nil {
		return nil, db.Exec{}, err
	}
	row, err := s.store.Queries.GetExec(ctx, execID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.SandboxID != sandboxID {
		return nil, db.Exec{}, ErrNotFound
	}
	if err != nil {
		return nil, db.Exec{}, fmt.Errorf("getting exec: %w", err)
	}
	tail, ok := s.tails.Lookup(execID)
	if !ok {
		return nil, row, nil
	}
	return tail, row, nil
}

// ListExecs returns one newest-first page of a sandbox's exec history, after an ownership check.
func (s *Service) ListExecs(ctx context.Context, p auth.Principal, sandboxID string, limit int64, cursor string) ([]db.Exec, error) {
	if limit < 1 || limit > 1000 || len(cursor) > 128 {
		return nil, ErrInvalid
	}
	if _, err := s.Get(ctx, p, sandboxID); err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListExecsNewestFirst(ctx, db.ListExecsNewestFirstParams{SandboxID: sandboxID, Cursor: cursor, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("listing execs: %w", err)
	}
	return rows, nil
}

// TemplateCount is a per-template number.
type TemplateCount struct {
	Template string
	Count    int64
	// Target is the configured warm pool size; only set for pool entries.
	Target int
}

// Overview is the dashboard's landing data.
type Overview struct {
	Runtime          string
	ActiveByTemplate []TemplateCount
	// Pool is empty for non-admin keys: pool fill is installation-wide.
	Pool   []TemplateCount
	Recent []db.Sandbox
}

// Overview returns global numbers to admin keys and the caller's own to everyone else.
func (s *Service) Overview(ctx context.Context, p auth.Principal) (Overview, error) {
	owner := p.ID
	if p.Admin {
		owner = ""
	}
	counts, err := s.store.Queries.CountActiveByTemplate(ctx, owner)
	if err != nil {
		return Overview{}, fmt.Errorf("counting sandboxes: %w", err)
	}
	recent, err := s.store.Queries.RecentEndedSandboxes(ctx, db.RecentEndedSandboxesParams{Owner: owner, PageLimit: 20})
	if err != nil {
		return Overview{}, fmt.Errorf("listing recent terminations: %w", err)
	}
	out := Overview{Runtime: s.cfg.Runtime.Docker.OCIRuntime, Recent: recent}
	for _, c := range counts {
		out.ActiveByTemplate = append(out.ActiveByTemplate, TemplateCount{Template: c.Template, Count: c.Active})
	}
	if p.Admin {
		available := s.PoolAvailable()
		for _, t := range s.cfg.Templates {
			if t.WarmPool > 0 || available[t.Name] > 0 {
				out.Pool = append(out.Pool, TemplateCount{Template: t.Name, Count: int64(available[t.Name]), Target: t.WarmPool})
			}
		}
	}
	return out, nil
}

// History returns a sandbox's persisted state transitions in order, after an ownership check.
func (s *Service) History(ctx context.Context, p auth.Principal, sandboxID string) ([]db.SandboxTransition, error) {
	if _, err := s.Get(ctx, p, sandboxID); err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListSandboxTransitions(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("listing transitions: %w", err)
	}
	return rows, nil
}
