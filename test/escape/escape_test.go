//go:build linux && escape

package escape

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

type fixture struct {
	api, other   *client.Client
	target, peer string
	ctx          context.Context
	base, key    string
}

func newFixture(t *testing.T, image, backend string, memory ...int64) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: database.InstallID, HelperPath: path})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Templates[0].Image = image
	cfg.Workspace.Backend = backend
	cfg.Workspace.CheckIntervalSeconds = 1
	cfg.Limits.Defaults.MemoryMB = 128
	if len(memory) > 0 {
		cfg.Limits.Defaults.MemoryMB = memory[0]
	}
	cfg.Limits.Defaults.DiskMB = 4
	cfg.Limits.Defaults.PIDs = 64
	cfg.Limits.Defaults.CPUs = .25
	cfg.Limits.Exec.MaxOutputBytes = 4096
	cfg.Limits.Exec.DefaultTimeoutSeconds = 10
	keys := auth.New(database, auth.SystemClock{})
	_, key, err := keys.Create(ctx, "attacker", false)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keys.Create(ctx, "other", false)
	if err != nil {
		t.Fatal(err)
	}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	owned, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.Maintain(owned, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service))
	t.Cleanup(func() {
		server.Close()
		stop()
		<-done
		cleanup, finish := context.WithTimeout(context.Background(), 20*time.Second)
		defer finish()
		instances, err := driver.List(cleanup)
		if err != nil {
			t.Error(err)
		}
		for _, instance := range instances {
			if err = driver.Destroy(cleanup, instance.ID); err != nil {
				t.Error(err)
			}
		}
		_ = driver.Close()
		_ = database.Close()
		cancel()
	})
	c, err := client.New(server.URL, key, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := client.New(server.URL, other, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{c, foreign, first.Id, peer.Id, ctx, server.URL, key}
	t.Cleanup(func() {
		healthCtx, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		if _, err := c.Health(healthCtx); err != nil {
			t.Error("Arcana unhealthy", err)
		}
		result, err := c.Exec(healthCtx, peer.Id, client.ExecRequest{Cmd: []string{"/bin/sleep", "0"}})
		if err != nil || result.ExitCode != 0 {
			t.Error("peer sandbox unhealthy", err)
		}
		docker(healthCtx, t, "info", "--format", "{{.ServerVersion}}")
	})
	return f
}

func (f *fixture) run(t *testing.T, args ...string) client.ExecResult {
	t.Helper()
	result, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Cmd: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f *fixture) probe(t *testing.T, name string) client.ExecResult {
	return f.run(t, "/usr/local/bin/escape-probe", name)
}

func TestEscapeV1_PublicAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireRunsc(ctx, t)
	docker(ctx, t, "build", "-t", "arcana-base:local", "../../templates/base")
	dir := t.TempDir()
	binary := filepath.Join(dir, "escape-probe")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./probe") // #nosec G204 -- fixed compiler/argv; output is a test-owned temporary path.
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("probe build: %v: %s", err, out)
	}
	dockerfile := []byte("FROM arcana-base:local\nUSER root\nCOPY escape-probe /usr/local/bin/escape-probe\nRUN [\"chmod\", \"4755\", \"/usr/local/bin/escape-probe\"]\nUSER 1000:1000\n")
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), dockerfile, 0o600); err != nil {
		t.Fatal(err)
	}
	image := "arcana-escape:" + fmt.Sprint(os.Getpid())
	docker(ctx, t, "build", "-t", image, dir)
	t.Cleanup(func() {
		cleanup, finish := context.WithTimeout(context.Background(), 15*time.Second)
		defer finish()
		docker(cleanup, t, "image", "rm", image)
	})
	for _, test := range []struct{ id, probe string }{{"ESC01", "fork"}, {"ESC05", "readonly"}, {"ESC10", "raw"}, {"ESC11", "setuid"}, {"ESC12", "privileged"}, {"ESC13", "socket"}, {"ESC14", "network"}} {
		t.Run(test.id, func(t *testing.T) {
			memory := int64(128)
			if test.id == "ESC01" {
				memory = 512
			}
			f := newFixture(t, image, "tmpfs", memory)
			if test.id == "ESC01" {
				stop := monitorPids(t, f)
				defer stop()
				result, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Cmd: []string{"/usr/local/bin/escape-probe", "fork"}})
				pids, oom := stop()
				t.Log("fork proof:", result.Stdout, "PID denials", pids, "OOM kills", oom, "error", err)
				if pids == 0 || oom != 0 {
					t.Fatal("fork did not hit PID cgroup limit without OOM")
				}
				if err != nil {
					var failure *client.Error
					row, getErr := f.api.GetSandbox(f.ctx, f.target)
					if !errors.As(err, &failure) || failure.Code != "runtime_unavailable" || getErr != nil || row.State != "destroyed" || row.EndReason == nil || *row.EndReason != "exec_uncertain" {
						t.Fatal("fork cleanup was not fail-closed", row, err, getErr)
					}
				} else if result.ExitCode != 0 || !strings.Contains(result.Stdout, "fork-blocked") {
					t.Fatal("fork not contained", result)
				}
				return
			}
			result := f.probe(t, test.probe)
			if result.ExitCode != 0 || result.TimedOut || result.Stdout == "" {
				t.Fatal(test.id, "attack was not contained", result)
			}
		})
	}
	t.Run("ESC02", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		containerID := docker(f.ctx, t, "inspect", "--format", "{{.Id}}", "sbx-"+f.target)
		since := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Cmd: []string{"/usr/local/bin/escape-probe", "memory"}})
		events := docker(f.ctx, t, "events", "--since", since, "--until", time.Now().UTC().Format(time.RFC3339Nano), "--filter", "container="+containerID, "--filter", "event=oom", "--format", "{{.Action}}")
		oom := strings.Contains(events, "oom") || strings.Contains(strings.ToLower(result.Stderr), "out of memory")
		if !oom || err == nil && (result.ExitCode == 0 || result.TimedOut) {
			t.Fatal("memory bomb lacked OOM containment proof", err, result, events)
		}
	})
	t.Run("ESC03", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		assertCPU := monitorCPU(t, f)
		stream, err := f.api.ExecStream(f.ctx, f.target, client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"/usr/local/bin/escape-probe", "cpu"}}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stream.Close() }()
		stderrDone := make(chan error, 1)
		go func() { _, err := io.Copy(io.Discard, stream.Stderr); stderrDone <- err }()
		reader := bufio.NewReader(stream.Stdout)
		line, err := reader.ReadString('\n')
		if err != nil || line != "cpu-started\n" {
			t.Fatal("CPU attack did not start", line, err)
		}
		start := time.Now()
		peer, err := f.api.Exec(f.ctx, f.peer, client.ExecRequest{Cmd: []string{"/bin/sleep", "0"}})
		if err != nil || peer.ExitCode != 0 || time.Since(start) > 2*time.Second {
			t.Fatal("peer latency exceeded bound", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, 4096))
		stderrErr := <-stderrDone
		result, waitErr := stream.Wait()
		var timing struct {
			CPU  int64 `json:"cpu_ms"`
			Wall int64 `json:"wall_ms"`
		}
		if readErr != nil || stderrErr != nil || waitErr != nil || result.ExitCode != 0 || json.Unmarshal(data, &timing) != nil || timing.CPU <= 0 || timing.Wall < 2500 {
			t.Fatal("CPU quota was not observed", result, timing)
		}
		assertCPU()
		t.Logf("guest CPU diagnostic=%d ms; wall=%d ms", timing.CPU, timing.Wall)
	})
	t.Run("ESC04_tmpfs", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		r := f.probe(t, "fill")
		if r.ExitCode != 0 || !strings.Contains(r.Stdout, "enospc") {
			t.Fatal("tmpfs limit not enforced", r)
		}
	})
	t.Run("ESC04_volume", func(t *testing.T) {
		f := newFixture(t, image, "volume")
		_, _ = f.api.Exec(f.ctx, f.target, client.ExecRequest{Cmd: []string{"/usr/local/bin/escape-probe", "volume"}})
		row, err := f.api.GetSandbox(f.ctx, f.target)
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for err == nil && row.State == "destroying" {
			select {
			case <-deadline.C:
				t.Fatal("disk quota cleanup did not complete")
			case <-ticker.C:
				row, err = f.api.GetSandbox(f.ctx, f.target)
			}
		}
		if err != nil || row.State != "destroyed" || row.EndReason == nil || *row.EndReason != "disk_quota" {
			t.Fatal("volume quota not enforced", row, err)
		}
	})
	t.Run("ESC06", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		for _, path := range []string{"%2Fworkspace%2F%2e%2e%2Fetc%2Fpasswd", "/workspace/.%2e/etc/passwd", "/workspace/a%00b"} {
			req, err := http.NewRequestWithContext(f.ctx, "GET", f.base+"/v1/sandboxes/"+f.target+"/files?path="+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.key)
			resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err != nil || resp.StatusCode != 400 || json.Unmarshal(data, &envelope) != nil || envelope.Error.Code != "invalid_path" {
				t.Fatal("encoded traversal accepted", resp.StatusCode)
			}
		}
		for _, path := range []string{"/workspace/../etc/passwd", "/etc/passwd", "/workspace/a\x00b"} {
			body, err := f.api.ReadFile(f.ctx, f.target, path)
			if body != nil {
				_ = body.Close()
			}
			var failure *client.Error
			if !errors.As(err, &failure) || failure.Code != "invalid_path" || failure.StatusCode != 400 {
				t.Fatal("traversal accepted", path, err)
			}
		}
	})
	t.Run("ESC07", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		hostFile := filepath.Join(t.TempDir(), "host-secret")
		secret := []byte("host-sentinel-" + f.target)
		if err := os.WriteFile(hostFile, secret, 0o600); err != nil {
			t.Fatal(err)
		}
		if r := f.run(t, "/bin/ln", "-s", hostFile, "/workspace/host-link"); r.ExitCode != 0 {
			t.Fatal(r)
		}
		body, err := f.api.ReadFile(f.ctx, f.target, "/workspace/host-link")
		if body != nil {
			data, _ := io.ReadAll(body)
			_ = body.Close()
			if bytes.Contains(data, secret) {
				t.Fatal("host file returned")
			}
		}
		var failure *client.Error
		if !errors.As(err, &failure) || failure.Code != "file_not_found" {
			t.Fatal("host path resolved unexpectedly", err)
		}
		actual, err := os.ReadFile(hostFile) // #nosec G304 -- read this test-owned host sentinel, never a sandbox volume path.
		if err != nil || !bytes.Equal(actual, secret) {
			t.Fatal("host sentinel changed", err)
		}
		if r := f.run(t, "/bin/ln", "-s", "/etc/passwd", "/workspace/passwd-link"); r.ExitCode != 0 {
			t.Fatal(r)
		}
		expected := f.run(t, "/bin/cat", "/etc/passwd")
		body, err = f.api.ReadFile(f.ctx, f.target, "/workspace/passwd-link")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		_ = body.Close()
		if err != nil || string(data) != expected.Stdout {
			t.Fatal("symlink did not resolve sandbox file", err)
		}
	})
	t.Run("ESC08", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		shell := `trap '' TERM; echo $$ > /workspace/parent; sleep 60 & echo $! > /workspace/child; wait`
		timeout := int64(1)
		result, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Shell: &shell, TimeoutSeconds: &timeout})
		if err != nil || !result.TimedOut || result.ExitCode != 137 {
			t.Fatal("timeout tree cleanup failed", result, err)
		}
		check := `for f in /workspace/parent /workspace/child; do read pid < "$f" || exit 2; case "$pid" in ''|*[!0-9]*) exit 3;; esac; test ! -e /proc/"$pid" || exit 1; done`
		r, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Shell: &check})
		if err != nil || r.ExitCode != 0 {
			t.Fatal("descendant survived timeout", r, err)
		}
	})
	t.Run("ESC09", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		var before, after goruntime.MemStats
		goruntime.GC()
		goruntime.ReadMemStats(&before)
		timeout := int64(1)
		result, err := f.api.Exec(f.ctx, f.target, client.ExecRequest{Cmd: []string{"/usr/bin/yes"}, TimeoutSeconds: &timeout})
		goruntime.GC()
		goruntime.ReadMemStats(&after)
		if err != nil || !result.Truncated || !result.TimedOut || len(result.Stdout)+len(result.Stderr) > 4096 || after.HeapAlloc > before.HeapAlloc+16<<20 {
			t.Fatal("output flood was not bounded", result, err, after.HeapAlloc-before.HeapAlloc)
		}
	})
	t.Run("ESC21", func(t *testing.T) {
		f := newFixture(t, image, "tmpfs")
		checks := []func() error{
			func() error { _, err := f.other.GetSandbox(f.ctx, f.target); return err },
			func() error { return f.other.DestroySandbox(f.ctx, f.target) },
			func() error { _, err := f.other.Keepalive(f.ctx, f.target); return err },
			func() error {
				_, err := f.other.Exec(f.ctx, f.target, client.ExecRequest{Cmd: []string{"/bin/sleep", "0"}})
				return err
			},
			func() error {
				stream, err := f.other.ExecStream(f.ctx, f.target, client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"/bin/sleep", "0"}}})
				if stream != nil {
					_ = stream.Close()
				}
				return err
			},
			func() error { return f.other.WriteFile(f.ctx, f.target, "/workspace/a", strings.NewReader("x")) },
			func() error {
				body, err := f.other.ReadFile(f.ctx, f.target, "/workspace/a")
				if body != nil {
					_ = body.Close()
				}
				return err
			},
			func() error { return f.other.DeleteFile(f.ctx, f.target, "/workspace/a", true) },
			func() error { _, err := f.other.ListFiles(f.ctx, f.target, "/workspace", 1); return err },
			func() error { return f.other.UploadArchive(f.ctx, f.target, "/workspace", strings.NewReader("x")) },
			func() error {
				body, err := f.other.DownloadArchive(f.ctx, f.target, "/workspace")
				if body != nil {
					_ = body.Close()
				}
				return err
			},
		}
		for i, check := range checks {
			err := check()
			var failure *client.Error
			if !errors.As(err, &failure) || failure.StatusCode != 404 {
				t.Fatal("ownership leak", i, err)
			}
		}
		page, err := f.other.ListSandboxes(f.ctx, client.ListOptions{Limit: 100})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("foreign sandbox visible in list", err)
		}
	})
}
