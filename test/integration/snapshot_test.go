//go:build linux && integration

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
)

// wheelBuilder writes a minimal pure-Python wheel so pip installs a real package offline.
const wheelBuilder = `
import zipfile
with zipfile.ZipFile('/workspace/arcana_demo-1.0-py3-none-any.whl', 'w') as z:
    z.writestr('arcana_demo/__init__.py', 'VALUE = "from-snapshot"\n')
    z.writestr('arcana_demo-1.0.dist-info/METADATA', 'Metadata-Version: 2.1\nName: arcana-demo\nVersion: 1.0\n')
    z.writestr('arcana_demo-1.0.dist-info/WHEEL', 'Wheel-Version: 1.0\nGenerator: test\nRoot-Is-Purelib: true\nTag: py3-none-any\n')
    z.writestr('arcana_demo-1.0.dist-info/RECORD', '')
`

func TestSnapshot_ForkRestoresInstalledPackageAndIsolatesChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "snapshot.db"))
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
	cfg.DataDir = dir
	cfg.Templates[0].Image = "arcana-python:local"
	cfg.Server.DestroyOnShutdown = true
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(database, auth.SystemClock{})
	owner, _, err := keys.Create(ctx, "snapshot-test", false)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{ID: owner.ID}
	run := func(id string, argv ...string) string {
		t.Helper()
		result, execErr := service.Exec(ctx, p, id, orchestrator.ExecRequest{Cmd: &argv})
		if execErr != nil || result.ExitCode != 0 {
			t.Fatalf("%v: exit=%d err=%v stderr=%q", argv, result.ExitCode, execErr, result.Stderr)
		}
		return strings.TrimSpace(result.Stdout)
	}
	source, err := service.Create(ctx, p, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	// Install a package into a /workspace virtualenv, with no network.
	run(source.ID, "python", "-m", "venv", "/workspace/.venv")
	run(source.ID, "python", "-c", wheelBuilder)
	run(source.ID, "/workspace/.venv/bin/python", "-m", "pip", "install", "--no-index", "--no-deps", "--disable-pip-version-check", "/workspace/arcana_demo-1.0-py3-none-any.whl")
	check := []string{"/workspace/.venv/bin/python", "-c", "import arcana_demo; print(arcana_demo.VALUE)"}
	if got := run(source.ID, check...); got != "from-snapshot" {
		t.Fatal("package not installed in source", got)
	}

	snap, err := service.CreateSnapshot(ctx, p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "snapshots", snap.Path))
	if err != nil || snap.SizeBytes != info.Size() || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatal("stored archive", snap.SizeBytes, info, err)
	}

	result, err := service.Fork(ctx, p, snap.ID, 3, orchestrator.CreateRequest{})
	if err != nil || len(result.Items) != 3 || len(result.Failures) != 0 {
		t.Fatalf("fork: %+v %v", result, err)
	}
	for _, row := range result.Items {
		if row.Source != "snapshot" || row.State != "running" || row.FromSnapshotID.String != snap.ID {
			t.Fatalf("fork member: %+v", row)
		}
		if got := run(row.ID, check...); got != "from-snapshot" {
			t.Fatalf("package missing in %s: %q", row.ID, got)
		}
		// The virtualenv's absolute symlink and executable bits survive the tar round trip.
		run(row.ID, "test", "-L", "/workspace/.venv/bin/python")
		run(row.ID, "test", "-x", "/workspace/.venv/bin/pip")
	}

	// A change in one fork is invisible to its siblings, the source and later restores.
	first, others := result.Items[0], result.Items[1:]
	run(first.ID, "sh", "-c", "echo changed > /workspace/only-in-first && echo 'VALUE = \"mutated\"' > /workspace/.venv/lib/python3.13/site-packages/arcana_demo/__init__.py")
	if got := run(first.ID, check...); got != "mutated" {
		t.Fatal("mutation did not apply", got)
	}
	for _, id := range []string{source.ID, others[0].ID, others[1].ID} {
		run(id, "test", "!", "-e", "/workspace/only-in-first")
		if got := run(id, check...); got != "from-snapshot" {
			t.Fatalf("change leaked into %s: %q", id, got)
		}
	}
	again, err := service.Create(ctx, p, orchestrator.CreateRequest{Template: "base", FromSnapshot: snap.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := run(again.ID, check...); got != "from-snapshot" {
		t.Fatal("snapshot was modified by a fork", got)
	}

	// Members created in parallel are separate runtime instances.
	distinct := map[string]bool{source.ContainerID.String: true}
	for _, row := range result.Items {
		distinct[row.ContainerID.String] = true
	}
	if len(distinct) != 4 {
		t.Fatal("forks share a runtime instance")
	}

	// A corrupted archive fails verification and leaves no sandbox behind.
	before, err := driver.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "snapshots", snap.Path)
	if err = os.WriteFile(file, []byte("not the archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Create(ctx, p, orchestrator.CreateRequest{Template: "base", FromSnapshot: snap.ID}); !errors.Is(err, orchestrator.ErrSnapshotCorrupt) {
		t.Fatal("corrupt archive restored", err)
	}
	after, err := driver.List(ctx)
	if err != nil || len(after) != len(before) {
		t.Fatalf("failed restore left instances: %d -> %d (%v)", len(before), len(after), err)
	}

	if err = service.DeleteSnapshot(ctx, p, snap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive survived deletion", err)
	}
}
