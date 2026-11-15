//go:build linux && integration

package containerd

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// These tests need a reachable containerd. Set ARCANA_CONTAINERD_ADDRESS (its socket),
// ARCANA_CONTAINERD_HELPER (a host path of sbx-exec as containerd sees it) and make the template
// image available in the namespace (ARCANA_CONTAINERD_NAMESPACE, default arcana; image
// ARCANA_CONTAINERD_IMAGE, default docker.io/library/arcana-base:local). Without an address they skip
// with the reason, so they never pass vacuously. scripts/containerd-test.sh sets all of this up.
func containerdFixture(t *testing.T, oci string) *Driver {
	t.Helper()
	address := os.Getenv("ARCANA_CONTAINERD_ADDRESS")
	helper := os.Getenv("ARCANA_CONTAINERD_HELPER")
	if address == "" || helper == "" {
		t.Skip("set ARCANA_CONTAINERD_ADDRESS and ARCANA_CONTAINERD_HELPER (see scripts/containerd-test.sh)")
	}
	cfg := Config{Address: address, Namespace: os.Getenv("ARCANA_CONTAINERD_NAMESPACE"), OCIRuntime: oci, AllowInsecureRuntime: oci == "runc", InstallID: "containerd-it", HelperPath: helper, FIFODir: os.Getenv("ARCANA_CONTAINERD_FIFO_DIR")}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func image() string {
	if v := os.Getenv("ARCANA_CONTAINERD_IMAGE"); v != "" {
		return v
	}
	return "docker.io/library/arcana-base:local"
}

func run(t *testing.T, d *Driver, id string, req runtime.ExecRequest) (stdout, stderr string, exit runtime.ExitInfo) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := d.Exec(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if session.Stderr() != nil {
			_, _ = io.Copy(&errOut, session.Stderr())
		}
	}()
	_, _ = io.Copy(&out, session.Stdout())
	wg.Wait()
	exit, err = session.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), exit
}

func TestContainerd_Lifecycle(t *testing.T) {
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) {
			d := containerdFixture(t, oci)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			spec := runtime.Spec{
				SandboxID: "sbx_integrationtest" + map[string]string{"runsc": "aaaaa", "runc": "bbbbb"}[oci][:1], Image: image(),
				WorkspaceBackend: "tmpfs", NetworkMode: "none", Env: map[string]string{"GREETING": "hello"},
				Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 256, DiskMB: 64, PIDs: 128}, Labels: map[string]string{"suite": "it"},
			}
			spec.SandboxID = "sbx_integrationtest" + strings.Repeat(map[string]string{"runsc": "a", "runc": "b"}[oci], 1) + "aaaa"
			spec.SandboxID = spec.SandboxID[:24]
			created, err := d.Create(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Destroy(context.Background(), created.ID) })

			listed, err := d.List(ctx)
			if err != nil || len(listed) == 0 {
				t.Fatalf("%+v %v", listed, err)
			}

			stdout, stderr, exit := run(t, d, created.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "echo out; echo err >&2; echo $GREETING; exit 5"}, Cwd: "/workspace"})
			if stdout != "out\nhello\n" || stderr != "err\n" || exit.ExitCode != 5 {
				t.Fatalf("%q %q %+v", stdout, stderr, exit)
			}

			// The hardening of FR-RL-1 and FR-RL-2, observed from inside the sandbox.
			probe := `id -u; grep -E '^(CapEff|NoNewPrivs):' /proc/self/status | tr -d '\t' | tr '\n' ' '; echo; ulimit -n;
			  (touch /etc/arcana-probe 2>&1 && echo ETC-WRITABLE) || echo etc-readonly;
			  (touch /opt/arcana/sbx-exec-probe 2>&1 && echo HELPER-DIR-WRITABLE) || echo helper-dir-readonly;
			  echo ws > /workspace/w && cat /workspace/w; echo t > /tmp/t && echo tmp-ok;
			  test -x /opt/arcana/sbx-exec && echo helper-present`
			out, _, exit := run(t, d, created.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", probe}, Cwd: "/workspace"})
			if exit.ExitCode != 0 {
				t.Fatalf("probe: %q %+v", out, exit)
			}
			for _, want := range []string{"1000\n", "CapEff:0000000000000000", "NoNewPrivs:1", "1024\n", "etc-readonly", "helper-dir-readonly", "ws\n", "tmp-ok", "helper-present"} {
				if !strings.Contains(out, want) {
					t.Errorf("missing %q in:\n%s", want, out)
				}
			}
			if strings.Contains(out, "WRITABLE") {
				t.Errorf("a read-only path was writable:\n%s", out)
			}

			// stdin round trip with a payload larger than a pipe buffer.
			ctx2, cancel2 := context.WithTimeout(ctx, 60*time.Second)
			defer cancel2()
			session, err := d.Exec(ctx2, created.ID, runtime.ExecRequest{Cmd: []string{"cat"}, Cwd: "/workspace"})
			if err != nil {
				t.Fatal(err)
			}
			payload := bytes.Repeat([]byte("0123456789abcdef"), 20000)
			go func() { _, _ = session.Stdin().Write(payload); _ = session.Stdin().Close() }()
			echoed, _ := io.ReadAll(session.Stdout())
			if _, err = session.Wait(ctx2); err != nil || !bytes.Equal(echoed, payload) {
				t.Fatalf("stdin round trip: %d bytes, %v", len(echoed), err)
			}

			// A terminal has its size and no separate stderr.
			tty, err := d.Exec(ctx2, created.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "sleep 1; stty size"}, TTY: true, Cols: 100, Rows: 30})
			if err != nil {
				t.Fatal(err)
			}
			if tty.Stderr() != nil {
				t.Fatal("a terminal session has no separate stderr")
			}
			ttyOut, _ := io.ReadAll(tty.Stdout())
			if _, err = tty.Wait(ctx2); err != nil || !strings.Contains(string(ttyOut), "30 100") {
				t.Fatalf("tty: %q %v", ttyOut, err)
			}

			// A cancelled context stops the command.
			cancelCtx, cancelNow := context.WithCancel(ctx)
			sleeper, err := d.Exec(cancelCtx, created.ID, runtime.ExecRequest{Cmd: []string{"sleep", "300"}})
			if err != nil {
				t.Fatal(err)
			}
			cancelNow()
			started := time.Now()
			if _, err = sleeper.Wait(cancelCtx); err == nil || time.Since(started) > 20*time.Second {
				t.Fatalf("cancellation: %v after %s", err, time.Since(started))
			}

			if stats, statsErr := d.Stats(ctx, created.ID); statsErr != nil || stats.MemoryUsageBytes == 0 {
				t.Fatalf("stats: %+v %v", stats, statsErr)
			}
			if err = d.Destroy(ctx, created.ID); err != nil {
				t.Fatal(err)
			}
			if err = d.Destroy(ctx, created.ID); err != nil {
				t.Fatalf("Destroy must be idempotent: %v", err)
			}
			listed, _ = d.List(ctx)
			for _, inst := range listed {
				if inst.ID == created.ID {
					t.Fatal("a destroyed sandbox is still listed")
				}
			}
		})
	}
}

// A new driver (a restarted Arcana) finds, uses and removes containers its predecessor created.
func TestContainerd_ASecondDriverCanAdoptAndDestroy(t *testing.T) {
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) { adoption(t, oci) })
	}
}

func adoption(t *testing.T, oci string) {
	first := containerdFixture(t, oci)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	spec := runtime.Spec{SandboxID: "sbx_adoptionintegration" + map[string]string{"runsc": "a", "runc": "b"}[oci], Image: image(), WorkspaceBackend: "tmpfs", NetworkMode: "none", Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 128, DiskMB: 32, PIDs: 64}}
	created, err := first.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Destroy(context.Background(), created.ID) })
	if _, _, exit := run(t, first, created.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "echo kept > /workspace/f"}}); exit.ExitCode != 0 {
		t.Fatalf("%+v", exit)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second := containerdFixture(t, oci)
	listed, err := second.List(ctx)
	if err != nil || !slices.ContainsFunc(listed, func(i runtime.Instance) bool { return i.ID == created.ID && !i.Stopped }) {
		t.Fatalf("%+v %v", listed, err)
	}
	if out, _, exit := run(t, second, created.ID, runtime.ExecRequest{Cmd: []string{"cat", "/workspace/f"}}); out != "kept\n" || exit.ExitCode != 0 {
		t.Fatalf("%q %+v", out, exit)
	}
	if err = second.Destroy(ctx, created.ID); err != nil {
		t.Fatalf("destroying a container another driver created: %v", err)
	}
}

// Sandboxes share the containerd namespace's cgroup parent, so removing one must not disturb or be blocked
// by the others.
func TestContainerd_DestroyingOneSandboxLeavesTheOthersRunning(t *testing.T) {
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) {
			d := containerdFixture(t, oci)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			var created [2]runtime.Instance
			for i := range created {
				id, err := ids.New("sbx")
				if err != nil {
					t.Fatal(err)
				}
				spec := runtime.Spec{SandboxID: id, Image: image(), WorkspaceBackend: "tmpfs", NetworkMode: "none", Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 128, DiskMB: 32, PIDs: 64}}
				inst, err := d.Create(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				created[i] = inst
				t.Cleanup(func() { _ = d.Destroy(context.Background(), inst.ID) })
			}
			if err := d.Destroy(ctx, created[0].ID); err != nil {
				t.Fatalf("destroying one of two sandboxes: %v", err)
			}
			if out, _, exit := run(t, d, created[1].ID, runtime.ExecRequest{Cmd: []string{"echo", "alive"}}); out != "alive\n" || exit.ExitCode != 0 {
				t.Fatalf("the other sandbox was disturbed: %q %+v", out, exit)
			}
			if err := d.Destroy(ctx, created[1].ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
