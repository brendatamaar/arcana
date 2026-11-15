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
		return containerd.New(containerd.Config{Address: k.Address, Namespace: k.Namespace, OCIRuntime: k.OCIRuntime, AllowInsecureRuntime: k.AllowInsecureRuntime, InstallID: o.InstallID, HelperPath: o.HelperPath, FIFODir: k.FIFODir})
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
