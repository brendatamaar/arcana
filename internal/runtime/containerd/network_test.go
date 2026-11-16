package containerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
)

const testSandbox = "sbx_abcdefghijklmnopqrst"

// fakeHost records the host operations allowlist networking performs, in order.
type fakeHost struct {
	mu      sync.Mutex
	calls   []string
	netns   []string
	failOn  string // a "CNI ADD <if>" call that fails
	failDel error
}

func (f *fakeHost) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeHost) NewNetns(name string) (string, error) {
	f.record("netns+ " + name)
	f.mu.Lock()
	f.netns = append(f.netns, name)
	f.mu.Unlock()
	return "/run/arcana/netns/" + name, nil
}

func (f *fakeHost) DeleteNetns(name string) error {
	f.record("netns- " + name)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.netns = slices.DeleteFunc(f.netns, func(n string) bool { return n == name })
	return nil
}

func (f *fakeHost) Netns() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.netns), nil
}

func (f *fakeHost) DeleteLink(name string) error { f.record("link- " + name); return nil }

func (f *fakeHost) CNI(_ context.Context, command string, conf []byte, containerID, _, ifName string) error {
	var c cniConfig
	if err := json.Unmarshal(conf, &c); err != nil {
		return err
	}
	call := "CNI " + command + " " + containerID + " " + ifName + " " + c.Name
	f.record(call)
	if command == "ADD" && f.failOn == containerID+" "+ifName {
		return errors.New("plugin failed")
	}
	if command == "DEL" {
		return f.failDel
	}
	return nil
}

func (f *fakeHost) has(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func (f *fakeHost) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// proxyServer plays the proxy's control socket: it records policy pushes and serves queued decisions.
type proxyServer struct {
	mu        sync.Mutex
	policies  []netpolicy.Policy
	tokens    []string
	decisions []netpolicy.Decision
}

func (p *proxyServer) handle(token, method, path string, body []byte) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tokens = append(p.tokens, token)
	switch {
	case method == "PUT" && path == "/policy":
		var policy netpolicy.Policy
		_ = json.Unmarshal(body, &policy)
		p.policies = append(p.policies, policy)
		return nil
	case method == "GET" && path == "/decisions":
		out, _ := json.Marshal(decisionBatch{Events: p.decisions})
		p.decisions = nil
		return out
	}
	return nil
}

type controlSession struct {
	handle func(token, method, path string, body []byte) []byte
	in     bytes.Buffer
	out    *io.PipeReader
	outW   *io.PipeWriter
}

func newControlSession(handle func(token, method, path string, body []byte) []byte) *controlSession {
	r, w := io.Pipe()
	return &controlSession{handle: handle, out: r, outW: w}
}

func (s *controlSession) Write(b []byte) (int, error) { return s.in.Write(b) }
func (s *controlSession) Close() error {
	var req struct {
		Token, Method, Path string
		Body                []byte
	}
	_ = json.Unmarshal(s.in.Bytes(), &req)
	go func() {
		_, _ = s.outW.Write(s.handle(req.Token, req.Method, req.Path, req.Body))
		_ = s.outW.Close()
	}()
	return nil
}
func (s *controlSession) Stdin() io.WriteCloser       { return s }
func (s *controlSession) Stdout() io.Reader           { return s.out }
func (s *controlSession) Stderr() io.Reader           { return strings.NewReader("") }
func (s *controlSession) Resize(uint16, uint16) error { return nil }
func (s *controlSession) Wait(context.Context) (runtime.ExitInfo, error) {
	return runtime.ExitInfo{}, nil
}

func networkConfig() *NetworkConfig {
	n := &NetworkConfig{ProxyImage: "present", ProxyDNS: []string{"1.1.1.1"}}
	n.applyDefaults()
	return n
}

func allowlistSpec() runtime.Spec {
	s := testSpec()
	s.NetworkMode = "allowlist"
	s.NetworkAllow = []string{"example.com", "pypi.org"}
	return s
}

// networkDriver is a driver with fake containerd, a fake host and a fake proxy, acting as root.
func networkDriver(t *testing.T) (*Driver, *fakeEngine, *fakeHost, *proxyServer) {
	t.Helper()
	previous := geteuid
	geteuid = func() int { return 0 }
	t.Cleanup(func() { geteuid = previous })
	d, f := newDriver(t)
	d.cfg.Network = networkConfig()
	proxy := &proxyServer{}
	f.onExec = func(id string, _ runtime.ExecRequest) (runtime.ExecSession, error) {
		if id != proxyContainerID(testSandbox) {
			return fakeSession{}, nil
		}
		return newControlSession(proxy.handle), nil
	}
	host := &fakeHost{}
	d.host = host
	return d, f, host, proxy
}

func TestNetworkPlan_NamesAddressesAndConfigurations(t *testing.T) {
	if got := bridgeName(testSandbox); got != "arcabcdefghijklm" && len(got) > 15 {
		t.Fatalf("bridge name %q exceeds the 15-byte interface name limit", got)
	}
	if bridgeName(testSandbox) == bridgeName("sbx_zzzzzzzzzzzzzzzzzzzz") {
		t.Fatal("two sandboxes must not share a bridge")
	}
	if linkMAC(proxyLinkIP) != "02:42:a9:fe:4d:03" || linkMAC(sandboxLinkIP) == linkMAC(proxyLinkIP) {
		t.Fatalf("MACs %s %s", linkMAC(proxyLinkIP), linkMAC(sandboxLinkIP))
	}
	link, err := linkConfig(testSandbox, sandboxLinkIP)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err = json.Unmarshal(link, &c); err != nil {
		t.Fatal(err)
	}
	ipam := c["ipam"].(map[string]any)
	if c["type"] != "bridge" || c["bridge"] != bridgeName(testSandbox) || c["isGateway"] != false || c["ipMasq"] != false || ipam["type"] != "static" {
		t.Fatalf("private link: %s", link)
	}
	if _, has := ipam["routes"]; has || strings.Contains(string(link), "0.0.0.0/0") {
		t.Fatalf("the private link must carry no route: %s", link)
	}
	if !strings.Contains(string(link), "169.254.77.2/29") || c["runtimeConfig"].(map[string]any)["mac"] != linkMAC(sandboxLinkIP) {
		t.Fatalf("fixed address and MAC missing: %s", link)
	}
	egress, err := egressConfig(*networkConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(egress), `"isGateway":true`) || !strings.Contains(string(egress), `"ipMasq":true`) || !strings.Contains(string(egress), "172.29.0.0/20") || !strings.Contains(string(egress), "0.0.0.0/0") {
		t.Fatalf("egress: %s", egress)
	}
}

// The gVisor shim resolves a container by ID prefix (found on a real runsc): one ID that begins another makes both
// unloadable.
func TestContainerNamesNeverPrefixEachOther(t *testing.T) {
	sandbox, proxy := containerID(testSandbox), proxyContainerID(testSandbox)
	if strings.HasPrefix(sandbox, proxy) || strings.HasPrefix(proxy, sandbox) {
		t.Fatalf("%q and %q must not be prefixes of each other", sandbox, proxy)
	}
	if id, ok := sandboxOfNetns(proxy); !ok || id != testSandbox {
		t.Fatalf("%q %v", id, ok)
	}
	if id, ok := sandboxOfNetns(sandbox); !ok || id != testSandbox {
		t.Fatalf("%q %v", id, ok)
	}
	if _, ok := sandboxFromContainer(proxy); ok {
		t.Fatal("a proxy must never be listed as a sandbox")
	}
}

func TestWithProxyEnvironment_ReplacesWhateverTheSandboxWasGiven(t *testing.T) {
	got := withProxyEnvironment([]string{"A=1", "http_proxy=http://evil:1", "NO_PROXY=*", "Https_Proxy=x"})
	want := []string{"A=1", "HTTP_PROXY=http://169.254.77.3:3128", "HTTPS_PROXY=http://169.254.77.3:3128", "NO_PROXY=", "http_proxy=http://169.254.77.3:3128", "https_proxy=http://169.254.77.3:3128", "no_proxy="}
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

func TestNetworkConfigValidate(t *testing.T) {
	if err := networkConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*NetworkConfig){
		"no proxy image":     func(n *NetworkConfig) { n.ProxyImage = "" },
		"no dns":             func(n *NetworkConfig) { n.ProxyDNS = nil },
		"hostname dns":       func(n *NetworkConfig) { n.ProxyDNS = []string{"dns.example"} },
		"relative cni dir":   func(n *NetworkConfig) { n.CNIBinDir = "cni" },
		"relative state dir": func(n *NetworkConfig) { n.StateDir = "run/arcana" },
		"ipv6 egress":        func(n *NetworkConfig) { n.EgressSubnet = "fd00::/64" },
		"unmasked egress":    func(n *NetworkConfig) { n.EgressSubnet = "172.29.0.1/20" },
		"tiny egress":        func(n *NetworkConfig) { n.EgressSubnet = "172.29.0.0/30" },
		"huge egress":        func(n *NetworkConfig) { n.EgressSubnet = "0.0.0.0/0" },
	} {
		n := networkConfig()
		mutate(n)
		if n.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPlanContainer_AllowlistNeedsNetworkConfigAndKeepsTheListOnTheContainer(t *testing.T) {
	cfg := testConfig(t)
	var unsupported runtime.Unsupported
	if _, err := planContainer(cfg, allowlistSpec()); !errors.As(err, &unsupported) {
		t.Fatalf("without network settings: %v", err)
	}
	cfg.Network = networkConfig()
	p, err := planContainer(cfg, allowlistSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !p.Allowlist || p.Labels["arcana.network"] != "allowlist" || p.Labels["arcana.allow"] != `["example.com","pypi.org"]` {
		t.Fatalf("%+v", p.Labels)
	}
	if !slices.Contains(p.Env, "HTTPS_PROXY=http://169.254.77.3:3128") {
		t.Fatalf("env %v", p.Env)
	}
	none, err := planContainer(cfg, testSpec())
	if err != nil || none.Allowlist || none.Labels["arcana.network"] != "" || slices.ContainsFunc(none.Env, func(e string) bool { return strings.Contains(e, "PROXY") }) {
		t.Fatalf("a none sandbox must not get proxy settings: %+v %v", none, err)
	}
}

func TestProxyPlan_IsHardenedAndHoldsOnlyTheTokenHash(t *testing.T) {
	cfg := testConfig(t)
	cfg.Network = networkConfig()
	p := proxyPlan(cfg, testSandbox, "secret-token", "/run/arcana/netns/proxy")
	sum := sha256.Sum256([]byte("secret-token"))
	if !slices.Contains(p.Env, "ARCANA_PROXY_TOKEN_HASH="+hex.EncodeToString(sum[:])) || slices.ContainsFunc(p.Env, func(e string) bool { return strings.Contains(e, "secret-token") }) {
		t.Fatalf("the token must appear only as a hash: %v", p.Env)
	}
	if p.ID != "prx-"+testSandbox || !slices.Equal(p.Args, []string{"/arcana", "proxy"}) || p.Labels["arcana.proxy"] != "true" || p.Labels["arcana.sandbox_id"] != testSandbox {
		t.Fatalf("%+v", p)
	}
	spec, err := applySpec(context.Background(), p.SpecOpts)
	if err != nil {
		t.Fatal(err)
	}
	proc := spec.Process
	if proc.User.UID != 1000 || !proc.NoNewPrivileges || len(proc.Capabilities.Effective) != 0 || len(proc.Capabilities.Bounding) != 0 || !spec.Root.Readonly {
		t.Fatalf("proxy hardening: %+v root %+v", proc, spec.Root)
	}
	if *spec.Linux.Resources.Pids.Limit != 64 || *spec.Linux.Resources.Memory.Limit != 256*1024*1024 || spec.Linux.CgroupsPath != "/arcana-"+p.ID {
		t.Fatalf("limits %+v", spec.Linux.Resources)
	}
	var joined bool
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			joined = ns.Path == "/run/arcana/netns/proxy"
		}
	}
	if !joined {
		t.Fatalf("the proxy must join its prepared namespace: %+v", spec.Linux.Namespaces)
	}
	for _, m := range spec.Mounts {
		if m.Type == "bind" && !strings.HasPrefix(m.Source, "/proc") && !strings.HasPrefix(m.Source, "/sys") && !strings.HasPrefix(m.Source, "/dev") {
			t.Errorf("the proxy has no host mounts: %+v", m)
		}
	}
}

func TestDriver_AllowlistCreateBuildsTheNetworkBeforeTheSandboxAndStartsTheProxyFirst(t *testing.T) {
	d, f, host, proxy := networkDriver(t)
	inst, err := d.Create(context.Background(), allowlistSpec())
	if err != nil || inst.ID != "sbx-"+testSandbox {
		t.Fatalf("%+v %v", inst, err)
	}
	wantCalls := []string{
		"netns+ sbx-" + testSandbox,
		"netns+ prx-" + testSandbox,
		"CNI ADD prx-" + testSandbox + " eth0 arcana-egress",
		"CNI ADD prx-" + testSandbox + " eth1 arcana-" + testSandbox,
		"CNI ADD sbx-" + testSandbox + " eth0 arcana-" + testSandbox,
	}
	if !slices.Equal(host.calls, wantCalls) {
		t.Fatalf("host calls:\n%v\nwant\n%v", host.calls, wantCalls)
	}
	if len(f.created) != 2 || f.created[0].ID != "prx-"+testSandbox || f.created[1].ID != "sbx-"+testSandbox {
		t.Fatalf("the proxy must start before the sandbox: %+v", f.created)
	}
	if len(proxy.policies) != 1 || proxy.policies[0].Source != sandboxLinkIP || !slices.Equal(proxy.policies[0].Allow, []string{"example.com", "pypi.org"}) || proxy.policies[0].SandboxID != testSandbox {
		t.Fatalf("policy %+v", proxy.policies)
	}
	// The token the proxy was given matches the hash in its environment, and the sandbox joined its own namespace.
	sum := sha256.Sum256([]byte(proxy.tokens[0]))
	if !slices.Contains(f.created[0].Env, "ARCANA_PROXY_TOKEN_HASH="+hex.EncodeToString(sum[:])) {
		t.Fatal("token and hash disagree")
	}
	spec, err := applySpec(context.Background(), f.created[1].SpecOpts)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			path = ns.Path
		}
	}
	if path != "/run/arcana/netns/sbx-"+testSandbox {
		t.Fatalf("sandbox namespace %q", path)
	}
}

func TestDriver_AllowlistNeedsRootAndCreatesNothingOtherwise(t *testing.T) {
	d, f, host, _ := networkDriver(t)
	geteuid = func() int { return 1000 }
	if _, err := d.Create(context.Background(), allowlistSpec()); !errors.Is(err, errNeedsRoot) {
		t.Fatalf("%v", err)
	}
	if len(host.calls) != 0 || len(f.created) != 0 {
		t.Fatalf("nothing may be created: %v %v", host.calls, f.created)
	}
}

func TestDriver_AFailedNetworkLeavesNothingBehind(t *testing.T) {
	for _, failing := range []string{"prx-" + testSandbox + " eth1", "sbx-" + testSandbox + " eth0"} {
		d, f, host, _ := networkDriver(t)
		host.failOn = failing
		if _, err := d.Create(context.Background(), allowlistSpec()); err == nil || !strings.Contains(err.Error(), "plugin failed") {
			t.Fatalf("%s: %v", failing, err)
		}
		if len(f.created) != 0 {
			t.Errorf("%s: no container may start before the network is complete: %+v", failing, f.created)
		}
		if len(host.netns) != 0 || !host.has("link- "+bridgeName(testSandbox)) || host.count("CNI DEL") != 3 {
			t.Errorf("%s: leftovers %v / %v", failing, host.netns, host.calls)
		}
	}
}

func TestDriver_AFailedSandboxStartRemovesTheNetworkAndProxy(t *testing.T) {
	d, f, host, _ := networkDriver(t)
	f.failCreateID = "sbx-" + testSandbox
	f.failCreate = errors.New("shim refused")
	if _, err := d.Create(context.Background(), allowlistSpec()); err == nil || !strings.Contains(err.Error(), "shim refused") {
		t.Fatalf("%v", err)
	}
	if len(host.netns) != 0 || !slices.Contains(f.deleted, "prx-"+testSandbox) || len(d.networks) != 0 {
		t.Fatalf("leftovers: netns %v deleted %v networks %v", host.netns, f.deleted, d.networks)
	}
}

func TestDriver_DestroyRemovesTheNetworkOnlyForAllowlistSandboxes(t *testing.T) {
	d, f, host, _ := networkDriver(t)
	ctx := context.Background()
	inst, err := d.Create(ctx, allowlistSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil {
		t.Fatal(err)
	}
	if len(host.netns) != 0 || !host.has("link- "+bridgeName(testSandbox)) || host.count("CNI DEL") != 3 || !slices.Contains(f.deleted, "prx-"+testSandbox) {
		t.Fatalf("%v %v %v", host.netns, host.calls, f.deleted)
	}
	// A network-none sandbox costs no host networking calls on destroy.
	other, _, quiet, _ := networkDriver(t)
	none, err := other.Create(ctx, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Destroy(ctx, none.ID); err != nil || len(quiet.calls) != 0 {
		t.Fatalf("%v %v", err, quiet.calls)
	}
	if err = d.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("destroy must stay idempotent: %v", err)
	}
}

func TestDriver_DrainDecisionsDeliversTheProxysEvents(t *testing.T) {
	d, _, _, proxy := networkDriver(t)
	var got []netpolicy.Decision
	var dropped uint64
	d.cfg.Network.OnDecision = func(e netpolicy.Decision) { got = append(got, e) }
	d.cfg.Network.OnDropped = func(n uint64) { dropped += n }
	if _, err := d.Create(context.Background(), allowlistSpec()); err != nil {
		t.Fatal(err)
	}
	proxy.decisions = []netpolicy.Decision{{SandboxID: testSandbox, Host: "evil.example"}}
	if err := d.DrainDecisions(context.Background()); err != nil || len(got) != 1 || got[0].Host != "evil.example" {
		t.Fatalf("%v %+v", err, got)
	}
	// Destroying flushes what the proxy still holds.
	proxy.decisions = []netpolicy.Decision{{SandboxID: testSandbox, Host: "last.example"}}
	if err := d.Destroy(context.Background(), "sbx-"+testSandbox); err != nil || len(got) != 2 || got[1].Host != "last.example" {
		t.Fatalf("%v %+v", err, got)
	}
	_ = dropped
}

func TestDriver_PruneOrphansKeepsLiveNetworksWithAFreshProxyAndRemovesTheRest(t *testing.T) {
	d, f, host, proxy := networkDriver(t)
	ctx := context.Background()
	if _, err := d.Create(ctx, allowlistSpec()); err != nil {
		t.Fatal(err)
	}
	firstToken := proxy.tokens[0]
	before := len(host.calls)
	orphan := "sbx_bbbbbbbbbbbbbbbbbbbb"
	host.netns = append(host.netns, "sbx-"+orphan, "prx-"+orphan, "not-ours")
	// A restart: the in-memory control tokens are gone.
	d.networks = nil
	proxy.policies, proxy.tokens = nil, nil
	if err := d.PruneOrphans(ctx, map[string]bool{testSandbox: true}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(host.netns, "sbx-"+orphan) || slices.Contains(host.netns, "prx-"+orphan) || !slices.Contains(host.netns, "not-ours") {
		t.Fatalf("orphan namespaces: %v", host.netns)
	}
	if !slices.Contains(host.netns, "sbx-"+testSandbox) || !slices.Contains(host.netns, "prx-"+testSandbox) {
		t.Fatalf("the kept sandbox lost its namespaces: %v", host.netns)
	}
	// The proxy's network is rebuilt (a gVisor proxy strips its interfaces' addresses when it starts, so reusing
	// the namespace would leave the second proxy without a link), and the sandbox's own network is left alone.
	restored := host.calls[before:]
	wantOrder := []string{"CNI DEL prx-" + testSandbox + " eth0", "CNI DEL prx-" + testSandbox + " eth1", "netns- prx-" + testSandbox, "netns+ prx-" + testSandbox, "CNI ADD prx-" + testSandbox + " eth0", "CNI ADD prx-" + testSandbox + " eth1"}
	at := -1
	for _, want := range wantOrder {
		i := slices.IndexFunc(restored[at+1:], func(c string) bool { return strings.HasPrefix(c, want) })
		if i < 0 {
			t.Fatalf("restoring the proxy: %q missing or out of order in %v", want, restored)
		}
		at += i + 1
	}
	if slices.ContainsFunc(restored, func(c string) bool {
		return strings.Contains(c, " sbx-"+testSandbox+" ") || c == "netns+ sbx-"+testSandbox
	}) {
		t.Fatalf("a restore must not touch the sandbox's own network: %v", restored)
	}
	last := f.created[len(f.created)-1]
	if last.ID != "prx-"+testSandbox || len(proxy.policies) != 1 || !slices.Equal(proxy.policies[0].Allow, []string{"example.com", "pypi.org"}) {
		t.Fatalf("the proxy must be recreated with the stored allowlist: %+v %+v", last.ID, proxy.policies)
	}
	if proxy.tokens[0] == firstToken {
		t.Fatal("a recreated proxy needs a fresh control token")
	}
	// Nothing kept: everything goes.
	if err := d.PruneOrphans(ctx, nil); err != nil || len(host.netns) != 1 || host.netns[0] != "not-ours" {
		t.Fatalf("%v %v", err, host.netns)
	}
}

func TestDriver_CheckReportsMissingNetworkPrerequisites(t *testing.T) {
	d, _, _, _ := networkDriver(t)
	d.cfg.Network.CNIBinDir = t.TempDir()
	if _, err := d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), `"bridge"`) {
		t.Fatalf("missing plugins: %v", err)
	}
	for _, plugin := range []string{"bridge", "host-local", "static"} {
		if err := os.WriteFile(filepath.Join(d.cfg.Network.CNIBinDir, plugin), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- an executable test fixture in a private temp directory
			t.Fatal(err)
		}
	}
	detail, err := d.Check(context.Background())
	if err != nil || !strings.Contains(detail, "allowlist networking") {
		t.Fatalf("%q %v", detail, err)
	}
	d.cfg.Network.ProxyImage = "absent"
	if _, err = d.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "proxy image") {
		t.Fatalf("missing proxy image: %v", err)
	}
	d.cfg.Network.ProxyImage = "present"
	geteuid = func() int { return 1000 }
	if _, err = d.Check(context.Background()); !errors.Is(err, errNeedsRoot) {
		t.Fatalf("non-root: %v", err)
	}
}

func TestWithoutDevice_DropsOnlyOurOwnBridgesRoutes(t *testing.T) {
	routes := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"eth0\t00000000\t010011AC\t0003\t0\t0\t0\t00000000\t0\t0\t0\n" +
		"arcana-egress0\t0000 1DAC\t00000000\t0001\t0\t0\t0\t00F0FFFF\t0\t0\t0\n" +
		"arcana-egress0x\t0000 1DAC\t00000000\t0001\t0\t0\t0\t00F0FFFF\t0\t0\t0\n"
	got := withoutDevice(routes, "arcana-egress0")
	if strings.Contains(got, "arcana-egress0\t") || !strings.Contains(got, "eth0\t") || !strings.Contains(got, "arcana-egress0x\t") || !strings.HasPrefix(got, "Iface") {
		t.Fatalf("%q", got)
	}
}
