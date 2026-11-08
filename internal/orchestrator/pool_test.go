package orchestrator

import (
	"context"
	"sync"
	"testing"
)

func TestPool_AcquireOnceAndColdFallback(t *testing.T) {
	s, _, owner, _, _ := fixture(t)
	s.cfg.Templates[0].WarmPool = 1
	ctx := context.Background()
	if err := s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	if s.PoolAvailable()["base"] != 1 {
		t.Fatal("pool not filled")
	}
	row, err := s.Create(ctx, owner, CreateRequest{Template: "base"})
	if err != nil || row.Source != "pool" {
		t.Fatalf("%+v %v", row, err)
	}
	if s.PoolAvailable()["base"] != 0 {
		t.Fatal("member reused")
	}
	if err := s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.Create(ctx, owner, CreateRequest{Template: "base", Env: map[string]string{"CUSTOM": "value"}})
	if err != nil || row.Source != "cold" || s.PoolAvailable()["base"] != 1 {
		t.Fatalf("%+v %v", row, err)
	}
	s.cfg.Server.DestroyOnShutdown = true
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if s.PoolAvailable()["base"] != 0 {
		t.Fatal("pool not drained")
	}
}

func TestPool_ConcurrentFillerAndAcquire(t *testing.T) {
	s, _, owner, _, _ := fixture(t)
	s.cfg.Templates[0].WarmPool = 2
	s.cfg.Limits.MaxSandboxesPerKey = 20
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.FillPool(ctx); err != nil {
				t.Error(err)
			}
			row, err := s.Create(ctx, owner, CreateRequest{Template: "base"})
			if err != nil {
				t.Error(err)
				return
			}
			if err = s.Destroy(ctx, owner, row.ID, "test"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if s.PoolAvailable()["base"] > 2 {
		t.Fatal("pool exceeded target")
	}
}

func TestPool_QuotaAndReconcile(t *testing.T) {
	s, _, owner, _, _ := fixture(t)
	ctx := context.Background()
	s.cfg.Templates[0].WarmPool = 1
	s.cfg.Limits.MaxSandboxes = 1
	if err := s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if s.PoolAvailable()["base"] != 1 {
		t.Fatal("default shutdown destroyed pool")
	}
	row, err := s.Create(ctx, owner, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	if s.PoolAvailable()["base"] != 0 {
		t.Fatal("pool filled beyond capacity")
	}
	if err = s.Destroy(ctx, owner, row.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err = s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	// A fresh service has no in-memory members: startup reconciliation removes them.
	restarted := New(s.store, s.driver, s.cfg, s.clock)
	if err = restarted.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	instances, err := s.driver.List(ctx)
	if err != nil || len(instances) != 0 {
		t.Fatalf("orphan pool: %+v %v", instances, err)
	}
}

func TestPool_ColdFallbackEvictsUnusedCapacity(t *testing.T) {
	s, _, owner, _, _ := fixture(t)
	s.cfg.Limits.MaxSandboxes = 1
	s.cfg.Templates[0].WarmPool = 1
	ctx := context.Background()
	if err := s.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := s.Create(ctx, owner, CreateRequest{Template: "base", Labels: map[string]string{"task": "cold"}})
	if err != nil || row.Source != "cold" {
		t.Fatalf("%+v %v", row, err)
	}
	instances, err := s.driver.List(ctx)
	if err != nil || len(instances) != 1 || s.PoolAvailable()["base"] != 0 {
		t.Fatalf("capacity: %+v %v", instances, err)
	}
}
