package containerd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// asSandboxUser makes the ownership rule see UID 1000 so the tests need no privileges.
func asSandboxUser(t *testing.T) {
	t.Helper()
	previous := geteuid
	geteuid = func() int { return sandboxUID }
	t.Cleanup(func() { geteuid = previous })
}

func volumeDriver(t *testing.T) (*Driver, *fakeEngine, string) {
	t.Helper()
	asSandboxUser(t)
	d, f := newDriver(t)
	d.cfg.WorkspaceDir = filepath.Join(t.TempDir(), "workspaces")
	return d, f, d.cfg.WorkspaceDir
}

func volumeSpec() runtime.Spec {
	s := testSpec()
	s.WorkspaceBackend = "volume"
	return s
}

func TestPlanContainer_VolumeWorkspaceIsAPerSandboxBindMount(t *testing.T) {
	cfg := testConfig(t)
	cfg.WorkspaceDir = "/var/lib/arcana/containerd/workspaces"
	p, err := planContainer(cfg, volumeSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := "/var/lib/arcana/containerd/workspaces/sbx_abcdefghijklmnopqrst"
	if p.Workspace != want {
		t.Fatalf("workspace %q", p.Workspace)
	}
	spec, err := applySpec(context.Background(), p.SpecOpts)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, m := range spec.Mounts {
		if m.Destination != "/workspace" {
			continue
		}
		found = true
		if m.Type != "bind" || m.Source != want || !slices.Contains(m.Options, "rw") || !slices.Contains(m.Options, "nosuid") || !slices.Contains(m.Options, "nodev") || slices.Contains(m.Options, "noexec") {
			t.Errorf("workspace mount %+v", m)
		}
	}
	if !found {
		t.Fatal("no /workspace mount")
	}
	// The only host directories in the spec are the helper and this sandbox's own workspace.
	for _, m := range spec.Mounts {
		if m.Type == "bind" && m.Source != want && m.Destination != "/opt/arcana/sbx-exec" && !strings.HasPrefix(m.Source, "/proc") && !strings.HasPrefix(m.Source, "/sys") && !strings.HasPrefix(m.Source, "/dev") {
			t.Errorf("unexpected host bind mount %+v", m)
		}
	}
	tmpfs, err := planContainer(testConfig(t), testSpec())
	if err != nil || tmpfs.Workspace != "" {
		t.Fatalf("tmpfs plan %+v %v", tmpfs, err)
	}
}

func TestConfigValidate_WorkspaceDirMustBeACleanAbsolutePath(t *testing.T) {
	for _, bad := range []string{"relative/dir", "/a/../b", "/a//b"} {
		c := testConfig(t)
		c.WorkspaceDir = bad
		if c.Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDriver_VolumeWorkspaceLivesExactlyAsLongAsTheContainer(t *testing.T) {
	d, f, root := volumeDriver(t)
	ctx := context.Background()
	inst, err := d.Create(ctx, volumeSpec())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sbx_abcdefghijklmnopqrst")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
		t.Fatalf("workspace %v %v", info, err)
	}
	if rootInfo, statErr := os.Stat(root); statErr != nil || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("the parent must stay private: %v %v", rootInfo, statErr)
	}
	if f.created[0].Workspace != dir {
		t.Fatalf("engine saw %q", f.created[0].Workspace)
	}
	// Something the sandbox wrote, including a directory it made unreadable.
	hidden := filepath.Join(dir, "a", "b")
	if err = os.MkdirAll(hidden, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(hidden, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(hidden, 0); err != nil {
		t.Fatal(err)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("destroy must remove even unreadable directories: %v", err)
	}
	if _, err = os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace survived destroy: %v", err)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("destroy must stay idempotent: %v", err)
	}
}

func TestDriver_VolumeWorkspaceIsNeverSharedOrLeaked(t *testing.T) {
	d, f, root := volumeDriver(t)
	ctx := context.Background()
	dir := filepath.Join(root, "sbx_abcdefghijklmnopqrst")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "left-over"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var invalid runtime.Invalid
	if _, err := d.Create(ctx, volumeSpec()); !errors.As(err, &invalid) || len(f.created) != 0 {
		t.Fatalf("an existing workspace must be refused, not reused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "left-over")); err != nil {
		t.Fatal("a refused create must not delete the existing directory")
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	f.failCreate = errors.New("shim refused")
	if _, err := d.Create(ctx, volumeSpec()); err == nil {
		t.Fatal("create succeeded")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a failed create left its workspace: %v", err)
	}
}

func TestDriver_VolumeWorkspaceRefusesAUserThatCannotOwnIt(t *testing.T) {
	d, f, root := volumeDriver(t)
	geteuid = func() int { return 1234 }
	if _, err := d.Create(context.Background(), volumeSpec()); err == nil || !strings.Contains(err.Error(), "root or UID 1000") {
		t.Fatalf("want an actionable refusal, got %v", err)
	}
	if len(f.created) != 0 {
		t.Fatal("no container may start without a usable workspace")
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("nothing should have been created")
	}
	if !workspaceOwnerOK(0) || !workspaceOwnerOK(1000) || workspaceOwnerOK(1) {
		t.Fatal("only root and the sandbox UID may own workspaces")
	}
}

func TestDriver_PruneOrphansKeepsOnlyKeptWorkspaces(t *testing.T) {
	d, _, root := volumeDriver(t)
	keep, orphan := "sbx_aaaaaaaaaaaaaaaaaaaa", "sbx_bbbbbbbbbbbbbbbbbbbb"
	for _, id := range []string{keep, orphan, "not-a-sandbox"} {
		if err := os.MkdirAll(filepath.Join(root, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.PruneOrphans(context.Background(), map[string]bool{keep: true}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{keep: true, orphan: false, "not-a-sandbox": true} {
		_, err := os.Stat(filepath.Join(root, id))
		if (err == nil) != want {
			t.Errorf("%s present=%v want %v", id, err == nil, want)
		}
	}
	tmpfs, _ := newDriver(t)
	if err := tmpfs.PruneOrphans(context.Background(), nil); err != nil {
		t.Fatalf("a tmpfs-only driver has nothing to prune: %v", err)
	}
}

func TestDriver_CheckProvesTheWorkspaceDirectory(t *testing.T) {
	d, _, root := volumeDriver(t)
	detail, err := d.Check(context.Background())
	if err != nil || !strings.Contains(detail, "volume workspaces in "+root) {
		t.Fatalf("%q %v", detail, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("the probe must clean up: %v", entries)
	}
	geteuid = func() int { return 7 }
	if _, err = d.Check(context.Background()); err == nil {
		t.Fatal("doctor must report a user that cannot own workspaces")
	}
}
