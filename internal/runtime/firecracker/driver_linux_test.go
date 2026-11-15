package firecracker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/envd"
	"github.com/brendatamaar/arcana/internal/runtime"
)

var envdBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fc-envd")
	if err != nil {
		panic(err)
	}
	envdBinary = filepath.Join(dir, "envd")
	build := exec.Command("go", "build", "-o", envdBinary, "../../../cmd/envd") // #nosec G204 -- fixed arguments, fresh temporary directory
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fakeVMM stands in for Firecracker: it serves the API socket, and after InstanceStart it
// exposes a vsock unix socket that proxies to a real envd, like the guest would.
type fakeVMM struct {
	t      *testing.T
	envd   string // unix socket of a real envd
	failOn map[string]int
	mu     sync.Mutex
	vmms   map[string]*fakeAPI
	argvs  [][]string
}

type sleeper struct {
	id   procIdentity
	done chan struct{}
	cmd  *exec.Cmd
}

func (s *sleeper) Identity() procIdentity { return s.id }
func (s *sleeper) Done() <-chan struct{}  { return s.done }
func (s *sleeper) Tail() string           { return "fake vmm output" }

func (f *fakeVMM) Launch(_ context.Context, argv []string) (process, error) {
	f.mu.Lock()
	f.argvs = append(f.argvs, argv)
	f.mu.Unlock()
	apiSock, root := f.paths(argv)
	cmd := exec.Command("sleep", "300")
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	id, err := identityOf(cmd.Process.Pid)
	if err != nil {
		return nil, err
	}
	p := &sleeper{id: id, done: make(chan struct{}), cmd: cmd}
	go func() { _ = cmd.Wait(); close(p.done) }()
	api := &fakeAPI{status: f.failOn}
	api.onCall = func(c recordedCall) {
		if c.Path == "/vsock" {
			f.serveVsock(filepath.Join(root, c.Body["uds_path"].(string)))
		}
	}
	// The socket appears slightly after the process starts, as with the real VMM.
	go func() {
		time.Sleep(50 * time.Millisecond)
		serveFakeAPI(f.t, apiSock, api)
	}()
	f.mu.Lock()
	f.vmms[apiSock] = api
	f.mu.Unlock()
	return p, nil
}

// paths returns the host API socket and the directory the VMM treats as its root.
func (f *fakeVMM) paths(argv []string) (apiSock, root string) {
	for i, a := range argv {
		if a == "--api-sock" {
			return argv[i+1], "/"
		}
	}
	var id, base, exe string
	for i, a := range argv {
		switch a {
		case "--id":
			id = argv[i+1]
		case "--chroot-base-dir":
			base = argv[i+1]
		case "--exec-file":
			exe = filepath.Base(argv[i+1])
		}
	}
	root = filepath.Join(base, exe, id, "root")
	_ = os.MkdirAll(filepath.Join(root, "run"), 0o700)
	return filepath.Join(root, "run", "firecracker.socket"), root
}

func (f *fakeVMM) serveVsock(path string) {
	listener, err := net.Listen("unix", path)
	if err != nil {
		f.t.Error(err)
		return
	}
	f.t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				one, line := make([]byte, 1), []byte{}
				for {
					if _, err := conn.Read(one); err != nil {
						_ = conn.Close()
						return
					}
					line = append(line, one[0])
					if one[0] == '\n' {
						break
					}
				}
				port, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(line), "CONNECT ")))
				if port != 1024 {
					_ = conn.Close()
					return
				}
				guest, err := net.Dial("unix", f.envd)
				if err != nil {
					_ = conn.Close()
					return
				}
				_, _ = conn.Write([]byte("OK 1\n"))
				go func() { _, _ = io.Copy(guest, conn); _ = guest.Close() }()
				_, _ = io.Copy(conn, guest)
				_ = conn.Close()
			}()
		}
	}()
}

type driverFixture struct {
	driver *Driver
	vmm    *fakeVMM
	cfg    Config
}

func startEnvd(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, dir := range []string{"ws", "run"} {
		if err = os.Mkdir(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	server, err := envd.NewServer(envd.Config{Self: envdBinary, StateDir: filepath.Join(root, "run"), Roots: []string{filepath.Join(root, "ws"), "/tmp"}, BaseEnv: []string{"PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := envd.Listen("unix:" + filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- envd.Serve(ctx, envd.NewGRPCServer(server), listener, 30*time.Second) }()
	t.Cleanup(func() { cancel(); <-done })
	return filepath.Join(root, "s")
}

func newFixture(t *testing.T, jailed bool) *driverFixture {
	t.Helper()
	root, err := os.MkdirTemp("", "d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	cfg := Config{
		FirecrackerBinary: "/usr/bin/firecracker", KernelImage: filepath.Join(root, "vmlinux"), RootfsDir: filepath.Join(root, "rootfs"),
		StateDir: filepath.Join(root, "state"), InstallID: "test-install", BootTimeout: 20 * time.Second,
	}
	if jailed {
		cfg.JailerBinary, cfg.JailerUID, cfg.JailerGID = "/usr/bin/jailer", 10001, 10001
	} else {
		cfg.AllowUnjailed = true
	}
	if err = os.MkdirAll(cfg.RootfsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{cfg.KernelImage, filepath.Join(cfg.RootfsDir, "arcana-python_local.ext4")} {
		if err = os.WriteFile(f, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	vmm := &fakeVMM{t: t, envd: startEnvd(t), vmms: map[string]*fakeAPI{}}
	d.launcher = vmm
	d.mkfs = func(_ context.Context, path string, _ int, _, _ int) error {
		return os.WriteFile(path, []byte("fs"), 0o600)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &driverFixture{driver: d, vmm: vmm, cfg: cfg}
}

func readAll(t *testing.T, session runtime.ExecSession) (stdout, stderr string, exit runtime.ExitInfo) {
	t.Helper()
	var out, errOut bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = io.Copy(&errOut, session.Stderr()) }()
	_, _ = io.Copy(&out, session.Stdout())
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exit, err := session.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), exit
}

func TestDriver_CreateExecStatsAndDestroyUnjailed(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	spec := testSpec()
	spec.Env = map[string]string{"GREETING": "hello"}
	spec.Labels = map[string]string{"owner": "test"}
	instance, err := f.driver.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if instance.ID != "sbx-sbx-abcdefghijklmnopqrst" || instance.SandboxID != spec.SandboxID || instance.Labels["owner"] != "test" {
		t.Fatalf("%+v", instance)
	}
	if f.driver.Name() != "firecracker/unjailed" || !f.driver.Capabilities().SupportsTTY || f.driver.Capabilities().SupportsMemorySnapshot {
		t.Fatalf("%s %+v", f.driver.Name(), f.driver.Capabilities())
	}

	session, err := f.driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "echo $GREETING $EXTRA; echo oops >&2; exit 4"}, Cwd: "/tmp", Env: map[string]string{"EXTRA": "world"}})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := readAll(t, session)
	if stdout != "hello world\n" || stderr != "oops\n" || exit.ExitCode != 4 {
		t.Fatalf("%q %q %+v", stdout, stderr, exit)
	}

	session, err = f.driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"cat"}, Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789abcdef"), 20000) // several chunks
	go func() {
		_, _ = session.Stdin().Write(payload)
		_ = session.Stdin().Close()
	}()
	stdout, _, exit = readAll(t, session)
	if stdout != string(payload) || exit.ExitCode != 0 {
		t.Fatalf("stdin round trip failed: %d bytes, %+v", len(stdout), exit)
	}

	tty, err := f.driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"stty", "size"}, Cwd: "/tmp", TTY: true, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	if tty.Stderr() != nil {
		t.Fatal("a terminal session has no separate stderr")
	}
	out, _ := io.ReadAll(tty.Stdout())
	if exit, err = tty.Wait(ctx); err != nil || exit.ExitCode != 0 || !strings.Contains(string(out), "30 100") {
		t.Fatalf("%q %+v %v", out, exit, err)
	}

	stats, err := f.driver.Stats(ctx, instance.ID)
	if err != nil || stats.MemoryUsageBytes == 0 {
		t.Fatalf("%+v %v", stats, err)
	}
	listed, err := f.driver.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != instance.ID || listed[0].Stopped {
		t.Fatalf("%+v %v", listed, err)
	}

	vmm := f.driver.vms[instance.ID].meta.Process
	if err = f.driver.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if vmm.alive() {
		t.Fatal("the VMM survived Destroy")
	}
	if _, statErr := os.Stat(filepath.Join(f.cfg.StateDir, instance.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("VM files left behind: %v", statErr)
	}
	if err = f.driver.Destroy(ctx, instance.ID); err != nil {
		t.Fatalf("Destroy must be idempotent: %v", err)
	}
	if _, err = f.driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"true"}}); !errors.As(err, &runtime.NotFound{}) {
		t.Fatalf("exec in a destroyed VM: %v", err)
	}
	if listed, _ = f.driver.List(ctx); len(listed) != 0 {
		t.Fatalf("%+v", listed)
	}
}

func TestDriver_JailedStagesLinksAndConfiguresChrootRelativePaths(t *testing.T) {
	f := newFixture(t, true)
	instance, err := f.driver.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if f.driver.Name() != "firecracker" {
		t.Fatal(f.driver.Name())
	}
	l := layoutFor(f.driver.cfg, instance.ID)
	for _, path := range []string{l.KernelHost, l.RootfsHost, l.WorkspaceHost} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("not staged in the jail: %v", err)
		}
	}
	rootfsSource, _ := os.Stat(filepath.Join(f.cfg.RootfsDir, "arcana-python_local.ext4"))
	linked, _ := os.Stat(l.RootfsHost)
	if !os.SameFile(rootfsSource, linked) {
		t.Fatal("the rootfs must be hard-linked, not copied")
	}
	argv := f.vmm.argvs[0]
	if argv[0] != "/usr/bin/jailer" || !strings.Contains(strings.Join(argv, " "), "--uid 10001") {
		t.Fatalf("%v", argv)
	}
	var calls []string
	for _, api := range f.vmm.vmms {
		for _, c := range api.calls {
			calls = append(calls, c.Path)
		}
	}
	if len(calls) != 6 {
		t.Fatalf("%v", calls)
	}
	if err = f.driver.Destroy(context.Background(), instance.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(l.jailDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the jail was not removed: %v", err)
	}
}

func TestDriver_CreateRejectsBadRequestsBeforeStartingAnything(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	spec := testSpec()
	spec.Image = "unknown:image"
	if _, err := f.driver.Create(ctx, spec); !errors.As(err, &runtime.Invalid{}) {
		t.Fatalf("missing rootfs: %v", err)
	}
	spec = testSpec()
	spec.NetworkMode = "allowlist"
	if _, err := f.driver.Create(ctx, spec); !errors.As(err, &runtime.Unsupported{}) {
		t.Fatalf("allowlist: %v", err)
	}
	if len(f.vmm.argvs) != 0 {
		t.Fatal("a VMM was started for a rejected request")
	}
	if _, err := f.driver.Create(ctx, testSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.driver.Create(ctx, testSpec()); !errors.As(err, &runtime.Invalid{}) {
		t.Fatalf("a duplicate sandbox: %v", err)
	}
	for _, id := range []string{"", "../x", "sbx-../x", "other"} {
		if err := f.driver.Destroy(ctx, id); !errors.As(err, &runtime.NotFound{}) {
			t.Errorf("Destroy(%q): %v", id, err)
		}
		if _, err := f.driver.Stats(ctx, id); !errors.As(err, &runtime.NotFound{}) {
			t.Errorf("Stats(%q): %v", id, err)
		}
	}
}

func TestDriver_FailedBootCleansUpTheVMMAndItsFiles(t *testing.T) {
	f := newFixture(t, false)
	f.vmm.failOn = map[string]int{"/boot-source": 400}
	before, _ := os.ReadDir(f.cfg.StateDir)
	_, err := f.driver.Create(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "rejected by the fake") {
		t.Fatalf("%v", err)
	}
	after, _ := os.ReadDir(f.cfg.StateDir)
	if len(after) != len(before) {
		t.Fatalf("a failed boot left %v behind", after)
	}
	if len(f.driver.vms) != 0 {
		t.Fatal("a failed boot left a VM registered")
	}
}

func TestDriver_AnotherProcessCanAdoptListAndDestroyRunningVMs(t *testing.T) {
	f := newFixture(t, false)
	ctx := context.Background()
	spec := testSpec()
	spec.Env = map[string]string{"SECRET": "value"}
	instance, err := f.driver.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := os.ReadFile(filepath.Join(f.cfg.StateDir, instance.ID, "meta.json"))
	if strings.Contains(string(meta), "value") || strings.Contains(string(meta), "SECRET") {
		t.Fatalf("environment leaked into persisted metadata: %s", meta)
	}
	restarted, err := New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	listed, _ := restarted.List(ctx)
	if len(listed) != 1 || listed[0].SandboxID != spec.SandboxID {
		t.Fatalf("%+v", listed)
	}
	session, err := restarted.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "echo ${SECRET:-none}"}, Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if out, _, exit := readAll(t, session); out != "none\n" || exit.ExitCode != 0 {
		t.Fatalf("an adopted VM must not know the old environment: %q %+v", out, exit)
	}
	pid := f.driver.vms[instance.ID].meta.Process
	if err = restarted.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if pid.alive() {
		t.Fatal("the adopted VMM survived Destroy")
	}
	// A foreign installation's VM is invisible.
	other := f.cfg
	other.InstallID = "someone-else"
	foreign, _ := New(other)
	if listed, _ = foreign.List(ctx); len(listed) != 0 {
		t.Fatalf("%+v", listed)
	}
}

func TestExecSession_CancelledContextStopsTheCommand(t *testing.T) {
	f := newFixture(t, false)
	instance, err := f.driver.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := f.driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"sleep", "300"}, Cwd: "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() {
		_, waitErr := session.Wait(ctx)
		done <- waitErr
	}()
	select {
	case waitErr := <-done:
		if !errors.Is(waitErr, context.Canceled) {
			t.Fatalf("%v", waitErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

func TestDriver_ExecRejectsAnEmptyCommandAndBadRequests(t *testing.T) {
	f := newFixture(t, false)
	instance, err := f.driver.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.driver.Exec(context.Background(), instance.ID, runtime.ExecRequest{}); !errors.As(err, &runtime.Invalid{}) {
		t.Fatalf("%v", err)
	}
	if _, err = f.driver.Exec(context.Background(), instance.ID, runtime.ExecRequest{Cmd: []string{"true"}, Cwd: "/etc"}); !errors.As(err, &runtime.Invalid{}) {
		t.Fatalf("a cwd outside the allowed roots must be rejected by envd: %v", err)
	}
}

func TestMakeWorkspace_CreatesASparseExt4ImageOwnedByTheSandboxUser(t *testing.T) {
	if _, err := os.Stat("/usr/sbin/mke2fs"); err != nil {
		t.Skip("mke2fs is not installed")
	}
	path := filepath.Join(t.TempDir(), "workspace.ext4")
	if err := makeWorkspace(Config{MkfsBinary: "/usr/sbin/mke2fs"})(context.Background(), path, 16, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != 16*1024*1024 {
		t.Fatalf("%v %v", info, err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks*512 > 8*1024*1024 {
		t.Fatalf("the image is not sparse: %d bytes allocated", st.Blocks*512)
	}
	if err = makeWorkspace(Config{MkfsBinary: "/usr/sbin/mke2fs"})(context.Background(), path, 16, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("an existing image must never be reformatted")
	}
	if debugfs, lookErr := exec.LookPath("/usr/sbin/debugfs"); lookErr == nil {
		out, runErr := exec.Command(debugfs, "-R", "stat /", path).CombinedOutput() // #nosec G204 -- fixed binary, test-owned path
		if runErr != nil || !strings.Contains(string(out), "User:  1000") || !strings.Contains(string(out), "Group:  1000") {
			t.Fatalf("the filesystem root must belong to the sandbox user: %v %s", runErr, out)
		}
	}
}
