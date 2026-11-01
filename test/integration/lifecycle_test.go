//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
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

func TestLifecycle_RealRuntimePersistenceOwnershipAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .Runtimes}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	var available map[string]json.RawMessage
	if err = json.Unmarshal(output, &available); err != nil {
		t.Fatal(err)
	}
	oci := "runsc"
	if _, ok := available[oci]; !ok {
		oci = "runc"
		t.Log("runsc unavailable; explicitly opting into runc, which does not prove the security boundary")
	}
	t.Log("lifecycle runtime:", oci)
	build := exec.CommandContext(ctx, "docker", "build", "-t", "arcana-base:local", "../../templates/base")
	if output, err = build.CombinedOutput(); err != nil {
		t.Fatalf("building base: %v: %s", err, output)
	}
	dataDir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dataDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	helperPath, err := helper.Install(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: oci, AllowInsecureRuntime: oci == "runc", InstallID: database.InstallID, HelperPath: helperPath})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(database, auth.SystemClock{})
	owner, _, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := keys.Create(ctx, "other", false)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: owner.ID}
	cfg := config.Default()
	cfg.Runtime.Docker.OCIRuntime = oci
	cfg.Limits.Defaults.MemoryMB = 128
	cfg.Limits.Defaults.DiskMB = 16
	cfg.Limits.Defaults.CPUs = .25
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	row, err := service.Create(ctx, p, orchestrator.CreateRequest{Template: "base", Labels: map[string]string{"project": "live"}})
	if err != nil {
		t.Fatal(err)
	}
	// The cleanup context outlives the test deadline and targets only this instance.
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := driver.Destroy(cleanup, row.ContainerID.String); err != nil {
			t.Error(err)
		}
	}()
	if row.State != "running" {
		t.Fatal(row.State)
	}
	if _, err = service.Get(ctx, auth.Principal{ID: other.ID}, row.ID); !errors.Is(err, orchestrator.ErrNotFound) {
		t.Fatal("ownership", err)
	}
	active, err := service.Activity(ctx, p, row.ID)
	if err != nil || !active.ExpiresAt.Equal(row.ExpiresAt) {
		t.Fatal("keepalive", err)
	}
	if err = service.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	if err = service.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal("idempotence", err)
	}
	ended, err := service.Get(ctx, p, row.ID)
	if err != nil || ended.State != "destroyed" || !ended.EndedAt.Valid {
		t.Fatal("persistence", err)
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 0 {
		t.Fatal("managed resource leak", err)
	}
	if _, err = exec.CommandContext(ctx, "docker", "info").Output(); err != nil {
		t.Fatal("Docker unhealthy", err)
	}
}
