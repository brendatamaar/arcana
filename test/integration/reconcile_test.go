//go:build linux && (integration || escape)

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestESC20_DaemonCrashReconcilesBeforeAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	runtimes := docker(t, ctx, "info", "--format", "{{json .Runtimes}}")
	oci := "runsc"
	if !strings.Contains(runtimes, `"runsc"`) {
		oci = "runc"
		t.Log("runsc unavailable: runc does not prove isolation")
	}
	t.Log("restart runtime:", oci)
	docker(t, ctx, "build", "-t", "arcana-base:local", "../../templates/base")
	dir := t.TempDir()
	binary := filepath.Join(dir, "arcana")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/arcana")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v: %s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("data_dir: %s\nserver:\n  listen: %s\nruntime:\n  docker:\n    oci_runtime: %s\n    allow_insecure_runtime: %t\nlimits:\n  defaults:\n    memory_mb: 128\n    cpus: 0.25\n", dir, address, oci, oci == "runc")
	if err = os.WriteFile(configFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + address
	poll := func(check func() bool) {
		t.Helper()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			if check() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("restart probe deadline", ctx.Err())
			case <-ticker.C:
			}
		}
	}
	start := func() (*exec.Cmd, <-chan error) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, "serve", "--config", configFile)
		// A private log file avoids retaining output or exposing bootstrap credentials.
		log, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout = log
		cmd.Stderr = log
		if err = cmd.Start(); err != nil {
			_ = log.Close()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait(); _ = log.Close() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		poll(func() bool {
			select {
			case err := <-done:
				t.Fatalf("daemon exited: %v", err)
			default:
			}
			resp, err := client.Get(base + "/v1/health")
			if err != nil {
				return false
			}
			_ = resp.Body.Close()
			return resp.StatusCode == 200
		})
		return cmd, done
	}
	daemon, exited := start()
	secret, err := os.ReadFile(filepath.Join(dir, "admin.key"))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.Fatalf("%s %s: HTTP %d", method, path, resp.StatusCode)
		}
		var result map[string]any
		if len(data) > 0 {
			if err = json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	create := func() string {
		t.Helper()
		row := request("POST", "/v1/sandboxes", `{"template":"base"}`)
		id, ok := row["id"].(string)
		if !ok {
			t.Fatal("missing sandbox ID")
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if exec.CommandContext(cleanup, "docker", "inspect", "sbx-"+id).Run() == nil {
				docker(t, cleanup, "rm", "-f", "sbx-"+id)
			}
			if exec.CommandContext(cleanup, "docker", "volume", "inspect", "sbx-"+id+"-ws").Run() == nil {
				docker(t, cleanup, "volume", "rm", "sbx-"+id+"-ws")
			}
		})
		return id
	}
	interrupted, healthy := create(), create()
	execDone := make(chan struct{})
	go func() {
		defer close(execDone)
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/v1/sandboxes/"+interrupted+"/exec", bytes.NewBufferString(`{"shell":"echo ready > /workspace/started; sleep 120","timeout_seconds":120}`))
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(secret)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{}).Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	poll(func() bool {
		return exec.CommandContext(ctx, "docker", "exec", "sbx-"+interrupted, "test", "-f", "/workspace/started").Run() == nil
	})
	if err = daemon.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-exited
	<-execDone
	database, err := store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	installID := database.InstallID
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	orphanID, err := ids.New("sbx")
	if err != nil {
		t.Fatal(err)
	}
	volume, network := "sbx-"+orphanID+"-ws", "sbx-"+orphanID+"-net"
	labels := []string{"--label=arcana.managed=true", "--label=arcana.instance=" + installID, "--label=arcana.sandbox_id=" + orphanID}
	docker(t, ctx, append([]string{"volume", "create"}, append(labels, volume)...)...)
	docker(t, ctx, append([]string{"network", "create", "--internal"}, append(labels, network)...)...)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "volume", "rm", volume).Run()
		_ = exec.CommandContext(cleanup, "docker", "network", "rm", network).Run()
	})
	daemon, exited = start()
	if exec.CommandContext(ctx, "docker", "volume", "inspect", volume).Run() == nil {
		t.Fatal("unreferenced managed volume survived")
	}
	if exec.CommandContext(ctx, "docker", "network", "inspect", network).Run() == nil {
		t.Fatal("unreferenced managed network survived")
	}
	lost := request("GET", "/v1/sandboxes/"+interrupted, "")
	if lost["state"] != "failed" || lost["end_reason"] != "lost" {
		t.Fatal("interrupted sandbox preserved", lost["state"], lost["end_reason"])
	}
	if exec.CommandContext(ctx, "docker", "inspect", "sbx-"+interrupted).Run() == nil {
		t.Fatal("orphan container survived")
	}
	if exec.CommandContext(ctx, "docker", "volume", "inspect", "sbx-"+interrupted+"-ws").Run() == nil {
		t.Fatal("orphan volume survived")
	}
	peer := request("POST", "/v1/sandboxes/"+healthy+"/exec", `{"cmd":["/bin/echo","healthy"]}`)
	if peer["stdout"] != "healthy\n" || peer["exit_code"] != float64(0) {
		t.Fatal("peer unhealthy")
	}
	database, err = store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	var unfinished, unknown int
	err = database.SQL.QueryRowContext(ctx, "SELECT count(*), count(exit_code) FROM execs WHERE sandbox_id=? AND ended_at IS NULL", interrupted).Scan(&unfinished, &unknown)
	if err != nil || unfinished != 0 {
		t.Fatal("unfinished records", err, unfinished)
	}
	err = database.SQL.QueryRowContext(ctx, "SELECT count(exit_code) FROM execs WHERE sandbox_id=?", interrupted).Scan(&unknown)
	if err != nil || unknown != 0 {
		t.Fatal("invented exit status", err, unknown)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
	if err = daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = <-exited; err != nil {
		t.Fatal("graceful restart shutdown", err)
	}
}
