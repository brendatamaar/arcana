package containerd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// proxyState is what Arcana must remember about a running proxy: the control token. It is never stored on disk
// or in a label; after a restart the proxy is recreated with a fresh one.
type proxyState struct{ token string }

const proxyReadyTimeout = 10 * time.Second

var errNeedsRoot = errors.New("allowlist networking needs Arcana to run as root: it creates network namespaces and runs CNI plugins")

// createNetwork builds a sandbox's network and returns the namespace path its container must join:
//
//	sandbox ns:  eth0 ── private bridge (no address, no route) ── eth1  proxy ns: eth0 ── shared egress bridge ── NAT
//
// A failure removes whatever it built.
func (d *Driver) createNetwork(ctx context.Context, sandboxID string, allow []string) (string, error) {
	if d.cfg.Network == nil {
		return "", runtime.Unsupported{Feature: "containerd allowlist networking"}
	}
	if geteuid() != 0 {
		return "", errNeedsRoot
	}
	sandboxNS, err := d.host.NewNetns(netnsName(containerID(sandboxID)))
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return "", errors.Join(cause, d.removeNetwork(cleanup, sandboxID))
	}
	proxyNS, err := d.attachProxy(ctx, sandboxID)
	if err != nil {
		return fail(err)
	}
	sandboxLink, err := linkConfig(sandboxID, sandboxLinkIP)
	if err != nil {
		return fail(err)
	}
	if err = d.host.CNI(ctx, "ADD", sandboxLink, containerID(sandboxID), sandboxNS, "eth0"); err != nil {
		return fail(err)
	}
	if err = d.startProxy(ctx, sandboxID, proxyNS, allow); err != nil {
		return fail(err)
	}
	return sandboxNS, nil
}

func (d *Driver) proxyNetnsPath(sandboxID string) string {
	return filepath.Join(d.cfg.Network.StateDir, "netns", netnsName(proxyContainerID(sandboxID)))
}

// attachProxy gives the proxy its own namespace with its complete network set: eth0 on the shared egress bridge
// and eth1 on the sandbox's private bridge, at the fixed address and MAC. All of it happens before the proxy
// container starts, because gVisor reads the interfaces once at start.
func (d *Driver) attachProxy(ctx context.Context, sandboxID string) (string, error) {
	proxyNS, err := d.host.NewNetns(netnsName(proxyContainerID(sandboxID)))
	if err != nil {
		return "", err
	}
	egress, err := egressConfig(*d.cfg.Network)
	if err != nil {
		return "", err
	}
	link, err := linkConfig(sandboxID, proxyLinkIP)
	if err != nil {
		return "", err
	}
	if err = d.host.CNI(ctx, "ADD", egress, proxyContainerID(sandboxID), proxyNS, "eth0"); err != nil {
		return "", err
	}
	if err = d.host.CNI(ctx, "ADD", link, proxyContainerID(sandboxID), proxyNS, "eth1"); err != nil {
		return "", err
	}
	return proxyNS, nil
}

// detachProxy removes the proxy container and its whole network. Missing pieces are success.
func (d *Driver) detachProxy(ctx context.Context, sandboxID string) error {
	errs := []error{d.engine.Delete(ctx, proxyContainerID(sandboxID))}
	proxyNS := d.proxyNetnsPath(sandboxID)
	if egress, err := egressConfig(*d.cfg.Network); err == nil {
		errs = append(errs, d.host.CNI(ctx, "DEL", egress, proxyContainerID(sandboxID), proxyNS, "eth0"))
	}
	if link, err := linkConfig(sandboxID, proxyLinkIP); err == nil {
		errs = append(errs, d.host.CNI(ctx, "DEL", link, proxyContainerID(sandboxID), proxyNS, "eth1"))
	}
	return errors.Join(append(errs, d.host.DeleteNetns(netnsName(proxyContainerID(sandboxID))))...)
}

// restoreProxy replaces a kept sandbox's proxy after a restart, with a fresh control token. The proxy's network
// is rebuilt, not reused: a gVisor proxy takes the interfaces over when it starts (it strips their addresses from
// the namespace), so a second proxy in the same namespace would have a link without an address. The fixed
// address and MAC make the sandbox's view of its proxy identical, and the sandbox is not touched.
func (d *Driver) restoreProxy(ctx context.Context, sandboxID string, allow []string) error {
	if err := d.detachProxy(ctx, sandboxID); err != nil {
		return err
	}
	proxyNS, err := d.attachProxy(ctx, sandboxID)
	if err != nil {
		return err
	}
	return d.startProxy(ctx, sandboxID, proxyNS, allow)
}

// startProxy (re)creates the sandbox's proxy container in its existing namespace with a fresh control token and
// pushes the policy. The namespace keeps its interfaces, so the proxy's addresses and MAC never change.
func (d *Driver) startProxy(ctx context.Context, sandboxID, proxyNS string, allow []string) error {
	if err := d.engine.Delete(ctx, proxyContainerID(sandboxID)); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	if err := d.engine.Create(ctx, proxyPlan(d.cfg, sandboxID, token, proxyNS)); err != nil {
		return errors.Join(fmt.Errorf("starting the egress proxy: %w", err), d.engine.Delete(context.WithoutCancel(ctx), proxyContainerID(sandboxID)))
	}
	state := &proxyState{token: token}
	policy, err := json.Marshal(netpolicy.Policy{SandboxID: sandboxID, Source: sandboxLinkIP, Allow: allow})
	if err != nil {
		return err
	}
	ready, cancel := context.WithTimeout(ctx, proxyReadyTimeout)
	defer cancel()
	for {
		if err = d.control(ready, sandboxID, state, "PUT", "/policy", policy, nil); err == nil {
			break
		}
		select {
		case <-ready.Done():
			return errors.Join(fmt.Errorf("proxy readiness: %w", ready.Err()), d.engine.Delete(context.WithoutCancel(ctx), proxyContainerID(sandboxID)))
		case <-time.After(50 * time.Millisecond):
		}
	}
	d.networkMu.Lock()
	if d.networks == nil {
		d.networks = make(map[string]*proxyState)
	}
	d.networks[sandboxID] = state
	d.networkMu.Unlock()
	return nil
}

func (d *Driver) control(ctx context.Context, sandboxID string, state *proxyState, method, path string, body []byte, out any) error {
	return netpolicy.Control(ctx, func(c context.Context, req runtime.ExecRequest) (runtime.ExecSession, error) {
		return d.engine.Exec(c, proxyContainerID(sandboxID), req)
	}, state.token, method, path, body, out)
}

type decisionBatch struct {
	Events  []netpolicy.Decision `json:"events"`
	Dropped uint64               `json:"dropped"`
}

// maxDecisionBatch mirrors the Docker driver's cap on one drain.
const maxDecisionBatch = 1024

func (d *Driver) deliver(batch decisionBatch) error {
	if len(batch.Events) > maxDecisionBatch {
		return errors.New("proxy event batch exceeds cap")
	}
	if d.cfg.Network == nil {
		return nil
	}
	for _, event := range batch.Events {
		if d.cfg.Network.OnDecision != nil {
			d.cfg.Network.OnDecision(event)
		}
	}
	if batch.Dropped > 0 && d.cfg.Network.OnDropped != nil {
		d.cfg.Network.OnDropped(batch.Dropped)
	}
	return nil
}

// DrainDecisions transfers bounded batches of proxy decisions to the configured sink.
func (d *Driver) DrainDecisions(ctx context.Context) error {
	d.networkMu.Lock()
	snapshot := make(map[string]*proxyState, len(d.networks))
	for id, state := range d.networks {
		snapshot[id] = state
	}
	d.networkMu.Unlock()
	for id, state := range snapshot {
		var batch decisionBatch
		if err := d.control(ctx, id, state, "GET", "/decisions", nil, &batch); err != nil {
			return err
		}
		if err := d.deliver(batch); err != nil {
			return err
		}
	}
	return nil
}

// hasNetwork reports whether the sandbox has a namespace the driver built, so Destroy skips the work for
// network-none sandboxes.
func (d *Driver) hasNetwork(sandboxID string) bool {
	if d.cfg.Network == nil || d.host == nil {
		return false
	}
	names, err := d.host.Netns()
	return err != nil || slices.Contains(names, netnsName(containerID(sandboxID))) || slices.Contains(names, netnsName(proxyContainerID(sandboxID)))
}

// removeNetwork tears everything down in dependency order and keeps going past failures, so a partial network
// is never stranded by one error. Missing pieces are success.
func (d *Driver) removeNetwork(ctx context.Context, sandboxID string) error {
	d.networkMu.Lock()
	state := d.networks[sandboxID]
	delete(d.networks, sandboxID)
	d.networkMu.Unlock()
	if state != nil {
		// A final batch, so decisions made just before destruction are not lost. A dead proxy must not block it.
		var batch decisionBatch
		if d.control(ctx, sandboxID, state, "GET", "/decisions", nil, &batch) == nil {
			_ = d.deliver(batch)
		}
	}
	if d.host == nil {
		return d.engine.Delete(ctx, proxyContainerID(sandboxID))
	}
	errs := []error{d.detachProxy(ctx, sandboxID)}
	sandboxNS := filepath.Join(d.cfg.Network.StateDir, "netns", netnsName(containerID(sandboxID)))
	if conf, err := linkConfig(sandboxID, sandboxLinkIP); err == nil {
		errs = append(errs, d.host.CNI(ctx, "DEL", conf, containerID(sandboxID), sandboxNS, "eth0"))
	}
	// The bridge plugin never removes its bridge.
	errs = append(errs, d.host.DeleteLink(bridgeName(sandboxID)), d.host.DeleteNetns(netnsName(containerID(sandboxID))))
	return errors.Join(errs...)
}

// sandboxOfNetns recovers the sandbox id from a namespace or container name of either kind.
func sandboxOfNetns(name string) (string, bool) {
	if id, ok := strings.CutPrefix(name, proxyPrefix); ok {
		return id, sandboxIDPattern.MatchString(id)
	}
	return sandboxFromContainer(name)
}

// pruneNetworks runs at startup before admission. Networks of kept allowlist sandboxes keep their namespaces and
// get a new proxy (its token died with the old process); everything else this installation left behind goes.
func (d *Driver) pruneNetworks(ctx context.Context, keep map[string]bool) error {
	if d.cfg.Network == nil || d.host == nil {
		return nil
	}
	containers, err := d.engine.List(ctx)
	if err != nil {
		return err
	}
	allowFor := map[string][]string{}
	for _, c := range containers {
		sandbox, ok := sandboxFromContainer(c.ID)
		if !ok || !keep[sandbox] || c.Labels["arcana.managed"] != "true" || c.Labels["arcana.instance"] != d.cfg.InstallID || c.Labels["arcana.network"] != "allowlist" {
			continue
		}
		var allow []string
		if err = json.Unmarshal([]byte(c.Labels["arcana.allow"]), &allow); err != nil {
			return fmt.Errorf("sandbox %s has an unreadable allowlist label: %w", sandbox, err)
		}
		allowFor[sandbox] = allow
	}
	names, err := d.host.Netns()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range names {
		sandbox, ok := sandboxOfNetns(name)
		if !ok || seen[sandbox] {
			continue
		}
		seen[sandbox] = true
		if _, wanted := allowFor[sandbox]; !wanted {
			if err = d.removeNetwork(ctx, sandbox); err != nil {
				return err
			}
		}
	}
	for sandbox, allow := range allowFor {
		if !slices.Contains(names, netnsName(proxyContainerID(sandbox))) && !slices.Contains(names, netnsName(containerID(sandbox))) {
			return fmt.Errorf("sandbox %s lost its network namespaces", sandbox)
		}
		if err = d.restoreProxy(ctx, sandbox, allow); err != nil {
			return err
		}
	}
	// Proxy containers whose sandbox is gone (a crash after the sandbox container went, before the proxy did).
	for _, c := range containers {
		sandbox, ok := sandboxOfNetns(c.ID)
		if !ok || !strings.HasPrefix(c.ID, proxyPrefix) || c.Labels["arcana.proxy"] != "true" || c.Labels["arcana.instance"] != d.cfg.InstallID {
			continue
		}
		if _, wanted := allowFor[sandbox]; !wanted {
			if err = d.removeNetwork(ctx, sandbox); err != nil {
				return err
			}
		}
	}
	return nil
}

// withoutDevice drops the routes of one interface from /proc/net/route data. Our own egress bridge's connected
// route legitimately covers the egress subnet once the first allowlist sandbox has run.
func withoutDevice(routes, device string) string {
	lines := strings.Split(routes, "\n")
	kept := lines[:0]
	for i, line := range lines {
		if fields := strings.Fields(line); i > 0 && len(fields) > 0 && fields[0] == device {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// checkNetwork verifies what allowlist networking needs, for `arcana doctor`.
func (d *Driver) checkNetwork(ctx context.Context) (string, error) {
	n := d.cfg.Network
	if geteuid() != 0 {
		return "", errNeedsRoot
	}
	for _, plugin := range []string{"bridge", "host-local", "static"} {
		info, err := os.Stat(filepath.Join(n.CNIBinDir, plugin))
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("CNI plugin %q is not an executable file in %s (install the containernetworking plugins, or set runtime.containerd.cni_bin_dir)", plugin, n.CNIBinDir)
		}
	}
	if !d.engine.HasImage(ctx, n.ProxyImage) {
		return "", fmt.Errorf("proxy image %s is not in containerd namespace %s (import it with `ctr -n %s images import`)", n.ProxyImage, d.cfg.Namespace, d.cfg.Namespace)
	}
	subnet := netip.MustParsePrefix(n.EgressSubnet)
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Name == egressBridge {
			continue // our own bridge legitimately holds an address in the subnet
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if prefix, parseErr := netip.ParsePrefix(addr.String()); parseErr == nil && prefix.Masked().Overlaps(subnet) {
				return "", fmt.Errorf("egress subnet %s overlaps %s on %s (set runtime.containerd.egress_subnet)", subnet, prefix, iface.Name)
			}
		}
	}
	if data, readErr := os.ReadFile("/proc/net/route"); readErr == nil {
		if routes, routeErr := netpolicy.HostRoutes(withoutDevice(string(data), egressBridge)); routeErr == nil {
			for _, route := range routes {
				if route.Overlaps(subnet) {
					return "", fmt.Errorf("egress subnet %s overlaps the host route %s (set runtime.containerd.egress_subnet)", subnet, route)
				}
			}
		}
	}
	return "allowlist networking: CNI plugins in " + n.CNIBinDir + ", egress subnet " + n.EgressSubnet + ", proxy image " + n.ProxyImage, nil
}
