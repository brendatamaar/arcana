//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestAllowlist_ProxyAllowedDeniedAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	recorder, err := audit.New(audit.SQLBackend{Store: database}, audit.SystemClock{}, 30, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := recorder.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var events []netpolicy.Decision
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: database.InstallID, HelperPath: path, Network: &dockerruntime.NetworkConfig{SubnetPool: "10.201.0.0/16", SubnetPrefix: 28, ProxyImage: "arcana:local", ProxyDNS: []string{"1.1.1.1", "1.0.0.1"}, DataDir: dir, OnDecision: func(event netpolicy.Decision) {
		events = append(events, event)
		recorder.Record("egress.decision", "", event.SandboxID, event)
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.Default()
	cfg.Templates[0].Image = "arcana-python:local"
	cfg.Server.DestroyOnShutdown = true
	for _, name := range []string{"python", "node", "go"} {
		cfg.Templates = append(cfg.Templates, config.Template{Name: name, Image: "arcana-" + name + ":local", NetworkPreset: name})
	}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{}, recorder)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(database, auth.SystemClock{}, recorder)
	owner, _, err := keys.Create(ctx, "network-test", false)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{ID: owner.ID}
	row, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base", NetworkMode: "allowlist", NetworkAllow: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(id, source string) string {
		t.Helper()
		cmd := []string{"python", "-c", source}
		result, err := service.Exec(ctx, principal, id, orchestrator.ExecRequest{Cmd: &cmd})
		if err != nil || result.ExitCode != 0 {
			diagnoseProxy(t, ctx, service, principal, id)
			t.Fatalf("exec: %+v %v", result, err)
		}
		return result.Stdout
	}
	if out := run(row.ID, "import urllib.request; r=urllib.request.urlopen('https://example.com', timeout=15); print(r.status)"); !strings.Contains(out, "200") {
		t.Fatal(out)
	}
	run(row.ID, "import urllib.request,urllib.error\ntry: urllib.request.urlopen('http://example.org',timeout=10); raise RuntimeError('denial failed')\nexcept urllib.error.HTTPError as e: assert e.code==403")
	run(row.ID, "import socket\ns=socket.socket();s.settimeout(2)\ntry: s.connect(('1.1.1.1',443));raise RuntimeError('direct egress succeeded')\nexcept OSError: pass")
	run(peer.ID, "print('healthy')")
	if err = driver.DrainDecisions(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, denied := false, false
	for _, event := range events {
		allowed = allowed || event.Allowed
		denied = denied || !event.Allowed
	}
	if !allowed || !denied {
		t.Fatalf("missing decisions: %+v", events)
	}
	// Restart reconciliation must restore policy without granting a direct route.
	if err = service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	run(row.ID, "import urllib.request; assert urllib.request.urlopen('https://example.com',timeout=15).status==200")
	run(peer.ID, "print('still healthy')")
	for _, ecosystem := range []struct {
		name     string
		commands [][]string
	}{
		{"python", [][]string{{"python", "-m", "venv", "/workspace/.venv"}, {"python", "-m", "pip", "install", "requests"}}},
		{"node", [][]string{{"npm", "install", "--no-audit", "--no-fund", "lodash"}}},
		{"go", [][]string{{"go", "mod", "init", "example.com/probe"}, {"go", "mod", "edit", "-require=rsc.io/quote@v1.5.2"}, {"go", "mod", "download"}}},
	} {
		sandbox, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: ecosystem.name, NetworkMode: "allowlist"})
		if err != nil {
			t.Fatal(ecosystem.name, err)
		}
		for _, argv := range ecosystem.commands {
			result, err := service.Exec(ctx, principal, sandbox.ID, orchestrator.ExecRequest{Cmd: &argv})
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("%s package installation: %+v %v", ecosystem.name, result, err)
			}
		}
		if err = service.Destroy(ctx, principal, sandbox.ID, "test"); err != nil {
			t.Fatal(err)
		}
	}

	content := "private-file-content"
	if err = service.WriteFile(ctx, principal, row.ID, "/workspace/audit-probe", io.NopCloser(strings.NewReader(content))); err != nil {
		t.Fatal(err)
	}
	if err = service.DeleteFile(ctx, principal, row.ID, "/workspace/audit-probe", false); err != nil {
		t.Fatal(err)
	}
	recorder.Record("test.barrier", owner.ID, row.ID, nil)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		page, err := recorder.List(ctx, audit.Filter{Action: "test.barrier"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	for _, action := range []string{"file.written", "file.deleted", "egress.decision"} {
		page, err := recorder.List(ctx, audit.Filter{Action: action, KeyID: owner.ID, SandboxID: row.ID})
		if err != nil || len(page.Items) == 0 {
			t.Fatal(action, page, err)
		}
		for _, event := range page.Items {
			if strings.Contains(string(event.Details), content) {
				t.Fatal("file contents persisted")
			}
			if action != "egress.decision" {
				var details struct {
					Size int64 `json:"size_bytes"`
				}
				if json.Unmarshal(event.Details, &details) != nil || details.Size != int64(len(content)) {
					t.Fatal("incorrect file size metadata", string(event.Details))
				}
			}
		}
	}
	if recorder.Stats().Errors != 0 {
		t.Fatal(recorder.Stats())
	}
}

// diagnoseProxy logs Docker and in-sandbox network state when a proxy exec fails.
// TEMPORARY: added to debug a CI-only post-reconcile timeout.
func diagnoseProxy(t *testing.T, ctx context.Context, service *orchestrator.Service, principal auth.Principal, id string) {
	t.Helper()
	for _, args := range [][]string{
		{"version", "--format", "{{.Server.Version}} api={{.Server.APIVersion}}"},
		{"ps", "-a", "--format", "{{.Names}} {{.Status}} {{.Image}}"},
		{"network", "ls", "--format", "{{.Name}} {{.Driver}} {{.Internal}}"},
	} {
		out, _ := exec.CommandContext(ctx, "docker", args...).CombinedOutput() // #nosec G204 -- fixed diagnostic arguments
		t.Logf("docker %v:\n%s", args, out)
	}
	for _, args := range [][]string{{"ip", "-br", "link"}, {"bridge", "fdb", "show"}, {"bridge", "link", "show"}, {"ip", "neigh"}} {
		out, _ := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput() // #nosec G204 -- fixed diagnostic arguments
		t.Logf("%v:\n%s", args, out)
	}
	names, _ := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "name=-proxy", "--format", "{{.Names}}").Output() // #nosec G204 -- fixed diagnostic arguments
	for _, name := range strings.Fields(string(names)) {
		for _, args := range [][]string{
			{"inspect", "--format", "{{json .NetworkSettings.Networks}}", name},
			{"logs", "--tail", "40", name},
		} {
			out, _ := exec.CommandContext(ctx, "docker", append(args[:1:1], append(args[1:len(args)-1:len(args)-1], name)...)...).CombinedOutput() // #nosec G204 -- fixed diagnostic arguments
			t.Logf("docker %v:\n%s", args, out)
		}
	}
	cmd := []string{"python", "-c", "import os,socket,time\nu=os.environ['HTTPS_PROXY'].split('//')[1].split(':')\nfor i in range(3):\n s=socket.socket();s.settimeout(3)\n try: s.connect((u[0],int(u[1])));print('connected',i)\n except OSError as e: print('connect',i,e)\n print(open('/proc/net/arp').read())\n time.sleep(1)"}
	result, err := service.Exec(ctx, principal, id, orchestrator.ExecRequest{Cmd: &cmd})
	t.Logf("sandbox diagnostics: %+v %v", result, err)
}
