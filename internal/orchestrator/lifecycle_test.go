package orchestrator

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

type testClock struct{ at time.Time }

func (c *testClock) Now() time.Time { return c.at }

func fixture(t *testing.T) (*Service, *fake.Driver, auth.Principal, auth.Principal, *testClock) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(database, auth.SystemClock{})
	owner, _, err := keys.Create(context.Background(), "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := keys.Create(context.Background(), "other", false)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	driver := fake.New()
	cfg := config.Default()
	cfg.Templates[0].Env = map[string]string{"DEFAULT": "template-secret"}
	cfg.Templates[0].Resources.CPUs = 2
	cfg.Templates[0].TTLSeconds = 120
	return New(database, driver, cfg, clock), driver, auth.Principal{ID: owner.ID}, auth.Principal{ID: other.ID}, clock
}

func TestLifecycle_ClampsMergesPersistsAndDestroys(t *testing.T) {
	s, driver, p, _, clock := fixture(t)
	ctx := context.Background()
	high := int64(999999)
	cpu := 999.0
	row, err := s.Create(ctx, p, CreateRequest{Template: "base", TTLSeconds: &high, Resources: Overrides{CPUs: &cpu, MemoryMB: &high, DiskMB: &high, PIDs: &high}, Env: map[string]string{"DEFAULT": "override-secret", "NEW": "new-secret"}, Labels: map[string]string{"project": "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := driver.Spec(row.ContainerID.String)
	if !ok || spec.Resources.CPUs != 4 || spec.Resources.MemoryMB != 8192 || spec.Resources.DiskMB != 20480 || spec.Resources.PIDs != 4096 || spec.NetworkMode != "none" || spec.Env["DEFAULT"] != "override-secret" || spec.Workdir != "/workspace" {
		t.Fatalf("unexpected resolved spec: resources=%+v", spec.Resources)
	}
	if row.State != "running" || row.Runtime != "runsc" || row.ExpiresAt.Sub(row.CreatedAt) != 86400*time.Second || !strings.Contains(row.EnvKeys.String, "NEW") || strings.Contains(row.EnvKeys.String, "secret") {
		t.Fatal("incorrect persisted metadata")
	}
	before := row.ExpiresAt
	clock.at = clock.at.Add(time.Minute)
	active, err := s.Activity(ctx, p, row.ID)
	if err != nil || !active.ExpiresAt.Equal(before) || !active.LastActivityAt.Equal(clock.at) {
		t.Fatal("keepalive changed TTL or lost activity", err)
	}
	if err = s.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	if err = s.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal("destroy not idempotent", err)
	}
	ended, err := s.Get(ctx, p, row.ID)
	if err != nil || ended.State != "destroyed" || !ended.EndedAt.Valid || ended.EndReason.String != "requested" {
		t.Fatal("missing terminal metadata", err)
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 0 {
		t.Fatal("runtime leak", err)
	}
	var history string
	if err = s.store.SQL.QueryRowContext(ctx, "SELECT group_concat(state,',') FROM (SELECT state FROM sandbox_transitions WHERE sandbox_id=? ORDER BY sequence)", row.ID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != "creating,running,destroying,destroyed" {
		t.Fatal(history)
	}
	if _, err = s.Activity(ctx, p, row.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("ended sandbox activity accepted")
	}
}

func TestLifecycle_OwnershipFilteringAndPagination(t *testing.T) {
	s, _, p, other, _ := fixture(t)
	ctx := context.Background()
	// Dots, quotes and colons in label values must remain data in the SQL predicate.
	labels := map[string]string{"project.name": "quoted\"value:tail"}
	own, err := s.Create(ctx, p, CreateRequest{Template: "base", Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.Create(ctx, other, CreateRequest{Template: "base", Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{foreign.ID, "sbx_missing"} {
		if _, err = s.Get(ctx, p, id); !errors.Is(err, ErrNotFound) {
			t.Fatal("get leaked existence", err)
		}
		if err = s.Destroy(ctx, p, id, "requested"); !errors.Is(err, ErrNotFound) {
			t.Fatal("destroy leaked existence", err)
		}
		if _, err = s.Activity(ctx, p, id); !errors.Is(err, ErrNotFound) {
			t.Fatal("keepalive leaked existence", err)
		}
	}
	rows, err := s.List(ctx, p, ListRequest{Limit: 1, Label: "project.name:quoted\"value:tail", State: "running"})
	if err != nil || len(rows) != 1 || rows[0].ID != own.ID {
		t.Fatal("label or ownership filter failed", err)
	}
	rows, err = s.List(ctx, p, ListRequest{Limit: 1, Cursor: own.ID})
	if err != nil || len(rows) != 0 {
		t.Fatal("cursor failed", err)
	}
	rows, err = s.List(ctx, auth.Principal{ID: p.ID, Admin: true}, ListRequest{Limit: 100})
	if err != nil || len(rows) != 2 {
		t.Fatal("admin visibility", err)
	}
	rows, err = s.List(ctx, p, ListRequest{Limit: 100, Label: "project.name:absent"})
	if err != nil || len(rows) != 0 {
		t.Fatal("label ignored", err)
	}
	for _, request := range []ListRequest{{Limit: 0}, {Limit: 1001}, {Limit: 1, State: "invented"}, {Limit: 1, Label: "invalid"}, {Limit: 1, Cursor: strings.Repeat("x", 129)}} {
		if _, err = s.List(ctx, p, request); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid page accepted", err)
		}
	}
}

func TestLifecycle_QuotaReservationIsAtomic(t *testing.T) {
	s, _, p, other, _ := fixture(t)
	ctx := context.Background()
	s.cfg.Limits.MaxSandboxes = 3
	s.cfg.Limits.MaxSandboxesPerKey = 2
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() { _, err := s.Create(ctx, p, CreateRequest{Template: "base"}); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrQuota) {
			t.Fatal(err)
		}
	}
	if successes != 2 {
		t.Fatalf("quota admitted %d", successes)
	}
	if _, err := s.Create(ctx, other, CreateRequest{Template: "base"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, other, CreateRequest{Template: "base"}); !errors.Is(err, ErrQuota) {
		t.Fatal("global quota", err)
	}
}

func TestLifecycle_FailuresPersistAndCanBeRetried(t *testing.T) {
	s, driver, p, _, clock := fixture(t)
	ctx := context.Background()
	driver.SetFailures(fake.Failures{Create: errors.New("injected")})
	if _, err := s.Create(ctx, p, CreateRequest{Template: "base"}); !errors.Is(err, ErrRuntime) {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, p, ListRequest{Limit: 100})
	if err != nil || len(rows) != 1 || rows[0].State != "failed" || rows[0].EndReason.String != "create_failed" || !rows[0].EndedAt.Valid {
		t.Fatal("failed reservation lost", err)
	}
	driver.SetFailures(fake.Failures{})
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	driver.SetFailures(fake.Failures{Destroy: errors.New("injected")})
	if err = s.Destroy(ctx, p, row.ID, "requested"); !errors.Is(err, ErrRuntime) {
		t.Fatal(err)
	}
	pending, err := s.Get(ctx, p, row.ID)
	if err != nil || pending.State != "destroying" {
		t.Fatal("destroy failure lost reservation", err)
	}
	driver.SetFailures(fake.Failures{})
	if err = s.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	row, err = s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	clock.at = row.ExpiresAt
	if _, err = s.Activity(ctx, p, row.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("keepalive resurrected expired TTL")
	}
	if err = s.Destroy(ctx, p, row.ID, "ttl"); err != nil {
		t.Fatal(err)
	}
	row, err = s.Get(ctx, p, row.ID)
	if err != nil || row.State != "expired" {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Create(cancelled, p, CreateRequest{Template: "base"}); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestResolve_RejectsInvalidOverrides(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	zero := int64(0)
	negative := int64(-1)
	nan := math.NaN()
	requests := []CreateRequest{{Template: "absent"}, {Template: "base", TTLSeconds: &zero}, {Template: "base", IdleTimeoutSeconds: &negative}, {Template: "base", Resources: Overrides{CPUs: &nan}}, {Template: "base", Resources: Overrides{MemoryMB: &zero}}, {Template: "base", NetworkMode: "open"}, {Template: "base", Labels: map[string]string{"arcana.managed": "true"}}, {Template: "base", Env: map[string]string{"BAD=KEY": "value"}}, {Template: "base", Labels: map[string]string{"key": strings.Repeat("x", 1025)}}, {Template: "base", Env: map[string]string{"key": "bad\x00"}}}
	for i, request := range requests {
		if _, err := resolve(s.cfg, request); err == nil {
			t.Fatalf("accepted invalid input %d", i)
		}
	}
	plan, err := resolve(s.cfg, CreateRequest{Template: "base", IdleTimeoutSeconds: &zero})
	if err != nil || plan.idle != 0 || plan.ttl != 120 || plan.spec.Resources.CPUs != 2 {
		t.Fatal("template defaults", err)
	}
	s.cfg.Templates[0].Workdir = "/etc"
	if _, err = resolve(s.cfg, CreateRequest{Template: "base"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("workdir escaped roots")
	}
}
