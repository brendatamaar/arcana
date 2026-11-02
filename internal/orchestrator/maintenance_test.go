package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store/db"
)

func TestMaintenance_TTLPrecedesIdle(t *testing.T) {
	s, _, p, _, clock := fixture(t)
	ctx := context.Background()
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	clock.at = row.ExpiresAt
	if err = s.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p, row.ID)
	if err != nil || got.State != "expired" || got.EndReason.String != "ttl" {
		t.Fatal(got, err)
	}
}

func TestReconcile_CleanupFailureRetainsRecoverableRecord(t *testing.T) {
	s, driver, p, _, _ := fixture(t)
	ctx := context.Background()
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.transition(ctx, row, DestroyRequested, "requested", ""); err != nil {
		t.Fatal(err)
	}
	driver.SetFailures(fake.Failures{Destroy: errors.New("daemon unavailable")})
	if err = s.Reconcile(ctx); err == nil {
		t.Fatal("cleanup error hidden")
	}
	got, err := s.Get(ctx, p, row.ID)
	if err != nil || got.State != "destroying" {
		t.Fatal(got, err)
	}
	driver.SetFailures(fake.Failures{})
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExpiry_DecisionsAtBoundaries(t *testing.T) {
	now := time.Unix(100000, 0)
	for _, test := range []struct {
		phase     Phase
		ttl, idle time.Duration
		seconds   int64
		want      string
	}{
		{Running, time.Second, 0, 1, ""},
		{Running, time.Second, -time.Second, 1, "idle"},
		{Running, time.Second, -time.Hour, 0, ""},
		{Running, 0, -time.Hour, 1, "ttl"},
		{Creating, -time.Second, -time.Hour, 1, ""},
		{Destroyed, -time.Second, -time.Hour, 1, ""},
	} {
		if got := expiryReason(now, expirySnapshot{test.phase, now.Add(test.ttl), now.Add(test.idle), test.seconds}); got != test.want {
			t.Fatalf("%+v: %s", test, got)
		}
	}
}

func TestMaintain_ContextOwnsLoopAndJoins(t *testing.T) {
	s, _, _, _, _, clock := execFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Maintain(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	timer := <-clock.timers
	if timer.after != time.Second && timer.after != time.Duration(s.cfg.Workspace.CheckIntervalSeconds)*time.Second {
		t.Fatal(timer.after)
	}
	cancel()
	<-done
}

func TestWatch_SupervisedScanPreservesIdleAndEnforcesQuota(t *testing.T) {
	for _, usage := range []string{"1\t/workspace\n", "999999999999\t/workspace\n"} {
		t.Run(usage, func(t *testing.T) {
			s, driver, p, _, row, clock := execFixture(t)
			clock.mu.Lock()
			clock.at = clock.at.Add(time.Second)
			clock.mu.Unlock()
			driver.script = fake.Script{Stdout: []byte(usage)}
			if err := s.Watch(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(context.Background(), p, row.ID)
			if err != nil || !got.LastActivityAt.Equal(row.LastActivityAt) {
				t.Fatal("watch changed idle clock", err)
			}
			if usage[0] == '9' && (got.State != "destroyed" || got.EndReason.String != "disk_quota") {
				t.Fatal(got)
			}
			if usage[0] == '1' && got.State != "running" {
				t.Fatal(got)
			}
		})
	}
}

func TestShutdown_PreservesByDefaultAndDestroysWhenEnabled(t *testing.T) {
	s, _, p, _, _ := fixture(t)
	ctx := context.Background()
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p, row.ID)
	if err != nil || got.State != "running" {
		t.Fatal(got, err)
	}
	s.cfg.Server.DestroyOnShutdown = true
	if err = s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, p, row.ID)
	if err != nil || got.State != "destroyed" || got.EndReason.String != "shutdown" {
		t.Fatal(got, err)
	}
}

func TestReconcile_PreservesHealthyAndRemovesInterrupted(t *testing.T) {
	s, driver, p, _, clock := fixture(t)
	ctx := context.Background()
	healthy, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	const id = "exe_aaaaaaaaaaaaaaaaaaaa"
	_, err = s.store.Queries.CreateExec(ctx, db.CreateExecParams{ID: id, SandboxID: interrupted.ID, Command: "[]", StartedAt: clock.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p, healthy.ID)
	if err != nil || got.State != "running" || !got.LastActivityAt.Equal(healthy.LastActivityAt) {
		t.Fatal(got, err)
	}
	got, err = s.Get(ctx, p, interrupted.ID)
	if err != nil || got.State != "failed" || got.EndReason.String != "lost" {
		t.Fatal(got, err)
	}
	exec, err := s.store.Queries.GetExec(ctx, id)
	if err != nil || !exec.EndedAt.Valid || exec.ExitCode.Valid {
		t.Fatal(exec, err)
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 1 {
		t.Fatal(instances, err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal("not idempotent", err)
	}
}

func TestMaintenance_ScanAdmissionDoesNotTouchIdleActivity(t *testing.T) {
	s, _, p, _, clock := fixture(t)
	ctx := context.Background()
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	clock.at = clock.at.Add(time.Second)
	got, err := s.execActivity(ctx, p, row.ID, true)
	if err != nil || !got.LastActivityAt.Equal(row.LastActivityAt) {
		t.Fatal(got, err)
	}
}

func TestReconcile_ResumesDestroyAfterContainerDisappears(t *testing.T) {
	s, driver, p, _, _ := fixture(t)
	ctx := context.Background()
	row, err := s.Create(ctx, p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	row, err = s.transition(ctx, row, DestroyRequested, "ttl", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.Destroy(ctx, row.ContainerID.String); err != nil {
		t.Fatal(err)
	}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p, row.ID)
	if err != nil || got.State != "expired" || got.EndReason.String != "ttl" {
		t.Fatal(got, err)
	}
}

func FuzzExpiry_TTLAlwaysWins(f *testing.F) {
	f.Add(int64(0), int64(1))
	f.Fuzz(func(t *testing.T, offset, idle int64) {
		// Bounded timestamps avoid duration overflow unrelated to configured limits.
		now := time.Unix(100000, 0)
		offset = offset % 86400
		if offset < 0 {
			offset = -offset
		}
		expires := now.Add(-time.Duration(offset%86400) * time.Second)
		reason := expiryReason(now, expirySnapshot{Running, expires, now.Add(-24 * time.Hour), idle % 86400})
		if reason != "ttl" {
			t.Fatal(reason)
		}
	})
}
