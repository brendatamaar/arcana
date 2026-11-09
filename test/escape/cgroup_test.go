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

// monitorCPU uses host controller counters because guest getrusage includes
// runtime-specific accounting that is not equivalent to physical CPU use.
func monitorCPU(t *testing.T, f *fixture) func() {
	t.Helper()
	path := cgroupPath(t, f)
	nano, err := strconv.ParseInt(docker(f.ctx, t, "inspect", "--format", "{{.HostConfig.NanoCpus}}", "sbx-"+f.target), 10, 64)
	if err != nil || nano != 250000000 {
		t.Fatal("unexpected CPU allocation", nano, err)
	}
	fields := strings.Fields(kernelFile(t, filepath.Join(path, "cpu.max")))
	if len(fields) != 2 {
		t.Fatal("invalid cpu.max")
	}
	quota, qerr := strconv.ParseUint(fields[0], 10, 64)
	period, perr := strconv.ParseUint(fields[1], 10, 64)
	if qerr != nil || perr != nil || period == 0 || period > 1000000 || quota != period/4 || period%4 != 0 {
		t.Fatal("CPU controller quota differs from 0.25 CPU", fields)
	}
	before, err := readCounter(filepath.Join(path, "cpu.stat"), "usage_usec")
	if err != nil {
		t.Fatal(err)
	}
	throttled, err := readCounter(filepath.Join(path, "cpu.stat"), "nr_throttled")
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		after, err := readCounter(filepath.Join(path, "cpu.stat"), "usage_usec")
		if err != nil || after <= before || after-before > 1500000 {
			t.Fatal("physical CPU ceiling exceeded", before, after, err)
		}
		count, err := readCounter(filepath.Join(path, "cpu.stat"), "nr_throttled")
		if err != nil || count <= throttled {
			t.Fatal("CPU throttling was not observed", throttled, count, err)
		}
		t.Logf("container CPU=%d us; throttled periods=%d", after-before, count-throttled)
	}
}
