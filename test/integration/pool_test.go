//go:build linux && integration

package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestPool_AcquireFasterThanCold(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: database.InstallID, HelperPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.Default()
	cfg.Templates[0].WarmPool = 1
	cfg.Server.DestroyOnShutdown = true
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(database, auth.SystemClock{})
	owner, _, err := keys.Create(ctx, "pool-test", false)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: owner.ID}
	start := time.Now()
	cold, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base"})
	coldTime := time.Since(start)
	if err != nil || cold.Source != "cold" {
		t.Fatalf("cold: %v %s", err, cold.Source)
	}
	if err = service.FillPool(ctx); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	warm, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base"})
	warmTime := time.Since(start)
	if err != nil || warm.Source != "pool" {
		t.Fatalf("warm: %v %s", err, warm.Source)
	}
	t.Logf("cold=%s pool=%s", coldTime, warmTime)
	if warmTime >= coldTime {
		t.Fatal("pool acquire did not beat cold create")
	}
	cmd := []string{"/bin/true"}
	result, err := service.Exec(ctx, principal, warm.ID, orchestrator.ExecRequest{Cmd: &cmd})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("pooled sandbox exec: %+v %v", result, err)
	}
}
