//go:build linux

package factory

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/runtime/containerd"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/runtime/firecracker"
)

// New builds the configured driver. It validates settings but does not start sandboxes.
func New(cfg config.Config, o Options) (Driver, error) {
	switch cfg.Runtime.Driver {
	case "docker":
		c := docker.Config{Host: cfg.Runtime.Docker.Host, OCIRuntime: cfg.Runtime.Docker.OCIRuntime, AllowInsecureRuntime: cfg.Runtime.Docker.AllowInsecureRuntime, InstallID: o.InstallID, HelperPath: o.HelperPath}
		if o.Docker != nil {
			o.Docker(&c)
		}
		return docker.New(c)
	case "containerd":
		k := cfg.Runtime.Containerd
		c := containerd.Config{Address: k.Address, Namespace: k.Namespace, OCIRuntime: k.OCIRuntime, AllowInsecureRuntime: k.AllowInsecureRuntime, InstallID: o.InstallID, HelperPath: o.HelperPath, FIFODir: k.FIFODir}
		if cfg.Workspace.Backend == "volume" {
			// Only a volume backend needs (and so checks for) the host directory and the privileges to own it.
			c.WorkspaceDir = filepath.Join(cfg.DataDir, "containerd", "workspaces")
		}
		if k.Allowlist {
			c.Network = &containerd.NetworkConfig{
				ProxyImage: cfg.Network.ProxyImage, ProxyDNS: cfg.Network.ProxyDNS, CNIBinDir: k.CNIBinDir, EgressSubnet: k.EgressSubnet,
				StateDir: k.StateDir, OnDecision: o.OnDecision, OnDropped: o.OnDropped,
			}
		}
		return containerd.New(c)
	case "firecracker":
		f := cfg.Runtime.Firecracker
		root := filepath.Join(cfg.DataDir, "firecracker")
		return firecracker.New(firecracker.Config{
			FirecrackerBinary: f.FirecrackerBinary, JailerBinary: f.JailerBinary, AllowUnjailed: f.AllowUnjailed,
			KernelImage: f.KernelImage, KernelArgs: f.KernelArgs, RootfsDir: f.RootfsDir,
			StateDir: filepath.Join(root, "state"), ChrootBaseDir: filepath.Join(root, "jail"),
			JailerUID: f.JailerUID, JailerGID: f.JailerGID, VsockPort: uint32(f.VsockPort), // #nosec G115 -- validated 1..2^31
			BootTimeout: time.Duration(f.BootTimeoutSeconds) * time.Second, InstallID: o.InstallID,
		})
	default:
		return nil, fmt.Errorf("unknown runtime driver %q", cfg.Runtime.Driver)
	}
}
