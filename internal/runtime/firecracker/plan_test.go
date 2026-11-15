package firecracker

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime"
)

func testConfig() Config {
	c := Config{
		FirecrackerBinary: "/usr/bin/firecracker", JailerBinary: "/usr/bin/jailer", KernelImage: "/var/lib/arcana/fc/vmlinux",
		RootfsDir: "/var/lib/arcana/fc/rootfs", StateDir: "/var/lib/arcana/fc/state", JailerUID: 10001, JailerGID: 10001, InstallID: "install",
	}
	c.applyDefaults()
	return c
}

func testSpec() runtime.Spec {
	return runtime.Spec{
		SandboxID: "sbx_abcdefghijklmnopqrst", Image: "arcana-python:local", WorkspaceBackend: "volume", NetworkMode: "none",
		Resources: runtime.Resources{CPUs: 1.5, MemoryMB: 512, DiskMB: 2048, PIDs: 256},
	}
}

func TestPlanVM_DerivesTheVMShapeAndLimits(t *testing.T) {
	plan, err := planVM(testConfig(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if plan.VMID != "sbx-sbx-abcdefghijklmnopqrst" || plan.VCPUs != 2 || plan.MemoryMiB != 512 || plan.DiskMiB != 2048 {
		t.Fatalf("%+v", plan)
	}
	if plan.Rootfs != "/var/lib/arcana/fc/rootfs/arcana-python_local.ext4" {
		t.Fatalf("unexpected rootfs %s", plan.Rootfs)
	}
	want := []string{"cpu.max=150000 100000", "memory.max=671088640", "memory.swap.max=0", "pids.max=66"}
	if strings.Join(plan.Cgroup, ";") != strings.Join(want, ";") {
		t.Fatalf("cgroup limits %v, want %v", plan.Cgroup, want)
	}
}

func TestPlanVM_FractionalCPUStillGetsOneVCPUWithAQuota(t *testing.T) {
	spec := testSpec()
	spec.Resources.CPUs = 0.25
	plan, err := planVM(testConfig(), spec)
	if err != nil || plan.VCPUs != 1 || plan.Cgroup[0] != "cpu.max=25000 100000" {
		t.Fatalf("%+v %v", plan, err)
	}
}

func TestPlanVM_RejectsInvalidAndUnsupportedSpecs(t *testing.T) {
	for name, mutate := range map[string]func(*runtime.Spec){
		"bad sandbox id":   func(s *runtime.Spec) { s.SandboxID = "sbx_short" },
		"path in id":       func(s *runtime.Spec) { s.SandboxID = "sbx_../../../../etc/passwd" },
		"empty image":      func(s *runtime.Spec) { s.Image = "" },
		"huge image":       func(s *runtime.Spec) { s.Image = strings.Repeat("a", 1025) },
		"bad workspace":    func(s *runtime.Spec) { s.WorkspaceBackend = "nfs" },
		"zero cpus":        func(s *runtime.Spec) { s.Resources.CPUs = 0 },
		"too many cpus":    func(s *runtime.Spec) { s.Resources.CPUs = 33 },
		"tiny memory":      func(s *runtime.Spec) { s.Resources.MemoryMB = 64 },
		"huge memory":      func(s *runtime.Spec) { s.Resources.MemoryMB = 1<<20 + 1 },
		"no disk":          func(s *runtime.Spec) { s.Resources.DiskMB = 0 },
		"no pids":          func(s *runtime.Spec) { s.Resources.PIDs = 0 },
		"unknown net mode": func(s *runtime.Spec) { s.NetworkMode = "open" },
	} {
		spec := testSpec()
		mutate(&spec)
		if _, err := planVM(testConfig(), spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	spec := testSpec()
	spec.NetworkMode = "allowlist"
	var unsupported runtime.Unsupported
	if _, err := planVM(testConfig(), spec); !errors.As(err, &unsupported) {
		t.Fatalf("allowlist must be unsupported, got %v", err)
	}
}

func TestImageFile_NeverProducesPathsOutsideTheRootfsDirectory(t *testing.T) {
	for _, image := range []string{"../../etc/passwd", "a/b:c@sha256:abc", "UPPER", "with space", "\x00", "ghcr.io/x/y:1"} {
		name := imageFile(image)
		if strings.ContainsAny(name, "/\x00 ") || filepath.Dir(filepath.Join("/rootfs", name)) != "/rootfs" || !strings.HasSuffix(name, ".ext4") {
			t.Errorf("%q -> %q", image, name)
		}
	}
}

func TestKernelArgs_AreHardenedAndOperatorArgsCannotOverrideArcanaSettings(t *testing.T) {
	cfg := testConfig()
	cfg.KernelArgs = "loglevel=3 arcana.pids=999999 arcana.workspace=/dev/vdz"
	plan, err := planVM(cfg, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	args := plan.KernelArgs
	for _, want := range []string{"ro", "root=/dev/vda", "nomodules", "init=/sbin/arcana-init", "arcana.pids=256", "arcana.workspace=/dev/vdb", "arcana.vsock_port=1024", "loglevel=3"} {
		if !strings.Contains(" "+args+" ", " "+want+" ") {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if strings.Contains(args, "999999") || strings.Contains(args, "vdz") {
		t.Fatalf("operator args overrode an arcana setting: %s", args)
	}
	spec := testSpec()
	spec.WorkspaceBackend = "tmpfs"
	plan, _ = planVM(cfg, spec)
	if !strings.Contains(plan.KernelArgs, "arcana.workspace=tmpfs:2048m") {
		t.Fatal(plan.KernelArgs)
	}
}

func TestConfigValidate_RequiresTheJailerUnlessExplicitlyDisabled(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"no jailer":            func(c *Config) { c.JailerBinary = "" },
		"root jailer uid":      func(c *Config) { c.JailerUID = 0 },
		"root jailer gid":      func(c *Config) { c.JailerGID = 0 },
		"relative firecracker": func(c *Config) { c.FirecrackerBinary = "firecracker" },
		"unclean kernel":       func(c *Config) { c.KernelImage = "/var/../etc/vmlinux" },
		"relative rootfs dir":  func(c *Config) { c.RootfsDir = "rootfs" },
		"relative jailer":      func(c *Config) { c.JailerBinary = "jailer" },
		"no install id":        func(c *Config) { c.InstallID = "" },
		"zero vsock":           func(c *Config) { c.VsockPort = 0 },
		"short boot timeout":   func(c *Config) { c.BootTimeout = time.Millisecond },
		"long kernel args":     func(c *Config) { c.KernelArgs = strings.Repeat("a", 513) },
	} {
		cfg := testConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dev := testConfig()
	dev.JailerBinary, dev.AllowUnjailed = "", true
	if err := dev.Validate(); err != nil {
		t.Fatalf("explicit unjailed development mode rejected: %v", err)
	}
}

func TestLaunchArgv_JailedRunsAsAnUnprivilegedUserWithCgroupLimits(t *testing.T) {
	cfg := testConfig()
	plan, _ := planVM(cfg, testSpec())
	l := layoutFor(cfg, plan.VMID)
	got := strings.Join(launchArgv(cfg, plan, l), " ")
	want := "/usr/bin/jailer --id sbx-sbx-abcdefghijklmnopqrst --exec-file /usr/bin/firecracker --uid 10001 --gid 10001 " +
		"--chroot-base-dir /var/lib/arcana/fc/state/jail --cgroup-version 2 --resource-limit no-file=2048 " +
		"--cgroup cpu.max=150000 100000 --cgroup memory.max=671088640 --cgroup memory.swap.max=0 --cgroup pids.max=66"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if l.APISock != "/var/lib/arcana/fc/state/jail/firecracker/sbx-sbx-abcdefghijklmnopqrst/root/run/firecracker.socket" {
		t.Fatal(l.APISock)
	}
	if l.Kernel != "/kernel" || l.Rootfs != "/rootfs.ext4" || l.Workspace != "/workspace.ext4" || l.Vsock != "/v.sock" {
		t.Fatalf("jailed paths must be chroot-relative: %+v", l)
	}
}

func TestLaunchArgv_UnjailedIsDevelopmentOnly(t *testing.T) {
	cfg := testConfig()
	cfg.JailerBinary, cfg.AllowUnjailed = "", true
	plan, _ := planVM(cfg, testSpec())
	l := layoutFor(cfg, plan.VMID)
	argv := launchArgv(cfg, plan, l)
	if argv[0] != "/usr/bin/firecracker" || argv[1] != "--api-sock" || l.Jailed {
		t.Fatalf("%v %+v", argv, l)
	}
}
