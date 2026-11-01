package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// Domain failures are mapped to wire errors only by the API boundary.
var (
	ErrInvalid    = errors.New("invalid sandbox request")
	ErrTemplate   = errors.New("invalid template")
	ErrNotFound   = errors.New("sandbox not found")
	ErrNotRunning = errors.New("sandbox not running")
	ErrQuota      = errors.New("sandbox quota exceeded")
	ErrRuntime    = errors.New("runtime unavailable")
)

// Clock supplies timestamps to the orchestration shell.
type Clock interface{ Now() time.Time }

// SystemClock uses UTC wall time.
type SystemClock struct{}

// Now returns UTC wall time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Service owns persistence and runtime effects; reservations are atomic in SQLite.
type Service struct {
	store      *store.Store
	driver     runtime.Driver
	cfg        config.Config
	clock      Clock
	createGate chan struct{}
}

// New constructs an orchestrator with independent operator configuration.
func New(database *store.Store, driver runtime.Driver, cfg config.Config, clock Clock) *Service {
	cfg.Templates = slices.Clone(cfg.Templates)
	for i := range cfg.Templates {
		cfg.Templates[i].Env = maps.Clone(cfg.Templates[i].Env)
	}
	cfg.Limits.Files.AllowedRoots = slices.Clone(cfg.Limits.Files.AllowedRoots)
	return &Service{store: database, driver: driver, cfg: cfg, clock: clock, createGate: make(chan struct{}, 1)}
}

// Create reserves quota before starting the instance and persists every outcome.
func (s *Service) Create(ctx context.Context, principal auth.Principal, request CreateRequest) (db.Sandbox, error) {
	plan, err := resolve(s.cfg, request)
	if err != nil {
		return db.Sandbox{}, err
	}
	id, err := ids.New("sbx")
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("generating sandbox ID: %w", err)
	}
	plan.spec.SandboxID = id
	row, err := s.reserve(ctx, principal, id, plan)
	if err != nil {
		return db.Sandbox{}, err
	}
	instance, createErr := s.driver.Create(ctx, plan.spec)
	// Cancellation must not discard an already-created instance or leave a stale reservation.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if createErr != nil {
		_, persistErr := s.transition(finishCtx, row, CreateFailed, "create_failed", "")
		return db.Sandbox{}, errors.Join(fmt.Errorf("%w: creating instance", ErrRuntime), persistErr)
	}
	result, err := s.transition(finishCtx, row, Created, "", instance.ID)
	if err != nil {
		cleanupErr := s.driver.Destroy(finishCtx, instance.ID)
		return db.Sandbox{}, errors.Join(err, cleanupErr)
	}
	return result, nil
}

func (s *Service) reserve(ctx context.Context, p auth.Principal, id string, plan resolved) (db.Sandbox, error) {
	// The cancellable gate avoids same-process deferred transaction lock upgrades.
	select {
	case s.createGate <- struct{}{}:
		defer func() { <-s.createGate }()
	case <-ctx.Done():
		return db.Sandbox{}, ctx.Err()
	}
	tx, err := s.store.SQL.BeginTx(ctx, nil)
	if err != nil {
		return db.Sandbox{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.store.Queries.WithTx(tx)
	global, err := q.CountActiveSandboxes(ctx, "")
	if err != nil {
		return db.Sandbox{}, err
	}
	own, err := q.CountActiveSandboxes(ctx, p.ID)
	if err != nil {
		return db.Sandbox{}, err
	}
	if global >= int64(s.cfg.Limits.MaxSandboxes) || own >= int64(s.cfg.Limits.MaxSandboxesPerKey) {
		return db.Sandbox{}, ErrQuota
	}
	now := s.clock.Now()
	keys := make([]string, 0, len(plan.spec.Env))
	for key := range plan.spec.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	envKeys, _ := json.Marshal(keys)
	labels := plan.spec.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	labelData, _ := json.Marshal(labels)
	r := plan.spec.Resources
	row, err := q.CreateSandbox(ctx, db.CreateSandboxParams{ID: id, OwnerKeyID: p.ID, Template: plan.template, Runtime: s.cfg.Runtime.Docker.OCIRuntime, State: string(Creating), Source: "cold", Cpus: sql.NullFloat64{Float64: r.CPUs, Valid: true}, MemoryMb: sql.NullInt64{Int64: r.MemoryMB, Valid: true}, DiskMb: sql.NullInt64{Int64: r.DiskMB, Valid: true}, Pids: sql.NullInt64{Int64: r.PIDs, Valid: true}, NetworkMode: plan.spec.NetworkMode, EnvKeys: sql.NullString{String: string(envKeys), Valid: true}, Labels: sql.NullString{String: string(labelData), Valid: true}, CreatedAt: now, ExpiresAt: now.Add(time.Duration(plan.ttl) * time.Second), IdleTimeoutSeconds: plan.idle, LastActivityAt: now})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("reserving sandbox: %w", err)
	}
	if err = q.RecordSandboxTransition(ctx, db.RecordSandboxTransitionParams{SandboxID: id, State: string(Creating), At: now}); err != nil {
		return db.Sandbox{}, err
	}
	if err = tx.Commit(); err != nil {
		return db.Sandbox{}, err
	}
	return row, nil
}

func (s *Service) transition(ctx context.Context, row db.Sandbox, event Event, reason, instance string) (db.Sandbox, error) {
	next, commands := Step(Phase(row.State), event)
	if len(commands) == 0 {
		return db.Sandbox{}, ErrNotRunning
	}
	now := s.clock.Now()
	ended := sql.NullTime{Time: now, Valid: terminal(next)}
	container := row.ContainerID
	if instance != "" {
		container = sql.NullString{String: instance, Valid: true}
	}
	endReason := sql.NullString{String: reason, Valid: reason != ""}
	tx, err := s.store.SQL.BeginTx(ctx, nil)
	if err != nil {
		return db.Sandbox{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.store.Queries.WithTx(tx)
	n, err := q.UpdateSandboxState(ctx, db.UpdateSandboxStateParams{State: string(next), ContainerID: container, EndedAt: ended, EndReason: endReason, ID: row.ID, PreviousState: row.State})
	if err != nil {
		return db.Sandbox{}, err
	}
	if n != 1 {
		return db.Sandbox{}, ErrNotRunning
	}
	if err = q.RecordSandboxTransition(ctx, db.RecordSandboxTransitionParams{SandboxID: row.ID, State: string(next), At: now, Reason: endReason}); err != nil {
		return db.Sandbox{}, err
	}
	row, err = q.GetSandbox(ctx, row.ID)
	if err != nil {
		return db.Sandbox{}, err
	}
	if err = tx.Commit(); err != nil {
		return db.Sandbox{}, err
	}
	return row, nil
}

// Get checks ownership before exposing a record or its state.
func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (db.Sandbox, error) {
	row, err := s.store.Queries.GetSandbox(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !p.Admin && row.OwnerKeyID != p.ID {
		return db.Sandbox{}, ErrNotFound
	}
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("getting sandbox: %w", err)
	}
	return row, nil
}

// ListRequest bounds a page and optional state/label filters.
type ListRequest struct {
	Cursor, State, Label string
	Limit                int64
}

// List returns only records visible to the caller, with filtering before pagination.
func (s *Service) List(ctx context.Context, p auth.Principal, request ListRequest) ([]db.Sandbox, error) {
	if request.Limit < 1 || request.Limit > 1000 || len(request.Cursor) > 128 || request.State != "" && !Phase(request.State).Valid() || len(request.Label) > 4225 {
		return nil, ErrInvalid
	}
	key, value := "", ""
	if request.Label != "" {
		var ok bool
		key, value, ok = strings.Cut(request.Label, ":")
		if !ok || key == "" {
			return nil, ErrInvalid
		}
	}
	owner := p.ID
	if p.Admin {
		owner = ""
	}
	rows, err := s.store.Queries.ListSandboxes(ctx, db.ListSandboxesParams{ID: request.Cursor, Owner: owner, State: request.State, PageLimit: request.Limit, LabelKey: key, LabelValue: sql.NullString{String: value, Valid: true}})
	if err != nil {
		return nil, fmt.Errorf("listing sandboxes: %w", err)
	}
	return rows, nil
}

// Destroy removes a running instance; repeated calls on ended records succeed.
func (s *Service) Destroy(ctx context.Context, p auth.Principal, id, reason string) error {
	row, err := s.Get(ctx, p, id)
	if err != nil {
		return err
	}
	if terminal(Phase(row.State)) {
		return nil
	}
	if row.State == string(Running) {
		row, err = s.transition(ctx, row, DestroyRequested, reason, "")
		if err != nil {
			if !errors.Is(err, ErrNotRunning) {
				return err
			}
			row, err = s.Get(ctx, p, id)
			if err != nil {
				return err
			}
			if terminal(Phase(row.State)) {
				return nil
			}
		}
	}
	if row.State != string(Destroying) {
		return ErrNotRunning
	}
	if !row.ContainerID.Valid {
		return fmt.Errorf("%w: missing runtime handle", ErrRuntime)
	}
	if err = s.driver.Destroy(ctx, row.ContainerID.String); err != nil {
		return fmt.Errorf("%w: destroying instance", ErrRuntime)
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	event := DestroyedInstance
	if row.EndReason.String == "ttl" || row.EndReason.String == "idle" {
		event = ExpiredInstance
	}
	_, err = s.transition(finishCtx, row, event, row.EndReason.String, "")
	if errors.Is(err, ErrNotRunning) {
		current, getErr := s.Get(finishCtx, p, id)
		if getErr == nil && terminal(Phase(current.State)) {
			return nil
		}
	}
	return err
}

// Activity resets the idle clock without extending the absolute expiry.
func (s *Service) Activity(ctx context.Context, p auth.Principal, id string) (db.Sandbox, error) {
	row, err := s.Get(ctx, p, id)
	if err != nil {
		return db.Sandbox{}, err
	}
	now := s.clock.Now()
	if row.State != string(Running) || !now.Before(row.ExpiresAt) {
		return db.Sandbox{}, ErrNotRunning
	}
	// Do not move activity backwards if calls race with a previously observed time.
	now = maxTime(now, row.LastActivityAt)
	n, err := s.store.Queries.TouchSandbox(ctx, db.TouchSandboxParams{ID: id, LastActivityAt: now})
	if err != nil {
		return db.Sandbox{}, err
	}
	if n != 1 {
		return db.Sandbox{}, ErrNotRunning
	}
	return s.Get(ctx, p, id)
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}
