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

// TestEnvd_RunsHardenedUnderBothRuntimes starts the daemon as the main process of a fully
// hardened sandbox and drives it with a probe that runs inside the same sandbox.
func TestEnvd_RunsHardenedUnderBothRuntimes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bin := t.TempDir()
	for _, build := range []struct{ target, source string }{{"envd", "../../cmd/envd"}, {"envdprobe", "./envdprobe/main_linux.go"}} {
		command := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(bin, build.target), build.source) // #nosec G204 -- fixed test build targets
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v %s", build.target, err, output)
		}
	}
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) {
			if oci == "runsc" && exec.CommandContext(ctx, "docker", "run", "--rm", "--runtime=runsc", "--network=none", "debian:stable-slim", "true").Run() != nil {
				t.Skip("runsc is not registered with Docker")
			}
			run := func(command ...string) string {
				args := []string{"run", "-d", "--runtime=" + oci, "--network=none", "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:exec,size=16m,uid=1000,gid=1000", "--tmpfs=/workspace:exec,size=16m,uid=1000,gid=1000", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000,mode=0700", "--pids-limit=128", "--cpus=0.5", "--memory=256m", "--memory-swap=256m", "--label=arcana.managed=true", "--label=arcana.instance=phase4-envd-test", "--init", "--mount", "type=bind,src=" + bin + ",dst=/opt/arcana,readonly", "debian:stable-slim"}
				id := docker(t, ctx, append(args, command...)...)
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
					defer done()
					docker(t, cleanup, "rm", "-f", id)
				})
				return id
			}
			id := run("/opt/arcana/envd", "serve", "--listen", "unix:/run/sbx/envd.sock", "--root", "/workspace", "--root", "/tmp")
			peer := run("sleep", "infinity")
			deadline := time.Now().Add(20 * time.Second)
			for exec.CommandContext(ctx, "docker", "exec", id, "test", "-S", "/run/sbx/envd.sock").Run() != nil { // #nosec G204 -- trusted test harness argv
				if time.Now().After(deadline) {
					t.Fatalf("envd did not start: %s", docker(t, ctx, "logs", id))
				}
				time.Sleep(100 * time.Millisecond)
			}
			output := docker(t, ctx, "exec", id, "/opt/arcana/envdprobe", "/run/sbx/envd.sock")
			for _, want := range []string{"info", "exit code and streams", "runs as the sandbox user", "timeout reaps detached descendants", "cancel and signal", "tty size", "files and symlinks stay in the sandbox"} {
				if !strings.Contains(output, "PASS "+want) {
					t.Errorf("missing %q in:\n%s", want, output)
				}
			}
			docker(t, ctx, "exec", peer, "true")
			docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
			if mode := docker(t, ctx, "exec", id, "stat", "-c", "%a", "/run/sbx/envd.sock"); mode != "600" {
				t.Errorf("socket mode %s", mode)
			}
		})
	}
}
