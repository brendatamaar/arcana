package firecracker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// Driver runs each sandbox in a Firecracker microVM and reaches it through envd.
type Driver struct {
	cfg      Config
	launcher launcher
	dial     func(ctx context.Context, uds string, port uint32) (net.Conn, error)
	mkfs     func(ctx context.Context, path string, sizeMiB int, uid, gid int) error

	mu     sync.Mutex
	vms    map[string]*vm
	closed bool
	wg     sync.WaitGroup
}

var _ runtime.Driver = (*Driver)(nil)

type vm struct {
	meta   vmMeta
	env    map[string]string // sandbox environment; held in memory only, never persisted
	layout layout
	proc   process // nil for VMs adopted from a previous run
	conn   *grpc.ClientConn
	client envdpb.EnvdClient
	cancel context.CancelFunc // cancels every session of this VM
	ctx    context.Context
}

// vmMeta is persisted so a restarted Arcana can find, list and destroy its VMs.
type vmMeta struct {
	InstallID string            `json:"install_id"`
	VMID      string            `json:"vm_id"`
	SandboxID string            `json:"sandbox_id"`
	Labels    map[string]string `json:"labels"`
	Process   procIdentity      `json:"process"`
}

// New validates the configuration. It does not touch /dev/kvm; Create fails if the host
// cannot start a VM.
func New(cfg Config) (*Driver, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}
	return &Driver{cfg: cfg, launcher: execLauncher{}, dial: dialGuest, mkfs: makeWorkspace(cfg), vms: make(map[string]*vm)}, nil
}

// Name identifies the driver and whether the VMM is jailed.
func (d *Driver) Name() string {
	if d.cfg.JailerBinary == "" {
		return "firecracker/unjailed"
	}
	return "firecracker"
}

// Capabilities reports terminal support through envd; memory snapshots are not implemented yet.
func (*Driver) Capabilities() runtime.Capabilities { return runtime.Capabilities{SupportsTTY: true} }

// Close cancels every session and releases connections. VMs keep running so a restart
// can reconcile them, exactly as with the Docker driver.
func (d *Driver) Close() error {
	d.mu.Lock()
	d.closed = true
	vms := make([]*vm, 0, len(d.vms))
	for _, v := range d.vms {
		vms = append(vms, v)
	}
	d.mu.Unlock()
	var errs []error
	for _, v := range vms {
		v.cancel()
		if v.conn != nil {
			errs = append(errs, v.conn.Close())
		}
	}
	d.wg.Wait()
	return errors.Join(errs...)
}

func (d *Driver) writeMeta(v *vm) error {
	data, err := json.Marshal(v.meta)
	if err != nil {
		return err
	}
	tmp := filepath.Join(v.layout.VMDir, "meta.json.tmp")
	if err = os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(v.layout.VMDir, "meta.json"))
}

// Create plans, launches and boots a VM and returns once envd answers on vsock.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Instance, error) {
	plan, err := planVM(d.cfg, spec)
	if err != nil {
		return runtime.Instance{}, err
	}
	for name, value := range spec.Labels {
		if len(name) > 128 || len(value) > 1024 {
			return runtime.Instance{}, runtime.Invalid{Field: "labels"}
		}
	}
	if info, statErr := os.Stat(plan.Rootfs); statErr != nil || !info.Mode().IsRegular() {
		return runtime.Instance{}, errors.Join(runtime.Invalid{Field: "image"}, fmt.Errorf("no rootfs image for %q (expected %s)", spec.Image, plan.Rootfs))
	}
	l := layoutFor(d.cfg, plan.VMID)
	v := &vm{layout: l, env: maps.Clone(spec.Env), meta: vmMeta{InstallID: d.cfg.InstallID, VMID: plan.VMID, SandboxID: spec.SandboxID, Labels: maps.Clone(spec.Labels)}}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return runtime.Instance{}, errors.New("firecracker driver is closed")
	}
	if _, exists := d.vms[plan.VMID]; exists {
		d.mu.Unlock()
		return runtime.Instance{}, runtime.Invalid{Field: "sandbox already exists"}
	}
	v.ctx, v.cancel = context.WithCancel(context.Background())
	d.vms[plan.VMID] = v
	d.mu.Unlock()
	if err = d.boot(ctx, v, plan); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return runtime.Instance{}, errors.Join(err, d.Destroy(cleanup, plan.VMID))
	}
	return runtime.Instance{ID: plan.VMID, SandboxID: spec.SandboxID, Labels: maps.Clone(spec.Labels)}, nil
}

func (d *Driver) boot(ctx context.Context, v *vm, plan vmPlan) error {
	if err := os.MkdirAll(v.layout.VMDir, 0o700); err != nil {
		return fmt.Errorf("creating VM directory: %w", err)
	}
	if err := d.writeMeta(v); err != nil {
		return err
	}
	argv := launchArgv(d.cfg, plan, v.layout)
	proc, err := d.launcher.Launch(ctx, argv)
	if err != nil {
		return err
	}
	v.proc = proc
	v.meta.Process = proc.Identity()
	if err = d.writeMeta(v); err != nil {
		return err
	}
	bootCtx, cancel := context.WithTimeout(ctx, d.cfg.BootTimeout)
	defer cancel()
	if err = waitForFile(bootCtx, v.layout.APISock, proc, d.cfg.BootTimeout); err != nil {
		return err
	}
	if err = d.stage(bootCtx, plan, v.layout); err != nil {
		return err
	}
	if err = newAPIClient(v.layout.APISock).configure(bootCtx, plan, v.layout); err != nil {
		return fmt.Errorf("configuring VM (%s): %w", proc.Tail(), err)
	}
	if err = d.connect(v); err != nil {
		return err
	}
	return d.waitReady(bootCtx, v, proc)
}

// connect creates the lazy gRPC connection to the guest's envd.
func (d *Driver) connect(v *vm) error {
	conn, err := grpc.NewClient("passthrough:///"+v.meta.VMID, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return d.dial(ctx, v.layout.VsockUDS, d.cfg.VsockPort)
		}))
	if err != nil {
		return err
	}
	v.conn, v.client = conn, envdpb.NewEnvdClient(conn)
	return nil
}

// stage puts the files the VMM needs in place once its API socket exists. Jailed VMs
// get hard links to the kernel and the shared read-only rootfs; the workspace image is
// created fresh, so nothing from another sandbox is ever shared writable.
func (d *Driver) stage(ctx context.Context, plan vmPlan, l layout) error {
	uid, gid := os.Getuid(), os.Getgid()
	if l.Jailed {
		uid, gid = d.cfg.JailerUID, d.cfg.JailerGID
		for _, link := range [][2]string{{d.cfg.KernelImage, l.KernelHost}, {plan.Rootfs, l.RootfsHost}} {
			if err := os.Link(link[0], link[1]); err != nil {
				return fmt.Errorf("linking %s into the jail (the jail must share a filesystem with the kernel and rootfs): %w", filepath.Base(link[0]), err)
			}
		}
	}
	if plan.TmpfsSpace {
		return nil
	}
	return d.mkfs(ctx, l.WorkspaceHost, plan.DiskMiB, uid, gid)
}

func (d *Driver) waitReady(ctx context.Context, v *vm, p process) error {
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := v.client.Info(attempt, &envdpb.InfoRequest{})
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-p.Done():
			return fmt.Errorf("VM exited during boot: %s", p.Tail())
		case <-ctx.Done():
			return fmt.Errorf("envd did not become ready: %w (last VMM output: %s)", errors.Join(err, ctx.Err()), p.Tail())
		case <-poll.C:
		}
	}
}

// find returns a managed VM, adopting a running one from disk after a restart. An
// adopted VM has no sandbox environment: values are never persisted.
func (d *Driver) find(id string) (*vm, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.vms[id]; ok {
		return v, true
	}
	if d.closed || !validVMID(id) {
		return nil, false
	}
	meta, err := d.readMeta(id)
	if err != nil || !meta.Process.alive() {
		return nil, false
	}
	v := &vm{meta: meta, layout: layoutFor(d.cfg, id)}
	v.ctx, v.cancel = context.WithCancel(context.Background())
	if d.connect(v) != nil {
		v.cancel()
		return nil, false
	}
	d.vms[id] = v
	return v, true
}

// Destroy kills the VMM and removes everything the VM owned. It is idempotent and
// treats a missing VM as success.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	if !validVMID(id) {
		return runtime.NotFound{ID: id}
	}
	d.mu.Lock()
	v, ok := d.vms[id]
	d.mu.Unlock()
	if !ok {
		meta, err := d.readMeta(id)
		if errors.Is(err, os.ErrNotExist) {
			return d.removeFiles(layoutFor(d.cfg, id))
		}
		if err != nil {
			return err
		}
		v = &vm{meta: meta, layout: layoutFor(d.cfg, id)}
	}
	if v.cancel != nil {
		v.cancel()
	}
	var errs []error
	if v.conn != nil {
		errs = append(errs, v.conn.Close())
	}
	errs = append(errs, v.meta.Process.kill())
	if err := errors.Join(errs...); err != nil {
		return err // keep the record so a retry can find the VM
	}
	if err := d.removeFiles(v.layout); err != nil {
		return err
	}
	d.mu.Lock()
	delete(d.vms, id)
	d.mu.Unlock()
	return ctx.Err()
}

func (d *Driver) removeFiles(l layout) error {
	var errs []error
	if l.Jailed {
		errs = append(errs, os.RemoveAll(l.jailDir()))
		// The jailer's cgroup directory is empty once the VMM is gone.
		_ = os.Remove(filepath.Join("/sys/fs/cgroup/firecracker", filepath.Base(l.jailDir())))
	}
	errs = append(errs, os.RemoveAll(l.VMDir))
	return errors.Join(errs...)
}

func validVMID(id string) bool {
	if len(id) < 5 || len(id) > 64 || id[:4] != "sbx-" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func (d *Driver) readMeta(id string) (vmMeta, error) {
	data, err := os.ReadFile(filepath.Join(d.cfg.StateDir, id, "meta.json")) // #nosec G304 -- id validated by validVMID
	if err != nil {
		return vmMeta{}, err
	}
	var meta vmMeta
	if err = json.Unmarshal(data, &meta); err != nil || meta.InstallID != d.cfg.InstallID || meta.VMID != id {
		return vmMeta{}, errors.New("invalid or foreign VM metadata")
	}
	return meta, nil
}

// List reports this installation's VMs from disk, which also finds ones started by a
// previous Arcana process.
func (d *Driver) List(_ context.Context) ([]runtime.Instance, error) {
	entries, err := os.ReadDir(d.cfg.StateDir)
	if err != nil {
		return nil, err
	}
	var out []runtime.Instance
	for _, entry := range entries {
		if !entry.IsDir() || !validVMID(entry.Name()) {
			continue
		}
		meta, err := d.readMeta(entry.Name())
		if err != nil {
			continue
		}
		out = append(out, runtime.Instance{ID: meta.VMID, SandboxID: meta.SandboxID, Labels: maps.Clone(meta.Labels), Stopped: !meta.Process.alive()})
	}
	return out, nil
}

// Stats reads the VMM's CPU time and resident memory.
func (d *Driver) Stats(_ context.Context, id string) (runtime.Stats, error) {
	if !validVMID(id) {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	meta, err := d.readMeta(id)
	if err != nil {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	return meta.Process.stats()
}

// Exec runs argv in the guest through envd and adapts the stream to an ExecSession.
func (d *Driver) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	v, ok := d.find(id)
	if !ok || v.client == nil {
		return nil, runtime.NotFound{ID: id}
	}
	if len(request.Cmd) == 0 {
		return nil, runtime.Invalid{Field: "cmd"}
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	start := &envdpb.ExecStart{ExecId: "d_" + hex.EncodeToString(entropy[:]), Argv: request.Cmd, Cwd: request.Cwd, TimeoutSeconds: 3600, Env: mergeEnv(v, request.Env)}
	if request.TTY {
		start.Tty, start.Cols, start.Rows = true, uint32(request.Cols), uint32(request.Rows)
		if start.Cols == 0 || start.Rows == 0 {
			start.Cols, start.Rows = 80, 24
		}
	}
	sctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(v.ctx, cancel)
	stream, err := v.client.Exec(sctx)
	if err == nil {
		err = stream.Send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Start{Start: start}})
	}
	var first *envdpb.ExecServerMessage
	if err == nil {
		first, err = stream.Recv()
	}
	if err == nil && first.GetStarted() == nil {
		err = errors.New("envd did not acknowledge the exec")
	}
	if err != nil {
		stop()
		cancel()
		if status.Code(err) == codes.InvalidArgument {
			return nil, errors.Join(runtime.Invalid{Field: "exec request"}, err)
		}
		return nil, fmt.Errorf("starting exec in the guest: %w", err)
	}
	d.wg.Add(1)
	session := newExecSession(stream, func() { stop(); cancel() }, request.TTY)
	go func() { <-session.done; d.wg.Done() }()
	return session, nil
}

// mergeEnv applies the sandbox environment under the per-exec one; envd then adds its
// own base environment.
func mergeEnv(v *vm, perExec map[string]string) map[string]string {
	out := make(map[string]string, len(v.env)+len(perExec))
	maps.Copy(out, v.env)
	maps.Copy(out, perExec)
	return out
}

// makeWorkspace returns the function that creates a sparse ext4 image owned by the
// sandbox identity. It only creates an empty filesystem; no workspace content is read.
func makeWorkspace(cfg Config) func(context.Context, string, int, int, int) error {
	return func(ctx context.Context, path string, sizeMiB int, uid, gid int) error {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- path is inside the VM's own directory
		if err != nil {
			return err
		}
		if err = file.Truncate(int64(sizeMiB) * 1024 * 1024); err != nil {
			_ = file.Close()
			return err
		}
		if err = file.Close(); err != nil {
			return err
		}
		args := []string{"-q", "-t", "ext4", "-F", "-m", "0", "-O", "^has_journal", "-L", "workspace", "-E", "root_owner=1000:1000", path}
		// #nosec G204 -- fixed binary from configuration, fixed flags, a path this driver chose
		if output, runErr := exec.CommandContext(ctx, cfg.MkfsBinary, args...).CombinedOutput(); runErr != nil {
			return fmt.Errorf("creating workspace filesystem: %w: %s", runErr, output)
		}
		return os.Chown(path, uid, gid)
	}
}

var _ runtime.Checker = (*Driver)(nil)

// Check verifies what a VM needs from the host: /dev/kvm, the Firecracker binary, the jailer (and root to
// run it), and the kernel image.
func (d *Driver) Check(_ context.Context) (string, error) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return "", errors.New("/dev/kvm is absent or inaccessible; Firecracker needs hardware virtualization")
	}
	files := map[string]string{"firecracker binary": d.cfg.FirecrackerBinary, "kernel image": d.cfg.KernelImage}
	if d.cfg.JailerBinary != "" {
		files["jailer binary"] = d.cfg.JailerBinary
	}
	for what, path := range files {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s %s is missing", what, path)
		}
	}
	if d.cfg.JailerBinary != "" && os.Geteuid() != 0 {
		return "", errors.New("the jailer needs Arcana to run as root; run as root, or set allow_unjailed for development")
	}
	if d.cfg.JailerBinary == "" {
		return "/dev/kvm present; Firecracker UNJAILED (development only)", nil
	}
	return "/dev/kvm present; Firecracker and jailer found", nil
}

// HasImage reports whether a rootfs image has been built for the template image.
func (d *Driver) HasImage(_ context.Context, image string) bool {
	info, err := os.Stat(filepath.Join(d.cfg.RootfsDir, imageFile(image)))
	return err == nil && info.Mode().IsRegular()
}
