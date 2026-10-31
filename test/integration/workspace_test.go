//go:build linux && (integration || escape)

package integration

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkspace_ExecRoundTripQuotaAndReadonlyRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Seed volume ownership from image metadata, without any host volume access.
	build := exec.CommandContext(ctx, "docker", "build", "-t", "arcana-phase0-workspace", "-")
	build.Stdin = strings.NewReader("FROM debian:stable-slim\nRUN mkdir /workspace && chown 1000:1000 /workspace\nUSER 1000:1000\n")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	volume := docker(t, ctx, "volume", "create", "--label=arcana.spike=003")
	t.Cleanup(func() { docker(t, context.Background(), "volume", "rm", volume) })
	start := func(tmpfs bool) string {
		args := []string{"run", "-d", "--runtime=runsc", "--network=none", "--read-only", "--user=1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--tmpfs=/tmp:size=16m", "--tmpfs=/run/sbx:size=4m,uid=1000,gid=1000,mode=0700", "--pids-limit=64", "--cpus=0.5", "--memory=256m", "--memory-swap=256m", "--label=arcana.managed=true", "--label=arcana.instance=phase0-spike", "--label=arcana.spike=003"}
		if tmpfs {
			args = append(args, "--tmpfs=/workspace:size=8m,uid=1000,gid=1000")
		} else {
			args = append(args, "--mount", "type=volume,src="+volume+",dst=/workspace")
		}
		id := docker(t, ctx, append(args, "arcana-phase0-workspace", "sleep", "infinity")...)
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			docker(t, cleanup, "rm", "-fv", id)
		})
		return id
	}
	id, limited := start(false), start(true)
	docker(t, ctx, "exec", id, "/bin/sh", "-c", `mkdir /workspace/source /workspace/restored; printf 'arcana\000roundtrip\n' > /workspace/source/probe; touch /tmp/writable /run/sbx/writable`)
	read := exec.CommandContext(ctx, "docker", "exec", id, "cat", "/workspace/source/probe")
	file, err := read.Output()
	if err != nil || !bytes.Equal(file, []byte("arcana\x00roundtrip\n")) {
		t.Fatalf("read round trip: %q %v", file, err)
	}
	archive := func(path string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", "exec", id, "tar", "-czf", "-", "-C", path, ".")
		data, err := cmd.Output()
		if err != nil || len(data) > 1<<20 {
			t.Fatalf("archive: size=%d err=%v", len(data), err)
		}
		return data
	}
	original := archive("/workspace/source")
	put := exec.CommandContext(ctx, "docker", "exec", "-i", id, "tar", "-xzf", "-", "-C", "/workspace/restored")
	put.Stdin = bytes.NewReader(original)
	if output, err := put.CombinedOutput(); err != nil {
		t.Fatalf("extract inside sandbox: %v: %s", err, output)
	}
	if restored := archive("/workspace/restored"); !bytes.Equal(original, restored) {
		t.Fatal("archive round trip differs")
	}
	read = exec.CommandContext(ctx, "docker", "exec", id, "cat", "/workspace/restored/probe")
	file, err = read.Output()
	if err != nil || !bytes.Equal(file, []byte("arcana\x00roundtrip\n")) {
		t.Fatalf("restored file: %q %v", file, err)
	}
	out, err := exec.CommandContext(ctx, "docker", "exec", id, "touch", "/etc/arcana-probe").CombinedOutput()
	if err == nil || (!strings.Contains(string(out), "Read-only file system") && !strings.Contains(string(out), "Permission denied")) {
		t.Fatalf("rootfs write: err=%v output=%s", err, out)
	}
	out, err = exec.CommandContext(ctx, "docker", "exec", limited, "dd", "if=/dev/zero", "of=/workspace/fill", "bs=1M", "count=12").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "No space left on device") {
		t.Fatalf("tmpfs quota: err=%v output=%s", err, out)
	}
	docker(t, ctx, "exec", limited, "true")
	docker(t, ctx, "exec", id, "true")
	docker(t, ctx, "info", "--format", "{{.ServerVersion}}")
	docker(t, ctx, "exec", id, "/bin/sh", "-c", `mkdir /workspace/tree; i=0; while test "$i" -lt 10000; do printf x > /workspace/tree/file_$i; i=$((i+1)); done`)
	for n := 0; n < 5; n++ {
		out := docker(t, ctx, "exec", id, "/bin/sh", "-c", `start=$(date +%s%N); du -sb /workspace/tree; end=$(date +%s%N); echo $((end-start))`)
		lines := strings.Split(out, "\n")
		nanos, err := strconv.ParseInt(lines[len(lines)-1], 10, 64)
		if err != nil || nanos <= 0 {
			t.Fatalf("du measurement: %q %v", out, err)
		}
		t.Logf("du 10k files: %s; duration=%.3fms", lines[0], float64(nanos)/1e6)
	}
	t.Log("volume bytes/archive round trip, read-only root, writable tmpfs and 8MiB workspace ENOSPC passed; both sandboxes and daemon healthy")
}
