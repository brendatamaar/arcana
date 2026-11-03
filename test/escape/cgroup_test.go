//go:build linux && escape

package escape

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func kernelFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path) // #nosec G304 -- caller restricts this read-only kernel path to the test container's cgroup or numeric init PID.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		t.Fatal("invalid kernel counter file", err)
	}
	return string(data)
}

func cgroupPath(t *testing.T, f *fixture) string {
	t.Helper()
	id := docker(f.ctx, t, "inspect", "--format", "{{.Id}}", "sbx-"+f.target)
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatal("invalid test container ID")
	}
	pid, err := strconv.Atoi(docker(f.ctx, t, "inspect", "--format", "{{.State.Pid}}", "sbx-"+f.target))
	if err != nil || pid < 1 {
		t.Fatal("invalid test container init PID", err)
	}
	membership := kernelFile(t, "/proc/"+strconv.Itoa(pid)+"/cgroup")
	cgroup := ""
	for _, line := range strings.Split(membership, "\n") {
		if strings.HasPrefix(line, "0::") {
			cgroup = strings.TrimPrefix(line, "0::")
		}
	}
	path := filepath.Clean("/sys/fs/cgroup/" + strings.TrimPrefix(cgroup, "/"))
	if !strings.HasPrefix(path, "/sys/fs/cgroup/") || !strings.Contains(path, id) {
		t.Fatal("cgroup path does not identify this test container", path)
	}
	return path
}

func readCounter(path, key string) (uint64, error) {
	file, err := os.Open(path) // #nosec G304 -- path is restricted to this test container's cgroup by cgroupPath.
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, os.ErrInvalid
}

func monitorPids(t *testing.T, f *fixture) func() (uint64, uint64) {
	t.Helper()
	path := cgroupPath(t, f)
	before, err := readCounter(filepath.Join(path, "pids.events"), "max")
	if err != nil {
		t.Fatal(err)
	}
	beforeOOM, err := readCounter(filepath.Join(path, "memory.events"), "oom_kill")
	if err != nil {
		t.Fatal(err)
	}
	var maxPids, maxOOM atomic.Uint64
	maxPids.Store(before)
	maxOOM.Store(beforeOOM)
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if value, err := readCounter(filepath.Join(path, "pids.events"), "max"); err == nil {
					maxPids.Store(value)
				}
				if value, err := readCounter(filepath.Join(path, "memory.events"), "oom_kill"); err == nil {
					maxOOM.Store(value)
				}
			}
		}
	}()
	return func() (uint64, uint64) { cancel(); <-done; return maxPids.Load() - before, maxOOM.Load() - beforeOOM }
}
