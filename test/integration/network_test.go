//go:build linux && (integration || escape)

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNetwork_InternalProxyAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	path, err := filepath.Abs("../../bin/netprobe")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", path, "./netprobe/main_linux.go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build proxy: %v: %s", err, output)
	}
	network := func(subnet string, internal bool) string {
		name := "arcana-phase0-" + strings.ReplaceAll(subnet, "/", "-")
		args := []string{"network", "create", "--label=arcana.spike=004"}
		if internal {
			args = append(args, "--internal", "--subnet", subnet)
		}
		docker(t, ctx, append(args, name)...)
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			docker(t, cleanup, "network", "rm", name)
		})
		return name
	}
	outbound := network("outbound", false)
	firstNet, secondNet := network("10.200.250.0/28", true), network("10.200.250.16/28", true)
	start := func(netName, mode string) string {
		args := []string{"create", "--runtime=runsc", "--network", netName, "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:size=16m", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000,mode=0700", "--pids-limit=64", "--cpus=0.5", "--memory=256m", "--memory-swap=256m", "--label=arcana.managed=true", "--label=arcana.instance=phase0-spike", "--label=arcana.spike=004", "--mount", "type=bind,src=" + path + ",dst=/opt/arcana/sbx-exec,readonly", "debian:stable-slim", "/opt/arcana/sbx-exec", mode}
		id := docker(t, ctx, args...)
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			docker(t, cleanup, "rm", "-fv", id)
		})
		if mode == "proxy" {
			docker(t, ctx, "network", "connect", firstNet, id)
		}
		docker(t, ctx, "start", id)
		return id
	}
	proxy := start(outbound, "proxy")

	first, second := start(firstNet, "listen"), start(secondNet, "listen")
	ready, done := context.WithTimeout(ctx, 15*time.Second)
	defer done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if exec.CommandContext(ready, "docker", "exec", proxy, "/opt/arcana/sbx-exec", "check", "127.0.0.1:3128").Run() == nil {
			break
		}
		select {
		case <-ready.Done():
			t.Fatalf("proxy did not become ready: %s", docker(t, ctx, "logs", proxy))
		case <-ticker.C:
		}
	}
	ip := func(id, networkName string) string {
		format := "{{(index .NetworkSettings.Networks \"" + networkName + "\").IPAddress}}"
		return docker(t, ctx, "inspect", "--format", format, id)
	}
	proxyAddr := ip(proxy, firstNet) + ":3128"
	secondAddr := ip(second, secondNet) + ":8080"
	check := func(target string, viaProxy, wantSuccess bool) {
		t.Helper()
		args := []string{"exec", first, "/opt/arcana/sbx-exec", "check", target}
		if viaProxy {
			args = append(args, proxyAddr)
		}
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if (err == nil) != wantSuccess {
			t.Fatalf("target=%s proxy=%v success=%v err=%v output=%s", target, viaProxy, wantSuccess, err, output)
		}
		t.Logf("target=%s proxy=%v success=%v output=%s", target, viaProxy, wantSuccess, strings.TrimSpace(string(output)))
	}
	docker(t, ctx, "exec", second, "/opt/arcana/sbx-exec", "check", "127.0.0.1:8080")
	check("example.com:443", true, true)
	check("example.org:443", true, false)
	check("169.254.169.254:80", true, false)
	check("1.1.1.1:443", false, false)
	check("169.254.169.254:80", false, false)
	check(secondAddr, false, false)
	output, dnsErr := exec.CommandContext(ctx, "docker", "exec", first, "getent", "ahostsv4", "example.com").CombinedOutput()
	t.Logf("internal network DNS err=%v output=%s", dnsErr, strings.TrimSpace(string(output)))
	docker(t, ctx, "exec", first, "/opt/arcana/sbx-exec", "check", "127.0.0.1:8080")
	docker(t, ctx, "exec", second, "/opt/arcana/sbx-exec", "check", "127.0.0.1:8080")
	docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
}
