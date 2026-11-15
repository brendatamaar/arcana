//go:build linux && integration

package firecracker

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// TestRealFirecracker boots a real microVM. It skips unless /dev/kvm exists and the
// host is configured through ARCANA_FC_* variables, so it never passes vacuously:
// the skip message says what is missing. It has not been run yet (no KVM host so far).
func TestRealFirecracker(t *testing.T) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("/dev/kvm is not available")
	}
	cfg := Config{
		FirecrackerBinary: os.Getenv("ARCANA_FC_FIRECRACKER"), JailerBinary: os.Getenv("ARCANA_FC_JAILER"),
		KernelImage: os.Getenv("ARCANA_FC_KERNEL"), RootfsDir: os.Getenv("ARCANA_FC_ROOTFS_DIR"), StateDir: os.Getenv("ARCANA_FC_STATE_DIR"),
		JailerUID: 10001, JailerGID: 10001, InstallID: "fc-integration", BootTimeout: 60 * time.Second,
	}
	image := os.Getenv("ARCANA_FC_IMAGE")
	if cfg.FirecrackerBinary == "" || cfg.KernelImage == "" || cfg.RootfsDir == "" || cfg.StateDir == "" || image == "" {
		t.Skip("set ARCANA_FC_FIRECRACKER, ARCANA_FC_KERNEL, ARCANA_FC_ROOTFS_DIR, ARCANA_FC_STATE_DIR and ARCANA_FC_IMAGE (and ARCANA_FC_JAILER, or run unjailed via ARCANA_FC_UNJAILED=1)")
	}
	if cfg.JailerBinary == "" {
		if os.Getenv("ARCANA_FC_UNJAILED") != "1" {
			t.Skip("set ARCANA_FC_JAILER, or ARCANA_FC_UNJAILED=1 for development")
		}
		cfg.AllowUnjailed = true
	}
	driver, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	spec := runtime.Spec{
		SandboxID: "sbx_integrationtestabcdef", Image: image, NetworkMode: "none", WorkspaceBackend: "volume",
		Resources: runtime.Resources{CPUs: 1, MemoryMB: 512, DiskMB: 256, PIDs: 128},
	}
	instance, err := driver.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Destroy(context.Background(), instance.ID) })
	session, err := driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "id -u; echo hello"}, Cwd: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(session.Stdout())
	exit, err := session.Wait(ctx)
	if err != nil || exit.ExitCode != 0 || string(out) != "1000\nhello\n" {
		t.Fatalf("%q %+v %v", out, exit, err)
	}
}
