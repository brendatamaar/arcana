//go:build !linux

package factory

import (
	"fmt"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
)

// New builds the Docker driver; the containerd and Firecracker drivers exist only on Linux.
func New(cfg config.Config, o Options) (Driver, error) {
	if cfg.Runtime.Driver != "docker" {
		return nil, fmt.Errorf("the %s runtime driver is available only on Linux", cfg.Runtime.Driver)
	}
	c := docker.Config{Host: cfg.Runtime.Docker.Host, OCIRuntime: cfg.Runtime.Docker.OCIRuntime, AllowInsecureRuntime: cfg.Runtime.Docker.AllowInsecureRuntime, InstallID: o.InstallID, HelperPath: o.HelperPath}
	if o.Docker != nil {
		o.Docker(&c)
	}
	return docker.New(c)
}
