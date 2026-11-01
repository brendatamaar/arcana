//go:build linux && (integration || escape)

package docker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
)

func realExec(t *testing.T, ctx context.Context, driver *Driver, id string, request runtime.ExecRequest, input string) (string, string, runtime.ExitInfo) {
	t.Helper()
	session, err := driver.Exec(ctx, id, request)
	if err != nil {
		t.Fatal(err)
	}
	if input != "" {
		if _, err = io.WriteString(session.Stdin(), input); err != nil {
			t.Fatal(err)
		}
	}
	if err = session.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr []byte
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		var err error
		stdout, err = io.ReadAll(session.Stdout())
		if err != nil {
			t.Error(err)
		}
	}()
	if session.Stderr() != nil {
		group.Add(1)
		go func() {
			defer group.Done()
			var err error
			stderr, err = io.ReadAll(session.Stderr())
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	result, err := session.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return string(stdout), string(stderr), result
}

func TestDocker_RealHardeningWorkspaceExecAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "docker", "build", "-t", "arcana-base:local", "../../../templates/base")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building minimal base: %v: %s", err, output)
	}
	helper := filepath.Join(t.TempDir(), "sbx-exec")
	build = exec.CommandContext(ctx, "go", "build", "-o", helper, "../../../cmd/sbx-exec")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building helper: %v: %s", err, output)
	}
	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .Runtimes}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	var available map[string]json.RawMessage
	if err = json.Unmarshal(output, &available); err != nil {
		t.Fatal(err)
	}
	for _, oci := range []string{"runsc", "runc"} {
		if _, ok := available[oci]; !ok {
			t.Logf("skipping unavailable OCI runtime %s; meaningful isolation checks require runsc", oci)
			continue
		}
		for _, backend := range []string{"volume", "tmpfs"} {
			t.Run(oci+"/"+backend, func(t *testing.T) {
				t.Logf("real Docker runtime %s; runc is explicitly opted in for comparison", oci)
				installID, err := ids.New("sbx")
				if err != nil {
					t.Fatal(err)
				}
				driver, err := New(Config{OCIRuntime: oci, AllowInsecureRuntime: oci == "runc", InstallID: installID, HelperPath: helper})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := driver.Close(); err != nil {
						t.Error(err)
					}
				})
				spec := testSpec()
				spec.Image = "arcana-base:local"
				spec.WorkspaceBackend = backend
				spec.SandboxID, err = ids.New("sbx")
				if err != nil {
					t.Fatal(err)
				}
				instance, err := driver.Create(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
					defer done()
					if err := driver.Destroy(cleanup, instance.ID); err != nil {
						t.Error(err)
					}
				})
				peerSpec := spec
				peerSpec.SandboxID, err = ids.New("sbx")
				if err != nil {
					t.Fatal(err)
				}
				peerSpec.WorkspaceBackend = "tmpfs"
				peer, err := driver.Create(ctx, peerSpec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
					defer done()
					if err := driver.Destroy(cleanup, peer.ID); err != nil {
						t.Error(err)
					}
				})
				inspected, err := driver.client.ContainerInspect(ctx, instance.ID, client.ContainerInspectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				c := inspected.Container
				h := c.HostConfig
				if c.Config.User != "1000:1000" || h.Runtime != oci || !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || !slices.Contains(h.CapDrop, "ALL") || h.NetworkMode != "none" || h.Init == nil || !*h.Init {
					t.Fatal("actual container lacks mandatory hardening")
				}
				if !slices.Contains(h.SecurityOpt, "no-new-privileges") && !slices.Contains(h.SecurityOpt, "no-new-privileges=true") {
					t.Fatal("no-new-privileges missing")
				}
				if h.NanoCPUs != 500000000 || h.Memory != 128*megabyte || h.MemorySwap != h.Memory || h.PidsLimit == nil || *h.PidsLimit != 64 || len(h.Ulimits) != 1 || h.Ulimits[0].Hard != 1024 {
					t.Fatal("actual resource limits differ")
				}
				if h.Tmpfs["/tmp"] == "" || h.Tmpfs["/run/sbx"] == "" || h.LogConfig.Type != "none" {
					t.Fatal("temporary filesystem/logging bounds missing")
				}
				helperFound := false
				workspaceFound := backend == "tmpfs" && h.Tmpfs["/workspace"] != ""
				for _, m := range c.Mounts {
					switch m.Type {
					case mount.TypeBind:
						if m.Source != helper || m.Destination != "/opt/arcana/sbx-exec" || m.RW {
							t.Fatalf("unexpected host bind: %+v", m)
						}
						helperFound = true
					case mount.TypeVolume:
						if backend != "volume" || m.Destination != "/workspace" {
							t.Fatalf("unexpected volume: %+v", m)
						}
						workspaceFound = true
					}
				}
				if !helperFound || !workspaceFound {
					t.Fatal("helper/workspace mounts missing")
				}
				stdout, _, result := realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/usr/bin/id", "-u"}}, "")
				if result.ExitCode != 0 || strings.TrimSpace(stdout) != "1000" {
					t.Fatal("command did not run as UID 1000")
				}
				_, stderr, result := realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/usr/bin/touch", "/workspace/probe"}}, "")
				if result.ExitCode != 0 {
					t.Fatalf("workspace is not writable by UID 1000: %s", stderr)
				}
				_, _, result = realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/bin/cp", "/opt/arcana/sbx-exec", "/workspace/copied-helper"}}, "")
				if result.ExitCode != 0 {
					t.Fatal("helper copy into workspace failed")
				}
				stdout, _, result = realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/workspace/copied-helper", "run", "--id", "exe_workspace", "--", "/usr/bin/id", "-u"}}, "")
				if result.ExitCode != 0 || strings.TrimSpace(stdout) != "1000" {
					t.Fatal("workspace does not support executable artifacts")
				}
				_, _, result = realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/usr/bin/touch", "/etc/arcana-denied"}}, "")
				if result.ExitCode == 0 {
					t.Fatal("write outside writable roots succeeded")
				}
				_, stderr, result = realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/bin/ls", "/definitely-missing"}}, "")
				if result.ExitCode == 0 || stderr == "" {
					t.Fatal("stderr/exit code was lost")
				}
				session, err := driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"/bin/sh", "-c", "read line; stty size"}, TTY: true, Cols: 80, Rows: 24})
				if err != nil {
					t.Fatal(err)
				}
				if err = session.Resize(120, 40); err != nil {
					t.Fatal(err)
				}
				if _, err = io.WriteString(session.Stdin(), "ready\n"); err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(session.Stdout())
				if err != nil {
					t.Fatal(err)
				}
				ttyExit, err := session.Wait(ctx)
				if err != nil || ttyExit.ExitCode != 0 || !strings.Contains(string(data), "40 120") || session.Stderr() != nil {
					t.Fatalf("TTY resize failed: %q %v", data, err)
				}
				if backend == "tmpfs" {
					_, _, result = realExec(t, ctx, driver, instance.ID, runtime.ExecRequest{Cmd: []string{"/bin/dd", "if=/dev/zero", "of=/workspace/full", "bs=1M", "count=9"}}, "")
					if result.ExitCode == 0 {
						t.Fatal("tmpfs disk bound was not enforced")
					}
				}
				stdout, _, result = realExec(t, ctx, driver, peer.ID, runtime.ExecRequest{Cmd: []string{"/usr/bin/id", "-u"}}, "")
				if result.ExitCode != 0 || strings.TrimSpace(stdout) != "1000" {
					t.Fatal("peer unhealthy after denied writes/quota attempt")
				}
				listed, err := driver.List(ctx)
				if err != nil || len(listed) != 2 {
					t.Fatalf("managed list/daemon health: %d %v", len(listed), err)
				}
				if _, err = driver.Stats(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
				if err = driver.Destroy(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
				if err = driver.Destroy(ctx, instance.ID); err != nil {
					t.Fatal(err)
				}
				if _, err = driver.client.ContainerInspect(ctx, instance.ID, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
					t.Fatalf("container remains: %v", err)
				}
				if _, err = driver.client.VolumeInspect(ctx, instance.ID+"-ws", client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
					t.Fatalf("volume remains: %v", err)
				}
			})
		}
	}
}
