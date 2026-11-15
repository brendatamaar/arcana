package containerd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/brendatamaar/arcana/internal/runtime"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "sbx-exec")
	if err := os.WriteFile(helper, []byte("x"), 0o755); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	c := Config{InstallID: "install", HelperPath: helper}
	c.applyDefaults()
	return c
}

func testSpec() runtime.Spec {
	return runtime.Spec{
		SandboxID: "sbx_abcdefghijklmnopqrst", Image: "arcana-base:local", WorkspaceBackend: "tmpfs", NetworkMode: "none",
		Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 256, DiskMB: 64, PIDs: 128},
		Env:       map[string]string{"B": "2", "A": "1"}, Labels: map[string]string{"task": "x"},
	}
}

func TestConfigValidate_RuncNeedsOptInAndSettingsAreChecked(t *testing.T) {
	good := testConfig(t)
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if good.shimName() != "io.containerd.runsc.v1" || good.Namespace != "arcana" || good.FIFODir != "/run/containerd/fifo" {
		t.Fatalf("%+v", good)
	}
	for name, mutate := range map[string]func(*Config){
		"runc without opt-in": func(c *Config) { c.OCIRuntime = "runc" },
		"unknown runtime":     func(c *Config) { c.OCIRuntime = "kata" },
		"no install id":       func(c *Config) { c.InstallID = "" },
		"bad namespace":       func(c *Config) { c.Namespace = "Bad Name" },
		"relative address":    func(c *Config) { c.Address = "containerd.sock" },
		"unclean address":     func(c *Config) { c.Address = "/run/../etc/x.sock" },
		"relative helper":     func(c *Config) { c.HelperPath = "sbx-exec" },
		"missing helper":      func(c *Config) { c.HelperPath = "/nonexistent/sbx-exec" },
		"relative fifo dir":   func(c *Config) { c.FIFODir = "fifo" },
	} {
		c := testConfig(t)
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	runc := testConfig(t)
	runc.OCIRuntime, runc.AllowInsecureRuntime = "runc", true
	if err := runc.Validate(); err != nil || runc.shimName() != "io.containerd.runc.v2" {
		t.Fatalf("explicit runc opt-in: %v", err)
	}
}

func TestPlanContainer_RejectsWhatItCannotProvideSafely(t *testing.T) {
	cfg := testConfig(t)
	for name, mutate := range map[string]func(*runtime.Spec){
		"bad sandbox id":  func(s *runtime.Spec) { s.SandboxID = "sbx_short" },
		"empty image":     func(s *runtime.Spec) { s.Image = "" },
		"zero cpus":       func(s *runtime.Spec) { s.Resources.CPUs = 0 },
		"no memory":       func(s *runtime.Spec) { s.Resources.MemoryMB = 0 },
		"no disk":         func(s *runtime.Spec) { s.Resources.DiskMB = 0 },
		"no pids":         func(s *runtime.Spec) { s.Resources.PIDs = 0 },
		"relative cwd":    func(s *runtime.Spec) { s.Workdir = "workspace" },
		"env with equals": func(s *runtime.Spec) { s.Env = map[string]string{"A=B": "v"} },
		"arcana label":    func(s *runtime.Spec) { s.Labels = map[string]string{"arcana.managed": "false"} },
	} {
		spec := testSpec()
		mutate(&spec)
		if _, err := planContainer(cfg, spec); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, mutate := range map[string]func(*runtime.Spec){
		"allowlist network": func(s *runtime.Spec) { s.NetworkMode = "allowlist" },
		"volume workspace":  func(s *runtime.Spec) { s.WorkspaceBackend = "volume" },
		"default workspace": func(s *runtime.Spec) { s.WorkspaceBackend = "" },
	} {
		spec := testSpec()
		mutate(&spec)
		var unsupported runtime.Unsupported
		if _, err := planContainer(cfg, spec); !errors.As(err, &unsupported) {
			t.Errorf("%s: want Unsupported, got %v", name, err)
		}
	}
}

func TestPlanContainer_LabelsEnvAndNames(t *testing.T) {
	p, err := planContainer(testConfig(t), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "sbx-sbx_abcdefghijklmnopqrst" || p.Workdir != "/workspace" || !slices.Equal(p.Env, []string{"A=1", "B=2"}) {
		t.Fatalf("%+v", p)
	}
	for k, v := range map[string]string{"arcana.managed": "true", "arcana.instance": "install", "arcana.sandbox_id": "sbx_abcdefghijklmnopqrst", "task": "x"} {
		if p.Labels[k] != v {
			t.Errorf("label %s=%q", k, p.Labels[k])
		}
	}
	spec := testSpec()
	spec.PoolTemplate = "base"
	if p, _ = planContainer(testConfig(t), spec); p.Labels["arcana.pool"] != "base" {
		t.Fatal("pool label missing")
	}
	if sandbox, ok := sandboxFromContainer(p.ID); !ok || sandbox != "sbx_abcdefghijklmnopqrst" {
		t.Fatal(sandbox, ok)
	}
	for _, bad := range []string{"", "sbx-", "sbx-other", "x-sbx_abcdefghijklmnopqrst", "sbx-sbx_ABCDEFGHIJKLMNOPQRST"} {
		if _, ok := sandboxFromContainer(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The hardening of FR-RL-1 and FR-RL-2 as the OCI spec the shim would receive.
func TestHardening_ProducesTheSameGuaranteesAsTheDockerDriver(t *testing.T) {
	cfg := testConfig(t)
	p, err := planContainer(cfg, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	spec, err := applySpec(context.Background(), p.SpecOpts)
	if err != nil {
		t.Fatal(err)
	}
	proc := spec.Process
	if proc.User.UID != 1000 || proc.User.GID != 1000 || !proc.NoNewPrivileges || proc.Cwd != "/workspace" {
		t.Fatalf("process %+v", proc)
	}
	if c := proc.Capabilities; c == nil || len(c.Bounding)+len(c.Effective)+len(c.Permitted)+len(c.Inheritable)+len(c.Ambient) != 0 {
		t.Fatalf("capabilities must be empty: %+v", c)
	}
	if !spec.Root.Readonly {
		t.Fatal("root filesystem must be read-only")
	}
	if !slices.ContainsFunc(proc.Rlimits, func(r specs.POSIXRlimit) bool { return r.Type == "RLIMIT_NOFILE" && r.Hard == 1024 && r.Soft == 1024 }) {
		t.Fatalf("rlimits %+v", proc.Rlimits)
	}
	res := spec.Linux.Resources
	if *res.Memory.Limit != 256*1024*1024 || *res.Memory.Swap != 256*1024*1024 {
		t.Fatalf("memory %+v swap %+v", *res.Memory.Limit, *res.Memory.Swap)
	}
	if *res.CPU.Quota != 50000 || *res.CPU.Period != 100000 || *res.Pids.Limit != 128 {
		t.Fatalf("cpu %+v pids %+v", res.CPU, res.Pids)
	}
	byDestination := map[string]specs.Mount{}
	for _, m := range spec.Mounts {
		byDestination[m.Destination] = m
	}
	for _, dest := range []string{"/tmp", "/run/sbx", "/workspace"} {
		m := byDestination[dest]
		if m.Type != "tmpfs" || !slices.Contains(m.Options, "nosuid") || !slices.Contains(m.Options, "nodev") || !slices.Contains(m.Options, "uid=1000") {
			t.Errorf("%s: %+v", dest, m)
		}
	}
	if !slices.Contains(byDestination["/workspace"].Options, "size=64m") || slices.Contains(byDestination["/workspace"].Options, "noexec") {
		t.Errorf("workspace %+v", byDestination["/workspace"])
	}
	if !slices.Contains(byDestination["/run/sbx"].Options, "noexec") {
		t.Error("/run/sbx must not allow execution")
	}
	helper := byDestination["/opt/arcana/sbx-exec"]
	if helper.Type != "bind" || helper.Source != cfg.HelperPath || !slices.Contains(helper.Options, "ro") {
		t.Fatalf("helper mount %+v", helper)
	}
	// The only host path in the spec is the read-only helper.
	for _, m := range spec.Mounts {
		if m.Type == "bind" && m.Destination != "/opt/arcana/sbx-exec" && !strings.HasPrefix(m.Source, "/proc") && !strings.HasPrefix(m.Source, "/sys") && !strings.HasPrefix(m.Source, "/dev") {
			t.Errorf("unexpected host bind mount %+v", m)
		}
	}
}

type fakeSession struct{}

func (fakeSession) Stdin() io.WriteCloser                          { return nil }
func (fakeSession) Stdout() io.Reader                              { return nil }
func (fakeSession) Stderr() io.Reader                              { return nil }
func (fakeSession) Resize(uint16, uint16) error                    { return nil }
func (fakeSession) Wait(context.Context) (runtime.ExitInfo, error) { return runtime.ExitInfo{}, nil }

type fakeEngine struct {
	mu         sync.Mutex
	created    []plan
	deleted    []string
	containers []containerInfo
	failCreate error
	execs      []runtime.ExecRequest
}

func (f *fakeEngine) Create(_ context.Context, p plan) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate != nil {
		return f.failCreate
	}
	f.created = append(f.created, p)
	f.containers = append(f.containers, containerInfo{ID: p.ID, Labels: p.Labels})
	return nil
}

func (f *fakeEngine) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	f.containers = slices.DeleteFunc(f.containers, func(c containerInfo) bool { return c.ID == id })
	return nil
}

func (f *fakeEngine) Exec(_ context.Context, _ string, r runtime.ExecRequest) (runtime.ExecSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs = append(f.execs, r)
	return fakeSession{}, nil
}

func (f *fakeEngine) List(context.Context) ([]containerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.containers), nil
}

func (*fakeEngine) Stats(context.Context, string) (runtime.Stats, error) {
	return runtime.Stats{MemoryUsageBytes: 7}, nil
}
func (*fakeEngine) Close() error { return nil }

func newDriver(t *testing.T) (*Driver, *fakeEngine) {
	t.Helper()
	f := &fakeEngine{}
	return &Driver{cfg: testConfig(t), engine: f, gate: make(chan struct{}, 1)}, f
}

func TestDriver_CreateExecStatsListDestroy(t *testing.T) {
	d, f := newDriver(t)
	ctx := context.Background()
	if d.Name() != "containerd/runsc" || !d.Capabilities().SupportsTTY {
		t.Fatal(d.Name(), d.Capabilities())
	}
	inst, err := d.Create(ctx, testSpec())
	if err != nil || inst.ID != "sbx-sbx_abcdefghijklmnopqrst" || inst.Labels["arcana.managed"] != "true" {
		t.Fatalf("%+v %v", inst, err)
	}
	if _, err = d.Exec(ctx, inst.ID, runtime.ExecRequest{Cmd: []string{"true"}}); err != nil || len(f.execs) != 1 {
		t.Fatal(err)
	}
	if _, err = d.Exec(ctx, inst.ID, runtime.ExecRequest{}); !errors.As(err, &runtime.Invalid{}) {
		t.Fatalf("empty command: %v", err)
	}
	if st, err := d.Stats(ctx, inst.ID); err != nil || st.MemoryUsageBytes != 7 {
		t.Fatal(st, err)
	}
	listed, err := d.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].SandboxID != "sbx_abcdefghijklmnopqrst" {
		t.Fatalf("%+v %v", listed, err)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil || len(f.deleted) != 1 {
		t.Fatal(err)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("Destroy must be idempotent: %v", err)
	}
	for _, bad := range []string{"", "../x", "sbx-other"} {
		if err = d.Destroy(ctx, bad); !errors.As(err, &runtime.NotFound{}) {
			t.Errorf("Destroy(%q): %v", bad, err)
		}
		if _, err = d.Exec(ctx, bad, runtime.ExecRequest{Cmd: []string{"x"}}); !errors.As(err, &runtime.NotFound{}) {
			t.Errorf("Exec(%q): %v", bad, err)
		}
		if _, err = d.Stats(ctx, bad); !errors.As(err, &runtime.NotFound{}) {
			t.Errorf("Stats(%q): %v", bad, err)
		}
	}
}

func TestDriver_FailedCreateRemovesWhatItStartedAndNeverStartsInvalidSpecs(t *testing.T) {
	d, f := newDriver(t)
	f.failCreate = errors.New("shim refused")
	if _, err := d.Create(context.Background(), testSpec()); err == nil || !strings.Contains(err.Error(), "shim refused") {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "sbx-sbx_abcdefghijklmnopqrst" {
		t.Fatalf("a failed create must clean up: %v", f.deleted)
	}
	bad := testSpec()
	bad.NetworkMode = "allowlist"
	f.failCreate = nil
	if _, err := d.Create(context.Background(), bad); !errors.As(err, &runtime.Unsupported{}) || len(f.created) != 0 {
		t.Fatalf("%v %d", err, len(f.created))
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Create(cancelled, testSpec()); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled request must not create: %v", err)
	}
}

func TestDriver_ListIgnoresOtherInstallationsAndUnlabelledContainers(t *testing.T) {
	d, f := newDriver(t)
	own := map[string]string{"arcana.managed": "true", "arcana.instance": "install", "arcana.sandbox_id": "sbx_abcdefghijklmnopqrst"}
	f.containers = []containerInfo{
		{ID: "sbx-sbx_abcdefghijklmnopqrst", Labels: own, Stopped: true},
		{ID: "sbx-sbx_bbbbbbbbbbbbbbbbbbbb", Labels: map[string]string{"arcana.managed": "true", "arcana.instance": "someone-else", "arcana.sandbox_id": "sbx_bbbbbbbbbbbbbbbbbbbb"}},
		{ID: "sbx-sbx_cccccccccccccccccccc", Labels: map[string]string{"arcana.instance": "install", "arcana.sandbox_id": "sbx_cccccccccccccccccccc"}},
		{ID: "sbx-sbx_dddddddddddddddddddd", Labels: map[string]string{"arcana.managed": "true", "arcana.instance": "install", "arcana.sandbox_id": "sbx_other"}},
		{ID: "unrelated", Labels: own},
	}
	listed, err := d.List(context.Background())
	if err != nil || len(listed) != 1 || !listed[0].Stopped {
		t.Fatalf("%+v %v", listed, err)
	}
}
