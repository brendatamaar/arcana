package containerd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// Allowlist networking (ADR 0010), the Docker driver's design on CNI. Each sandbox gets a network namespace
// whose only interface sits on its own private L2 bridge, and a dedicated egress proxy container is the only
// other thing on that bridge (plus a second interface on a shared NAT'd bridge for outbound traffic). The
// private bridge has no IP and no route on the host, so the sandbox has a link to its proxy and nothing else.
//
// Everything here is pure: names, addresses, CNI configurations and container plans. Nothing touches the host.
const (
	// The private link is link-local, fixed and reused by every sandbox: each sandbox's bridge is a separate
	// L2 segment in separate namespaces, so the addresses never meet. No allocation, no exhaustion.
	linkSubnet     = "169.254.77.0/29"
	sandboxLinkIP  = "169.254.77.2"
	proxyLinkIP    = "169.254.77.3"
	linkPrefixBits = 29
	proxyPort      = "3128"
	egressBridge   = "arcana-egress0"
	egressName     = "arcana-egress"
	cniVersion     = "1.0.0"
	// The proxy is named prx-<id>, not sbx-<id>-proxy: the gVisor shim finds containers by ID prefix, so a proxy id
	// that extends its sandbox's id makes both ambiguous.
	proxyPrefix = "prx-"

	defaultCNIBinDir    = "/opt/cni/bin"
	defaultEgressSubnet = "172.29.0.0/20"
	defaultStateDir     = "/run/arcana"
)

// NetworkConfig enables allowlist networking. Nil means the driver supports network mode none only.
type NetworkConfig struct {
	// ProxyImage is the image running `arcana proxy`; it must already be in the containerd namespace.
	ProxyImage string
	// ProxyDNS lists the resolvers the proxy uses (it never relies on the host's resolver).
	ProxyDNS []string
	// CNIBinDir holds the CNI plugins bridge, host-local and static.
	CNIBinDir string
	// EgressSubnet is the shared outbound bridge's subnet; each proxy takes one address from it.
	EgressSubnet string
	// StateDir holds the network namespaces and IPAM state. Arcana must run as root to use it.
	StateDir   string
	OnDecision func(netpolicy.Decision)
	OnDropped  func(uint64)
}

func (n *NetworkConfig) applyDefaults() {
	if n.CNIBinDir == "" {
		n.CNIBinDir = defaultCNIBinDir
	}
	if n.EgressSubnet == "" {
		n.EgressSubnet = defaultEgressSubnet
	}
	if n.StateDir == "" {
		n.StateDir = defaultStateDir
	}
}

// Validate rejects settings that cannot work before anything is created.
func (n NetworkConfig) Validate() error {
	if n.ProxyImage == "" || len(n.ProxyImage) > 1024 {
		return runtime.Invalid{Field: "network proxy_image"}
	}
	for _, dir := range []string{n.CNIBinDir, n.StateDir} {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			return runtime.Invalid{Field: "network directory " + dir}
		}
	}
	prefix, err := netip.ParsePrefix(n.EgressSubnet)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 8 || prefix.Bits() > 28 || prefix.Masked() != prefix {
		return runtime.Invalid{Field: "egress_subnet (an IPv4 CIDR from /8 to /28)"}
	}
	if len(n.ProxyDNS) == 0 || len(n.ProxyDNS) > 8 {
		return runtime.Invalid{Field: "network proxy_dns"}
	}
	for _, server := range n.ProxyDNS {
		if _, err = netip.ParseAddr(server); err != nil {
			return runtime.Invalid{Field: "network proxy_dns"}
		}
	}
	return nil
}

// netnsName is the namespace (and CNI container) name for a container.
func netnsName(containerID string) string { return containerID }

func proxyContainerID(sandboxID string) string { return proxyPrefix + sandboxID }

// bridgeName is the sandbox's private bridge. Linux limits interface names to 15 bytes: "arc" plus 12 id characters.
func bridgeName(sandboxID string) string {
	return "arc" + strings.TrimPrefix(sandboxID, "sbx_")[:12]
}

// linkMAC is a stable locally administered MAC derived from the address, so a recreated proxy keeps the one
// the sandbox's neighbour cache learned.
func linkMAC(ip string) string {
	a := netip.MustParseAddr(ip).As4()
	return fmt.Sprintf("02:42:%02x:%02x:%02x:%02x", a[0], a[1], a[2], a[3])
}

// cniConfig marshals a CNI plugin configuration; the structs keep the shape reviewable.
type cniConfig struct {
	CNIVersion    string          `json:"cniVersion"`
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Bridge        string          `json:"bridge"`
	IsGateway     bool            `json:"isGateway"`
	IPMasq        bool            `json:"ipMasq"`
	HairpinMode   bool            `json:"hairpinMode"`
	Capabilities  map[string]bool `json:"capabilities,omitempty"`
	RuntimeConfig map[string]any  `json:"runtimeConfig,omitempty"`
	IPAM          any             `json:"ipam"`
}

type staticIPAM struct {
	Type      string          `json:"type"`
	Addresses []staticAddress `json:"addresses"`
}
type staticAddress struct {
	Address string `json:"address"`
}

type hostLocalIPAM struct {
	Type    string          `json:"type"`
	Ranges  [][]rangeSubnet `json:"ranges"`
	Routes  []route         `json:"routes"`
	DataDir string          `json:"dataDir"`
}
type rangeSubnet struct {
	Subnet string `json:"subnet"`
}
type route struct {
	Dst string `json:"dst"`
}

// linkConfig attaches an interface to the sandbox's private bridge at a fixed address and MAC. No routes, no
// gateway, no masquerade: the bridge has no address of its own, so nothing on it leads anywhere else.
func linkConfig(sandboxID, ip string) ([]byte, error) {
	return json.Marshal(cniConfig{
		CNIVersion: cniVersion, Name: "arcana-" + sandboxID, Type: "bridge", Bridge: bridgeName(sandboxID),
		Capabilities: map[string]bool{"mac": true}, RuntimeConfig: map[string]any{"mac": linkMAC(ip)},
		IPAM: staticIPAM{Type: "static", Addresses: []staticAddress{{Address: fmt.Sprintf("%s/%d", ip, linkPrefixBits)}}},
	})
}

// egressConfig attaches a proxy to the shared outbound bridge: a gateway, a default route and masquerade,
// like Docker's default bridge.
func egressConfig(n NetworkConfig) ([]byte, error) {
	return json.Marshal(cniConfig{
		CNIVersion: cniVersion, Name: egressName, Type: "bridge", Bridge: egressBridge, IsGateway: true, IPMasq: true,
		IPAM: hostLocalIPAM{Type: "host-local", Ranges: [][]rangeSubnet{{{Subnet: n.EgressSubnet}}}, Routes: []route{{Dst: "0.0.0.0/0"}}, DataDir: filepath.Join(n.StateDir, "cni")},
	})
}

// proxyURL is where a sandbox reaches its proxy.
func proxyURL() string { return "http://" + net.JoinHostPort(proxyLinkIP, proxyPort) }

// withProxyEnvironment replaces any proxy variables the sandbox was given with the ones that reach its proxy.
func withProxyEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+6)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToLower(name) {
		case "http_proxy", "https_proxy", "no_proxy":
			continue
		}
		out = append(out, entry)
	}
	url := proxyURL()
	return append(out, "HTTP_PROXY="+url, "HTTPS_PROXY="+url, "NO_PROXY=", "http_proxy="+url, "https_proxy="+url, "no_proxy=")
}

// withNetns makes a container join an existing network namespace instead of creating its own.
func withNetns(path string) oci.SpecOpts {
	return oci.WithLinuxNamespace(specs.LinuxNamespace{Type: specs.NetworkNamespace, Path: path})
}

// proxyPlan is the egress proxy container: the arcana binary in `proxy` mode with the same hardening as a
// sandbox, no workspace and no helper. Its control token is kept only as a hash in its environment.
func proxyPlan(cfg Config, sandboxID, token string, netns string) plan {
	hash := sha256.Sum256([]byte(token))
	id := proxyContainerID(sandboxID)
	uid, gid := fmt.Sprintf("uid=%d", sandboxUID), fmt.Sprintf("gid=%d", sandboxGID)
	memory := 256 * megabyte
	pids := int64(64)
	opts := []oci.SpecOpts{
		oci.WithUIDGID(sandboxUID, sandboxGID),
		oci.WithNoNewPrivileges,
		oci.WithCapabilities([]string{}),
		oci.WithRootFSReadonly(),
		oci.WithProcessCwd("/"),
		oci.WithMemoryLimit(uint64(memory)), // #nosec G115 -- constant
		oci.WithMemorySwap(memory),
		oci.WithCPUCFS(int64(cpuPeriod/2), cpuPeriod),
		oci.WithPidsLimit(pids),
		oci.WithMounts([]specs.Mount{
			{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "size=16m"}},
			{Destination: "/run/control", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", uid, gid, "mode=0700", "size=4m"}},
		}),
		oci.WithCgroup("/arcana-" + id),
		withNetns(netns),
	}
	return plan{
		ID: id, Image: cfg.Network.ProxyImage, Args: []string{"/arcana", "proxy"}, SpecOpts: opts, Workdir: "/",
		Env: []string{"ARCANA_PROXY_TOKEN_HASH=" + hex.EncodeToString(hash[:]), "ARCANA_PROXY_DNS=" + strings.Join(cfg.Network.ProxyDNS, ",")},
		Labels: map[string]string{
			"arcana.managed": "true", "arcana.instance": cfg.InstallID, "arcana.sandbox_id": sandboxID, "arcana.proxy": "true",
		},
	}
}
