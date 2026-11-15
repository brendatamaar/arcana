//go:build linux && integration

package containerd

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
)

// TestCompareWithDocker times the same sandbox lifecycle through the Docker driver and this driver, for
// each OCI runtime, and logs a table (and writes it to ARCANA_COMPARE_OUT if set). It runs only when
// ARCANA_COMPARE=1, because it takes minutes and its numbers depend on the host.
func TestCompareWithDocker(t *testing.T) {
	if os.Getenv("ARCANA_COMPARE") != "1" {
		t.Skip("set ARCANA_COMPARE=1 (with the containerd variables) to run the comparison")
	}
	helper := os.Getenv("ARCANA_CONTAINERD_HELPER")
	type result struct{ create, exec, destroy []time.Duration }
	rows := map[string]*result{}
	order := []string{}
	iterations := 15
	for _, oci := range []string{"runc", "runsc"} {
		drivers := map[string]runtime.Driver{}
		docker, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: oci, AllowInsecureRuntime: oci == "runc", InstallID: "compare-" + oci, HelperPath: helper})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = docker.Close() })
		drivers["docker"] = docker
		drivers["containerd"] = containerdFixture(t, oci)
		for _, name := range []string{"docker", "containerd"} {
			driver := drivers[name]
			image := "arcana-base:local"
			if name == "containerd" {
				image = "docker.io/library/arcana-base:local"
			}
			key := fmt.Sprintf("%s/%s", name, oci)
			r := &result{}
			rows[key] = r
			order = append(order, key)
			for i := 0; i < iterations+1; i++ { // the first iteration warms caches and is not recorded
				id, err := ids.New("sbx")
				if err != nil {
					t.Fatal(err)
				}
				spec := runtime.Spec{SandboxID: id, Image: image, WorkspaceBackend: "tmpfs", NetworkMode: "none", Resources: runtime.Resources{CPUs: 1, MemoryMB: 256, DiskMB: 64, PIDs: 128}}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				started := time.Now()
				inst, err := driver.Create(ctx, spec)
				created := time.Since(started)
				if err != nil {
					cancel()
					t.Fatalf("%s create: %v", key, err)
				}
				started = time.Now()
				session, err := driver.Exec(ctx, inst.ID, runtime.ExecRequest{Cmd: []string{"/bin/true"}, Cwd: "/workspace"})
				if err == nil {
					_, _ = io.Copy(io.Discard, session.Stdout())
					_, err = session.Wait(ctx)
				}
				executed := time.Since(started)
				if err != nil {
					cancel()
					t.Fatalf("%s exec: %v", key, err)
				}
				started = time.Now()
				if err = driver.Destroy(ctx, inst.ID); err != nil {
					cancel()
					t.Fatalf("%s destroy: %v", key, err)
				}
				destroyed := time.Since(started)
				cancel()
				if i > 0 {
					r.create, r.exec, r.destroy = append(r.create, created), append(r.exec, executed), append(r.destroy, destroyed)
				}
			}
		}
	}
	p := func(values []time.Duration, q float64) string {
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		return fmt.Sprintf("%d ms", sorted[min(len(sorted)-1, int(q*float64(len(sorted))))].Milliseconds())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "| driver/runtime | create+start p50 / p95 | exec of true p50 / p95 | destroy p50 / p95 |\n|---|---|---|---|\n")
	for _, key := range order {
		r := rows[key]
		fmt.Fprintf(&b, "| %s | %s / %s | %s / %s | %s / %s |\n", key, p(r.create, .5), p(r.create, .95), p(r.exec, .5), p(r.exec, .95), p(r.destroy, .5), p(r.destroy, .95))
	}
	t.Log("\n" + b.String())
	if out := os.Getenv("ARCANA_COMPARE_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil { // #nosec G306 G703 -- benchmark table at an operator-chosen path in a test
			t.Fatal(err)
		}
	}
}
