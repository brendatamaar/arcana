//go:build linux && integration

package containerd

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// Allowlist networking needs root, the CNI plugins and the proxy image, and reaches the real internet through
// the proxy. scripts/containerd-test.sh runs it as root inside the containerd container with all three
// (ARCANA_CONTAINERD_CNI_BIN_DIR, ARCANA_CONTAINERD_PROXY_IMAGE, ARCANA_CONTAINERD_PYTHON_IMAGE); anywhere else
// it skips with the reason rather than passing without checking anything.
type networkRig struct {
	driver    *Driver
	mu        sync.Mutex
	decisions []netpolicy.Decision
	image     string
	oci       string
}

func newNetworkRig(t *testing.T, oci string) *networkRig {
	t.Helper()
	cni, proxyImage, pythonImage := os.Getenv("ARCANA_CONTAINERD_CNI_BIN_DIR"), os.Getenv("ARCANA_CONTAINERD_PROXY_IMAGE"), os.Getenv("ARCANA_CONTAINERD_PYTHON_IMAGE")
	if cni == "" || proxyImage == "" || pythonImage == "" || os.Geteuid() != 0 {
		t.Skip("needs root and ARCANA_CONTAINERD_CNI_BIN_DIR, ARCANA_CONTAINERD_PROXY_IMAGE, ARCANA_CONTAINERD_PYTHON_IMAGE (scripts/containerd-test.sh runs it as root)")
	}
	rig := &networkRig{image: pythonImage, oci: oci}
	rig.driver = rig.open(t)
	return rig
}

// open builds a driver, as a restarted Arcana would.
func (r *networkRig) open(t *testing.T) *Driver {
	t.Helper()
	cfg := Config{
		Address: os.Getenv("ARCANA_CONTAINERD_ADDRESS"), Namespace: os.Getenv("ARCANA_CONTAINERD_NAMESPACE"), OCIRuntime: r.oci,
		AllowInsecureRuntime: r.oci == "runc", InstallID: "containerd-it", HelperPath: os.Getenv("ARCANA_CONTAINERD_HELPER"), FIFODir: os.Getenv("ARCANA_CONTAINERD_FIFO_DIR"),
		Network: &NetworkConfig{
			ProxyImage: os.Getenv("ARCANA_CONTAINERD_PROXY_IMAGE"), ProxyDNS: []string{"1.1.1.1", "1.0.0.1"}, CNIBinDir: os.Getenv("ARCANA_CONTAINERD_CNI_BIN_DIR"),
			OnDecision: func(e netpolicy.Decision) { r.mu.Lock(); r.decisions = append(r.decisions, e); r.mu.Unlock() },
		},
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func (r *networkRig) create(t *testing.T, ctx context.Context, allow ...string) runtime.Instance {
	t.Helper()
	id, err := ids.New("sbx")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := r.driver.Create(ctx, runtime.Spec{SandboxID: id, Image: r.image, WorkspaceBackend: "tmpfs", NetworkMode: "allowlist", NetworkAllow: allow, Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 256, DiskMB: 64, PIDs: 128}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.driver.Destroy(context.Background(), inst.ID) })
	return inst
}

func (r *networkRig) py(t *testing.T, id, code string) (string, runtime.ExitInfo) {
	t.Helper()
	out, errOut, exit := run(t, r.driver, id, runtime.ExecRequest{Cmd: []string{"python3", "-c", code}})
	return strings.TrimSpace(out + errOut), exit
}

const fetch = `import urllib.request,urllib.error
try:
    print(urllib.request.urlopen(%q,timeout=20).status)
except urllib.error.HTTPError as e:
    print("HTTP",e.code)
except Exception as e:
    print("ERR",type(e).__name__,e)
`

func TestContainerd_AllowlistNetworking(t *testing.T) {
	for _, oci := range []string{"runsc", "runc"} {
		t.Run(oci, func(t *testing.T) {
			rig := newNetworkRig(t, oci)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			one := rig.create(t, ctx, "example.com")
			two := rig.create(t, ctx, "example.com")

			// Through the proxy: the allowed host works over plain HTTP and through CONNECT; others are refused.
			for _, url := range []string{"http://example.com", "https://example.com"} {
				if out, _ := rig.py(t, one.ID, strings.Replace(fetch, "%q", `"`+url+`"`, 1)); out != "200" {
					t.Fatalf("%s through the proxy: %q", url, out)
				}
			}
			if out, _ := rig.py(t, one.ID, strings.Replace(fetch, "%q", `"http://example.org"`, 1)); out != "HTTP 403" {
				t.Fatalf("a host off the allowlist must get 403 from the proxy: %q", out)
			}

			// Directly: there is no route. Each probe must fail fast with a routing error, not time out.
			for name, target := range map[string]string{"the internet": "1.1.1.1:443", "the egress gateway": "172.29.0.1:22", "a host address": "10.0.0.1:80"} {
				host, port, _ := net.SplitHostPort(target)
				out, _ := rig.py(t, one.ID, `import socket
try:
    socket.create_connection(("`+host+`",`+port+`),timeout=5); print("CONNECTED")
except OSError as e:
    print("blocked",e.errno)`)
				if !strings.HasPrefix(out, "blocked") {
					t.Errorf("a direct connection to %s must fail: %q", name, out)
				} else if out != "blocked 101" {
					t.Errorf("a direct connection to %s should fail with ENETUNREACH (no route), got %q", name, out)
				}
			}
			// The only thing on the link is the proxy's port.
			if out, _ := rig.py(t, one.ID, `import socket
try:
    socket.create_connection(("169.254.77.3",22),timeout=5); print("CONNECTED")
except OSError as e:
    print("blocked",e.errno)`); !strings.HasPrefix(out, "blocked") {
				t.Errorf("only the proxy port may answer on the link: %q", out)
			}
			// One route only: the link to the proxy, never a default route.
			if out, _, exit := run(t, rig.driver, one.ID, runtime.ExecRequest{Cmd: []string{"sh", "-c", "tail -n +2 /proc/net/route | cut -f2"}}); exit.ExitCode != 0 || strings.Count(out, "\n") != 1 || strings.Contains(out, "00000000\n") {
				t.Errorf("sandbox routes (destinations): %q", out)
			}

			// The second sandbox has its own proxy, and destroying the first must not disturb it.
			if out, _ := rig.py(t, two.ID, strings.Replace(fetch, "%q", `"http://example.com"`, 1)); out != "200" {
				t.Fatalf("second sandbox: %q", out)
			}
			bridge := bridgeName(strings.TrimPrefix(one.ID, "sbx-"))
			if _, err := net.InterfaceByName(bridge); err != nil {
				t.Fatalf("the sandbox's private bridge %s should exist: %v", bridge, err)
			}
			if err := rig.driver.DrainDecisions(ctx); err != nil {
				t.Fatal(err)
			}
			rig.mu.Lock()
			var allowed, denied bool
			for _, e := range rig.decisions {
				allowed = allowed || (e.Host == "example.com" && e.Allowed)
				denied = denied || (e.Host == "example.org" && !e.Allowed)
			}
			rig.mu.Unlock()
			if !allowed || !denied {
				t.Fatalf("proxy decisions were not reported (allowed=%v denied=%v): %+v", allowed, denied, rig.decisions)
			}

			if err := rig.driver.Destroy(ctx, one.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := net.InterfaceByName(bridge); err == nil {
				t.Fatalf("the private bridge %s survived destroy", bridge)
			}
			names, _ := rig.driver.host.Netns()
			for _, name := range names {
				if strings.Contains(name, strings.TrimPrefix(one.ID, "sbx-")) {
					t.Fatalf("namespace %s survived destroy", name)
				}
			}
			if out, _ := rig.py(t, two.ID, strings.Replace(fetch, "%q", `"http://example.com"`, 1)); out != "200" {
				t.Fatalf("the surviving sandbox was disturbed: %q", out)
			}

			// A restarted Arcana: a new driver finds the sandbox, replaces its proxy (the token died with the old
			// process) and the sandbox keeps its allowlist.
			if err := rig.driver.Close(); err != nil {
				t.Fatal(err)
			}
			restarted := rig.open(t)
			rig.driver = restarted
			if err := restarted.PruneOrphans(ctx, map[string]bool{strings.TrimPrefix(two.ID, "sbx-"): true}); err != nil {
				t.Fatal(err)
			}
			if out, _ := rig.py(t, two.ID, strings.Replace(fetch, "%q", `"http://example.com"`, 1)); out != "200" {
				t.Fatalf("after the restart: %q", out)
			}
			if out, _ := rig.py(t, two.ID, strings.Replace(fetch, "%q", `"http://example.org"`, 1)); out != "HTTP 403" {
				t.Fatalf("after the restart the allowlist must still apply: %q", out)
			}
			if err := restarted.Destroy(ctx, two.ID); err != nil {
				t.Fatal(err)
			}
			if names, _ = restarted.host.Netns(); len(names) != 0 {
				t.Fatalf("namespaces left behind: %v", names)
			}
		})
	}
}
