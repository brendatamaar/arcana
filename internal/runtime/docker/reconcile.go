package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// PruneOrphans removes only this installation's unreferenced labelled resources.
// It runs before request admission, so the keep set cannot race with creation.
func (d *Driver) PruneOrphans(ctx context.Context, keep map[string]bool) error {
	filters := make(client.Filters).Add("label", "arcana.managed=true", "arcana.instance="+d.config.InstallID)
	volumes, err := d.client.VolumeList(ctx, client.VolumeListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("listing orphan volumes: %w", err)
	}
	if len(volumes.Items) > maxManaged {
		return fmt.Errorf("managed volume limit exceeded")
	}
	for _, volume := range volumes.Items {
		id := volume.Labels["arcana.sandbox_id"]
		if !d.ownsResource(volume.Labels, id) || keep[id] || volume.Name != sandboxName(id)+"-ws" {
			continue
		}
		if _, err = d.client.VolumeRemove(ctx, volume.Name, client.VolumeRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("removing orphan volume: %w", err)
		}
		if d.config.OnOrphan != nil {
			d.config.OnOrphan("volume")
		}
	}
	networks, err := d.client.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		return fmt.Errorf("listing orphan networks: %w", err)
	}
	if len(networks.Items) > maxManaged {
		return fmt.Errorf("managed network limit exceeded")
	}
	for _, network := range networks.Items {
		id := network.Labels["arcana.sandbox_id"]
		if !d.ownsResource(network.Labels, id) || network.Name != sandboxName(id)+"-net" {
			continue
		}
		if keep[id] {
			subnet, err := netip.ParsePrefix(network.Labels["arcana.subnet"])
			if err != nil {
				return err
			}
			var allow []string
			if err = json.Unmarshal([]byte(network.Labels["arcana.allow"]), &allow); err != nil {
				return err
			}
			if _, err = d.startProxy(ctx, id, subnet, allow); err != nil {
				return err
			}
			continue
		}
		if err = d.removeProxy(ctx, id); err != nil {
			return err
		}
		if _, err = d.client.NetworkRemove(ctx, network.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("removing orphan network: %w", err)
		}
		if d.config.OnOrphan != nil {
			d.config.OnOrphan("network")
		}
	}
	if d.config.Network == nil {
		return nil
	}
	proxies, err := d.client.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: maxManaged + 1, Filters: make(client.Filters).Add("label", "arcana.managed=true", "arcana.instance="+d.config.InstallID, "arcana.proxy=true")})
	if err != nil {
		return err
	}
	if len(proxies.Items) > maxManaged {
		return fmt.Errorf("proxy count exceeds cap")
	}
	for _, proxy := range proxies.Items {
		id := proxy.Labels["arcana.sandbox_id"]
		if !keep[id] && d.ownsResource(proxy.Labels, id) {
			if err = d.removeProxy(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Driver) ownsResource(labels map[string]string, id string) bool {
	return validSandboxID(id) && labels["arcana.managed"] == "true" && labels["arcana.instance"] == d.config.InstallID
}
