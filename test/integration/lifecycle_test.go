//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
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
	owner, secret, err := keys.Create(ctx, "owner", false)
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
	args := []string{"/usr/bin/printf", "argv ; stays data"}
	result, err := service.Exec(ctx, p, row.ID, orchestrator.ExecRequest{Cmd: &args})
	if err != nil || result.ExitCode != 0 || result.Stdout != "argv ; stays data" {
		t.Fatal("argv exec", err, result.ExitCode)
	}
	shell := "printf 'stdout'; printf 'stderr' >&2; exit 7"
	result, err = service.Exec(ctx, p, row.ID, orchestrator.ExecRequest{Shell: &shell})
	if err != nil || result.ExitCode != 7 || result.Stdout != "stdout" || result.Stderr != "stderr" {
		t.Fatal("shell exec", err, result.ExitCode)
	}
	args = []string{"/definitely-not-a-command"}
	result, err = service.Exec(ctx, p, row.ID, orchestrator.ExecRequest{Cmd: &args})
	if err != nil || result.ExitCode != 127 {
		t.Fatal("ordinary missing executable destroyed sandbox", err, result.ExitCode)
	}
	shell = "trap '' TERM; sleep 300 & wait"
	seconds := int64(1)
	result, err = service.Exec(ctx, p, row.ID, orchestrator.ExecRequest{Shell: &shell, TimeoutSeconds: &seconds})
	if err != nil || result.ExitCode != 137 || !result.TimedOut {
		t.Fatal("timeout", err, result.ExitCode)
	}
	current, err := service.Get(ctx, p, row.ID)
	if err != nil || current.State != "running" {
		t.Fatal("normal timeout lost sandbox", err)
	}
	peer, err := service.Create(ctx, p, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := driver.Destroy(cleanup, peer.ContainerID.String); err != nil {
			t.Error(err)
		}
	}()
	cancelCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	shell = "trap '' TERM; printf ready > /tmp/exec-cancel-ready; sleep 300 & wait"
	type completion struct {
		result orchestrator.ExecResult
		err    error
	}
	completed := make(chan completion, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, err := service.Exec(cancelCtx, p, row.ID, orchestrator.ExecRequest{Shell: &shell})
		completed <- completion{result, err}
	}()
	defer func() { cancelExec(); <-finished }()
	readyDeadline := time.NewTimer(10 * time.Second)
	defer readyDeadline.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		probe, err := driver.Exec(ctx, row.ContainerID.String, runtime.ExecRequest{Cmd: []string{"/usr/bin/test", "-e", "/tmp/exec-cancel-ready"}, Cwd: "/workspace"})
		if err != nil {
			t.Fatal(err)
		}
		if err = probe.Stdin().Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, probe.Stdout()); err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, probe.Stderr()); err != nil {
			t.Fatal(err)
		}
		exit, err := probe.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if exit.ExitCode == 0 {
			break
		}
		select {
		case <-readyDeadline.C:
			t.Fatal("exec never became ready for cancellation")
		case <-poll.C:
		}
	}
	cancelExec()
	completionResult := <-completed
	if !errors.Is(completionResult.err, context.Canceled) || completionResult.result.ExitCode != 137 || !completionResult.result.Cancelled {
		t.Fatal("cancellation did not retain proven child status", completionResult.err, completionResult.result.ExitCode)
	}
	current, err = service.Get(ctx, p, row.ID)
	if err != nil || current.State != "running" {
		t.Fatal("cancellation lost sandbox", err)
	}
	docker(t, ctx, "exec", peer.ContainerID.String, "/usr/bin/true")
	assertStreamEngine(t, ctx, service, p, row.ID)
	assertLiveStream(t, ctx, api.New(keys, slog.New(slog.NewJSONHandler(io.Discard, nil)), cfg.Limits.Files, service), row.ID, secret)
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
	if err = service.Destroy(ctx, p, peer.ID, "requested"); err != nil {
		t.Fatal("peer cleanup", err)
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 0 {
		t.Fatal("managed resource leak", err)
	}
	if _, err = exec.CommandContext(ctx, "docker", "info").Output(); err != nil {
		t.Fatal("Docker unhealthy", err)
	}
}
