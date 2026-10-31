//go:build linux && (integration || escape)

package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func docker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestHelper_ProcessGroupTimeoutKillAndTTY(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	path, err := filepath.Abs("../../bin/sbx-exec")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", path, "../../cmd/sbx-exec")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v: %s", err, output)
	}
	ttyPath, err := filepath.Abs("../../bin/ttycheck")
	if err != nil {
		t.Fatal(err)
	}
	build = exec.CommandContext(ctx, "go", "build", "-o", ttyPath, "./ttycheck/main_linux.go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build terminal probe: %v: %s", err, output)
	}
	for _, runtimeName := range []string{"runc", "runsc"} {
		t.Run(runtimeName, func(t *testing.T) {
			start := func(helper bool) string {
				args := []string{"run", "-d", "--runtime=" + runtimeName, "--network=none", "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:size=16m", "--tmpfs=/workspace:exec,size=16m,uid=1000,gid=1000", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000,mode=0700", "--pids-limit=64", "--cpus=0.5", "--memory=256m", "--memory-swap=256m", "--label=arcana.managed=true", "--label=arcana.instance=phase0-spike", "--label=arcana.spike=002", "--init"}
				if helper {
					args = append(args, "--mount", "type=bind,src="+path+",dst=/opt/arcana/sbx-exec,readonly")
				}
				id := docker(t, ctx, append(args, "debian:stable-slim", "sleep", "infinity")...)
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
					defer done()
					docker(t, cleanup, "rm", "-f", "-v", id)
				})
				return id
			}
			id, second := start(true), start(false)
			attack := `trap '' TERM; echo $$ > /tmp/parent; sleep 300 & echo $! > /tmp/child; wait`
			assertStopped := func() {
				t.Helper()
				out := docker(t, ctx, "exec", id, "/bin/sh", "-c", `for f in /tmp/parent /tmp/child; do read pid < "$f"; if test -r /proc/"$pid"/stat; then read p c state rest < /proc/"$pid"/stat; test "$state" = Z || exit 1; fi; done; echo stopped`)
				if out != "stopped" {
					t.Fatalf("surviving process: %s", out)
				}
				docker(t, ctx, "exec", id, "true")
				docker(t, ctx, "exec", second, "true")
				docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
			}
			status := func(execID string, wantTimeout bool) {
				t.Helper()
				out := docker(t, ctx, "exec", id, "/opt/arcana/sbx-exec", "status", "--id", execID)
				var got struct {
					ExitCode int    `json:"exit_code"`
					TimedOut bool   `json:"timed_out"`
					Signal   string `json:"signal"`
				}
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				if got.ExitCode != 137 || got.TimedOut != wantTimeout || got.Signal != "killed" {
					t.Fatalf("unexpected status: %s", out)
				}
			}
			cmd := exec.CommandContext(ctx, "docker", "exec", id, "/opt/arcana/sbx-exec", "run", "--id", "exe_timeout", "--timeout", "1", "--", "/bin/sh", "-c", attack)
			if err := cmd.Run(); err == nil {
				t.Fatal("timeout command unexpectedly succeeded")
			}
			status("exe_timeout", true)
			assertStopped()
			docker(t, ctx, "exec", id, "rm", "/tmp/child", "/tmp/parent")
			cmd = exec.CommandContext(ctx, "docker", "exec", id, "/opt/arcana/sbx-exec", "run", "--id", "exe_cancel", "--timeout", "30", "--", "/bin/sh", "-c", attack)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					<-waited
				}
			})
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			deadline, stop := context.WithTimeout(ctx, 10*time.Second)
			defer stop()
			for {
				if exec.CommandContext(deadline, "docker", "exec", id, "test", "-s", "/tmp/child").Run() == nil {
					break
				}
				select {
				case <-deadline.Done():
					t.Fatal("child did not start")
				case <-ticker.C:
				}
			}
			docker(t, ctx, "exec", id, "/opt/arcana/sbx-exec", "kill", "--id", "exe_cancel", "--grace", "200ms")
			if err := <-waited; err == nil {
				t.Fatal("cancel command unexpectedly succeeded")
			}
			status("exe_cancel", false)
			assertStopped()
			probeFile, err := os.Open(ttyPath)
			if err != nil {
				t.Fatal(err)
			}
			transfer := exec.CommandContext(ctx, "docker", "exec", "-i", id, "/bin/sh", "-c", "cat > /workspace/ttycheck; chmod 700 /workspace/ttycheck")
			transfer.Stdin = probeFile
			output, transferErr := transfer.CombinedOutput()
			_ = probeFile.Close()
			if transferErr != nil {
				t.Fatalf("transfer terminal probe via exec: %v: %s", transferErr, output)
			}
			out := docker(t, ctx, "exec", "-t", id, "/opt/arcana/sbx-exec", "run", "--tty", "--id", "exe_tty", "--", "/workspace/ttycheck")
			if !strings.Contains(out, "foreground=") {
				t.Fatalf("missing terminal probe result: %q", out)
			}
			t.Log(out)
			t.Log("read-only helper bind mount, timeout, kill, foreground TTY and second-sandbox/daemon health passed")
		})
	}
}
