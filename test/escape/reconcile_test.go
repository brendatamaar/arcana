//go:build linux && (integration || escape)

package escape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/brendatamaar/arcana/internal/doctor"
	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestESC20_DaemonCrashReconcilesBeforeAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireRunsc(ctx, t)
	oci := "runsc"
	t.Log("restart runtime:", oci)
	docker(ctx, t, "build", "-t", "arcana-base:local", "../../templates/base")
	dir := t.TempDir()
	binary := filepath.Join(dir, "arcana")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/arcana") // #nosec G204 -- fixed compiler/argv; output is a test-owned temporary path.
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
		cmd := exec.CommandContext(ctx, binary, "serve", "--config", configFile) // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
		// A private log file avoids retaining output or exposing bootstrap credentials.
		log, err := os.OpenFile(filepath.Join(dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- private file in this test-owned temporary directory.
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
	secret, err := os.ReadFile(filepath.Join(dir, "admin.key")) // #nosec G304 -- read credentials only from this test daemon's private data directory.
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
		defer func() { _ = resp.Body.Close() }()
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
			if exec.CommandContext(cleanup, "docker", "inspect", "sbx-"+id).Run() == nil { // #nosec G204 -- API ID is one argv with a fixed sbx- prefix, never a shell string.
				docker(cleanup, t, "rm", "-f", "sbx-"+id)
			}
			if exec.CommandContext(cleanup, "docker", "volume", "inspect", "sbx-"+id+"-ws").Run() == nil { // #nosec G204 -- API ID is one argv with fixed resource-name prefixes and suffixes.
				docker(cleanup, t, "volume", "rm", "sbx-"+id+"-ws")
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
		return exec.CommandContext(ctx, "docker", "exec", "sbx-"+interrupted, "test", "-f", "/workspace/started").Run() == nil // #nosec G204 -- sandbox ID is one fixed-prefix argv, not shell text.
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
	docker(ctx, t, append([]string{"volume", "create"}, append(labels, volume)...)...)
	docker(ctx, t, append([]string{"network", "create", "--internal"}, append(labels, network)...)...)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "volume", "rm", volume).Run()   // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
		_ = exec.CommandContext(cleanup, "docker", "network", "rm", network).Run() // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	})
	daemon, exited = start()
	if exec.CommandContext(ctx, "docker", "volume", "inspect", volume).Run() == nil { // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
		t.Fatal("unreferenced managed volume survived")
	}
	if exec.CommandContext(ctx, "docker", "network", "inspect", network).Run() == nil { // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
		t.Fatal("unreferenced managed network survived")
	}
	lost := request("GET", "/v1/sandboxes/"+interrupted, "")
	if lost["state"] != "failed" || lost["end_reason"] != "lost" {
		t.Fatal("interrupted sandbox preserved", lost["state"], lost["end_reason"])
	}
	if exec.CommandContext(ctx, "docker", "inspect", "sbx-"+interrupted).Run() == nil { // #nosec G204 -- sandbox ID is one fixed-prefix argv, not shell text.
		t.Fatal("orphan container survived")
	}
	if exec.CommandContext(ctx, "docker", "volume", "inspect", "sbx-"+interrupted+"-ws").Run() == nil { // #nosec G204 -- sandbox ID remains within one canonical resource-name argv.
		t.Fatal("orphan volume survived")
	}
	peer := request("POST", "/v1/sandboxes/"+healthy+"/exec", `{"cmd":["/bin/echo","healthy"]}`)
	if peer["stdout"] != "healthy\n" || peer["exit_code"] != float64(0) {
		t.Fatal("peer unhealthy")
	}
	// Exercise the public CLI against the live daemon, including OS exit status
	// and successful binary HTTP uploads through the normal streaming client.
	cliArgs := []string{"--server", base, "--api-key", strings.TrimSpace(string(secret))}
	command := exec.CommandContext(ctx, binary, append(cliArgs, "exec", healthy, "--", "/bin/sh", "-c", "printf cli; exit 7")...) // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	var cliOut bytes.Buffer
	command.Stdout = &cliOut
	cliErr := command.Run()
	var exitErr *exec.ExitError
	if !errors.As(cliErr, &exitErr) || exitErr.ExitCode() != 7 || cliOut.String() != "cli" {
		t.Fatal("live CLI exit", cliErr, cliOut.String())
	}
	localFile := filepath.Join(dir, "upload.bin")
	if err = os.WriteFile(localFile, []byte{0, 255, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(ctx, binary, append(cliArgs, "files", "put", healthy, localFile, "/workspace/cli.bin")...) // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	if err = command.Run(); err != nil {
		t.Fatal("live CLI upload", err)
	}
	command = exec.CommandContext(ctx, binary, append(cliArgs, "files", "get", healthy, "/workspace/cli.bin")...) // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	cliBytes, err := command.Output()
	if err != nil || !bytes.Equal(cliBytes, []byte{0, 255, 'x'}) {
		t.Fatal("live CLI binary download", err, cliBytes)
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
	docker(ctx, t, "info", "--format", "{{.ServerVersion}}")
	if err = daemon.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = <-exited; err != nil {
		t.Fatal("graceful restart shutdown", err)
	}
	badConfig := config + "templates:\n  - name: base\n    image: arcana-doctor-missing:" + orphanID + "\n"
	if err = os.WriteFile(configFile, []byte(badConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	probeCtx, stopProbe := context.WithTimeout(ctx, 15*time.Second)
	defer stopProbe()
	command = exec.CommandContext(probeCtx, binary, "doctor", "--config", configFile, "--json") // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	diagnostic, doctorErr := command.Output()
	var report doctor.Report
	if doctorErr == nil || probeCtx.Err() != nil || json.Unmarshal(diagnostic, &report) != nil || !report.Failed() {
		t.Fatal("doctor did not report missing runtime prerequisites", doctorErr)
	}
	command = exec.CommandContext(probeCtx, binary, "serve", "--config", configFile) // #nosec G204 -- fixed test binary or Docker CLI; argv is scoped to this test's private files and managed resources.
	if err = command.Run(); err == nil || probeCtx.Err() != nil {
		t.Fatal("serve did not refuse failed prerequisites", err)
	}
	if resp, err := client.Get(base + "/v1/health"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("listener admitted requests after failed prerequisites")
	}
}
