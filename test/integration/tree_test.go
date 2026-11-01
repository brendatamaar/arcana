//go:build linux && (integration || escape)

package integration

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHelper_DetachedSessionsAndControllerSignalsAreReaped(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	helper := filepath.Join(t.TempDir(), "sbx-exec")
	attack := filepath.Join(t.TempDir(), "treeattack")
	for _, build := range []struct{ target, source string }{{helper, "../../cmd/sbx-exec"}, {attack, "./treeattack/main_linux.go"}} {
		command := exec.CommandContext(ctx, "go", "build", "-o", build.target, build.source)
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("building fixture: %v %s", err, output)
		}
	}
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) {
			start := func() string {
				id := docker(t, ctx, "run", "-d", "--runtime="+oci, "--network=none", "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:exec,size=16m,uid=1000,gid=1000", "--tmpfs=/workspace:exec,size=16m,uid=1000,gid=1000", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000,mode=0700", "--pids-limit=128", "--cpus=0.5", "--memory=256m", "--memory-swap=256m", "--label=arcana.managed=true", "--label=arcana.instance=phase1-helper-test", "--mount", "type=bind,src="+helper+",dst=/opt/arcana/sbx-exec,readonly", "--init", "debian:stable-slim", "sleep", "infinity")
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
					defer done()
					docker(t, cleanup, "rm", "-f", id)
				})
				return id
			}
			id, peer := start(), start()
			file, err := os.Open(attack)
			if err != nil {
				t.Fatal(err)
			}
			transfer := exec.CommandContext(ctx, "docker", "exec", "-i", id, "/usr/bin/tee", "/workspace/treeattack")
			transfer.Stdin = file
			transfer.Stdout = io.Discard
			transfer.Stderr = io.Discard
			transferErr := transfer.Run()
			_ = file.Close()
			if transferErr != nil {
				t.Fatal(transferErr)
			}
			docker(t, ctx, "exec", id, "/bin/chmod", "0700", "/workspace/treeattack")
			for _, mode := range []string{"timeout", "kill", "signal"} {
				t.Run(mode, func(t *testing.T) {
					execID := "exe_detached_" + mode
					seconds := "30"
					role := "root"
					if mode == "timeout" {
						seconds = "2"
					}
					if mode == "signal" {
						role = "signal"
					}
					command := exec.CommandContext(ctx, "docker", "exec", id, "/opt/arcana/sbx-exec", "run", "--id", execID, "--timeout", seconds, "--", "/workspace/treeattack", role)
					if err := command.Start(); err != nil {
						t.Fatal(err)
					}
					waited := make(chan error, 1)
					done := make(chan struct{})
					go func() { waited <- command.Wait(); close(done) }()
					t.Cleanup(func() {
						select {
						case <-done:
						default:
							_ = command.Process.Kill()
							<-done
						}
					})
					if mode == "kill" {
						deadline, done := context.WithTimeout(ctx, 10*time.Second)
						defer done()
						poll := time.NewTicker(20 * time.Millisecond)
						defer poll.Stop()
						for exec.CommandContext(deadline, "docker", "exec", id, "test", "-s", "/tmp/tree_detached").Run() != nil {
							select {
							case <-deadline.Done():
								t.Fatal("detached descendant did not start")
							case <-poll.C:
							}
						}
						docker(t, ctx, "exec", id, "/opt/arcana/sbx-exec", "kill", "--id", execID, "--grace", "100ms")
					}
					if err := <-waited; err == nil {
						t.Fatal("attack unexpectedly exited successfully")
					}
					var status struct {
						ExitCode int    `json:"exit_code"`
						TimedOut bool   `json:"timed_out"`
						Cleanup  bool   `json:"cleanup_complete"`
						Signal   string `json:"signal"`
					}
					if err := json.Unmarshal([]byte(docker(t, ctx, "exec", id, "/opt/arcana/sbx-exec", "status", "--id", execID)), &status); err != nil {
						t.Fatal(err)
					}
					if status.ExitCode != 137 || status.Signal != "killed" || status.TimedOut != (mode == "timeout") || !status.Cleanup {
						t.Fatalf("incorrect completed status: %+v", status)
					}
					for _, name := range []string{"parent", "middle", "detached"} {
						raw := docker(t, ctx, "exec", id, "cat", "/tmp/tree_"+name)
						pid, err := strconv.Atoi(strings.TrimSpace(raw))
						if err != nil || pid <= 1 {
							t.Fatalf("invalid attack PID: %q", raw)
						}
						if err = exec.CommandContext(ctx, "docker", "exec", id, "test", "!", "-e", "/proc/"+strconv.Itoa(pid)).Run(); err != nil {
							t.Fatalf("descendant %s (%d) was not reaped", name, pid)
						}
					}
					docker(t, ctx, "exec", id, "true")
					docker(t, ctx, "exec", peer, "true")
					docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
					docker(t, ctx, "exec", id, "rm", "/tmp/tree_parent", "/tmp/tree_middle", "/tmp/tree_detached")
				})
			}
		})
	}
}
