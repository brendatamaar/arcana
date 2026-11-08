package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// NetworkConfig supplies operator-controlled networking and decision event sinks.
type NetworkConfig struct {
	SubnetPool   string
	SubnetPrefix int
	ProxyImage   string
	ProxyDNS     []string
	DataDir      string
	OnDecision   func(netpolicy.Decision)
	OnDropped    func(uint64)
}

type networkState struct {
	id     string
	subnet netip.Prefix
	token  string
}

func (d *Driver) createNetwork(ctx context.Context, spec runtime.Spec) (*networkState, error) {
	if d.config.Network == nil {
		return nil, runtime.Invalid{Field: "allowlist networking is not configured"}
	}
	networks, err := d.client.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	if len(networks.Items) > 10000 {
		return nil, errors.New("too many Docker networks")
	}
	used := make([]netip.Prefix, 0, len(networks.Items))
	for _, item := range networks.Items {
		for _, ipam := range item.IPAM.Config {
			if prefix, err := netip.ParsePrefix(ipam.Subnet.String()); err == nil {
				used = append(used, prefix)
			}
		}
	}
	pool, err := netip.ParsePrefix(d.config.Network.SubnetPool)
	if err != nil {
		return nil, err
	}
	subnet, err := netpolicy.Allocate(pool, d.config.Network.SubnetPrefix, used)
	if err != nil {
		return nil, err
	}
	labels := d.labels(spec)
	allow, _ := json.Marshal(spec.NetworkAllow)
	labels["arcana.allow"] = string(allow)
	labels["arcana.subnet"] = subnet.String()
	result, err := d.client.NetworkCreate(ctx, sandboxName(spec.SandboxID)+"-net", client.NetworkCreateOptions{Driver: "bridge", Internal: true, IPAM: &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}}, Labels: labels})
	if err != nil {
		return nil, err
	}
	state, err := d.startProxy(ctx, spec.SandboxID, subnet, spec.NetworkAllow)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		return nil, errors.Join(err, d.removeProxy(cleanup, spec.SandboxID), func() error {
			_, e := d.client.NetworkRemove(cleanup, result.ID, client.NetworkRemoveOptions{})
			return e
		}())
	}
	return state, nil
}

func address(subnet netip.Prefix, offset int) string {
	ip := subnet.Addr()
	for range offset {
		ip = ip.Next()
	}
	return ip.String()
}

func (d *Driver) startProxy(ctx context.Context, id string, subnet netip.Prefix, allow []string) (*networkState, error) {
	if err := d.removeProxy(ctx, id); err != nil {
		return nil, err
	}
	cfg := d.config.Network
	if cfg == nil {
		return nil, errors.New("proxy configuration required to restore allowlist sandbox")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	spec := runtime.Spec{SandboxID: id}
	labels := d.labels(spec)
	labels["arcana.proxy"] = "true"
	pids := int64(64)
	initProcess := true
	options := client.ContainerCreateOptions{Name: sandboxName(id) + "-proxy", Config: &container.Config{Image: cfg.ProxyImage, User: "1000:1000", Entrypoint: []string{"/arcana"}, Cmd: []string{"proxy"}, Env: []string{"ARCANA_PROXY_TOKEN_HASH=" + hex.EncodeToString(hash[:]), "ARCANA_PROXY_DNS=" + strings.Join(cfg.ProxyDNS, ",")}, Labels: labels}, HostConfig: &container.HostConfig{Runtime: d.config.OCIRuntime, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Init: &initProcess, NetworkMode: "bridge", Tmpfs: map[string]string{"/tmp": "size=16m,nosuid,nodev", "/run/control": "size=4m,nosuid,nodev,uid=1000,gid=1000,mode=0700"}, Resources: container.Resources{Memory: 256 * megabyte, MemorySwap: 256 * megabyte, NanoCPUs: 500000000, PidsLimit: &pids}, LogConfig: container.LogConfig{Type: "none"}}}
	created, err := d.client.ContainerCreate(ctx, options)
	if err != nil {
		return nil, err
	}
	_, err = d.client.NetworkConnect(ctx, sandboxName(id)+"-net", client.NetworkConnectOptions{Container: created.ID, EndpointConfig: &network.EndpointSettings{MacAddress: proxyMAC(subnet), IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: netip.MustParseAddr(address(subnet, 3))}}})
	if err != nil {
		return nil, err
	}
	if _, err = d.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, err
	}
	state := &networkState{id: id, subnet: subnet, token: token}
	policy := netpolicy.Policy{SandboxID: id, Source: address(subnet, 2), Allow: allow}
	data, _ := json.Marshal(policy)
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if err = d.control(ready, state, "PUT", "/policy", data, nil); err == nil {
			break
		}
		select {
		case <-ready.Done():
			return nil, fmt.Errorf("proxy readiness: %w", ready.Err())
		case <-d.config.Clock.After(50 * time.Millisecond):
		}
	}
	d.networkMu.Lock()
	d.networks[id] = state
	d.networkMu.Unlock()
	return state, nil
}

func (d *Driver) control(ctx context.Context, state *networkState, method, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := d.Exec(ctx, sandboxName(state.id)+"-proxy", runtime.ExecRequest{Cmd: []string{"/arcana", "proxy-control"}})
	if err != nil {
		return err
	}
	var output bytes.Buffer
	var group errgroup.Group
	group.Go(func() error { _, err := io.Copy(&output, io.LimitReader(session.Stdout(), (1<<20)+1)); return err })
	group.Go(func() error { _, err := io.Copy(io.Discard, session.Stderr()); return err })
	data, _ := json.Marshal(struct {
		Token, Method, Path string
		Body                []byte
	}{state.token, method, path, body})
	_, writeErr := session.Stdin().Write(data)
	closeErr := session.Stdin().Close()
	result, waitErr := session.Wait(ctx)
	drainErr := group.Wait()
	if err = errors.Join(writeErr, closeErr, waitErr, drainErr); err != nil {
		return err
	}
	if result.ExitCode != 0 || output.Len() > 1<<20 {
		return errors.New("proxy control failed")
	}
	if out != nil {
		return json.Unmarshal(output.Bytes(), out)
	}
	return nil
}

// DrainDecisions transfers bounded proxy decision batches to the injected sink.
func (d *Driver) DrainDecisions(ctx context.Context) error {
	d.networkMu.Lock()
	states := make([]*networkState, 0, len(d.networks))
	for _, state := range d.networks {
		states = append(states, state)
	}
	d.networkMu.Unlock()
	for _, state := range states {
		var batch struct {
			Events  []netpolicy.Decision `json:"events"`
			Dropped uint64               `json:"dropped"`
		}
		if err := d.control(ctx, state, "GET", "/decisions", nil, &batch); err != nil {
			return err
		}
		if len(batch.Events) > 1024 {
			return errors.New("proxy event batch exceeds cap")
		}
		if d.config.Network.OnDecision != nil {
			for _, event := range batch.Events {
				d.config.Network.OnDecision(event)
			}
		}
		if batch.Dropped > 0 && d.config.Network.OnDropped != nil {
			d.config.Network.OnDropped(batch.Dropped)
		}
	}
	return nil
}

func (d *Driver) removeProxy(ctx context.Context, id string) error {
	name := sandboxName(id) + "-proxy"
	found, err := d.client.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	labels := found.Container.Config.Labels
	if !d.ownsResource(labels, id) || labels["arcana.proxy"] != "true" {
		return errors.New("proxy ownership mismatch")
	}
	_, err = d.client.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	return err
}

func (d *Driver) removeNetwork(ctx context.Context, id string) error {
	d.networkMu.Lock()
	state := d.networks[id]
	delete(d.networks, id)
	d.networkMu.Unlock()
	if state != nil {
		if err := d.drainOne(ctx, state); err != nil {
			return err
		}
	}
	if err := d.removeProxy(ctx, id); err != nil {
		return err
	}
	name := sandboxName(id) + "-net"
	found, err := d.client.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !d.ownsResource(found.Network.Labels, id) {
		return errors.New("network ownership mismatch")
	}
	_, err = d.client.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}

	return err
}

// drainOne flushes a final batch before destroying a healthy proxy.
func (d *Driver) drainOne(ctx context.Context, state *networkState) error {
	var batch struct {
		Events  []netpolicy.Decision `json:"events"`
		Dropped uint64               `json:"dropped"`
	}
	if err := d.control(ctx, state, "GET", "/decisions", nil, &batch); err != nil {
		return nil
	} // A stopped proxy must not block fail-closed destruction.
	if len(batch.Events) > 1024 {
		return errors.New("proxy event batch exceeds cap")
	}
	if batch.Dropped > 0 && d.config.Network != nil && d.config.Network.OnDropped != nil {
		d.config.Network.OnDropped(batch.Dropped)
	}
	if d.config.Network != nil && d.config.Network.OnDecision != nil {
		for _, event := range batch.Events {
			d.config.Network.OnDecision(event)
		}
	}
	return nil
}

func proxyMAC(subnet netip.Prefix) network.HardwareAddr {
	ip := netip.MustParseAddr(address(subnet, 3)).As4()
	return network.HardwareAddr{2, 66, ip[0], ip[1], ip[2], ip[3]}
}
