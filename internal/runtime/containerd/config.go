// Package containerd implements runtime.Driver directly on containerd, with the gVisor
// (runsc) shim by default, instead of through the Docker Engine (ADR 0009). It has not
// been run against a real containerd: the host it was built on has no accessible
// containerd socket.
package containerd

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// Config selects the containerd endpoint, namespace and OCI runtime.
type Config struct {
	// Address is containerd's socket. It is root-equivalent, like the Docker socket.
	Address string
	// Namespace isolates Arcana's containers and images. Template images must be present
	// in it (for example `ctr -n arcana images pull`).
	Namespace string
	// OCIRuntime is runsc (the default) or runc. runc shares the host kernel and needs
	// AllowInsecureRuntime, exactly as with the Docker driver.
	OCIRuntime           string
	AllowInsecureRuntime bool
	InstallID            string
	// HelperPath is the host's sbx-exec, bind-mounted read-only into every sandbox.
	HelperPath string
}

var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func (c *Config) applyDefaults() {
	if c.Address == "" {
		c.Address = "/run/containerd/containerd.sock"
	}
	if c.Namespace == "" {
		c.Namespace = "arcana"
	}
	if c.OCIRuntime == "" {
		c.OCIRuntime = "runsc"
	}
}

// shimName is the containerd runtime for the selected OCI runtime.
func (c Config) shimName() string {
	if c.OCIRuntime == "runc" {
		return "io.containerd.runc.v2"
	}
	return "io.containerd.runsc.v1"
}

// Validate rejects unsafe settings before containerd is contacted.
func (c Config) Validate() error {
	if c.OCIRuntime != "runsc" && c.OCIRuntime != "runc" {
		return runtime.Invalid{Field: "oci_runtime"}
	}
	if c.OCIRuntime == "runc" && !c.AllowInsecureRuntime {
		return runtime.Invalid{Field: "allow_insecure_runtime"}
	}
	if c.InstallID == "" || len(c.InstallID) > 128 {
		return runtime.Invalid{Field: "install_id"}
	}
	if !namespacePattern.MatchString(c.Namespace) {
		return runtime.Invalid{Field: "namespace"}
	}
	if !filepath.IsAbs(c.Address) || filepath.Clean(c.Address) != c.Address {
		return runtime.Invalid{Field: "address"}
	}
	if !filepath.IsAbs(c.HelperPath) {
		return runtime.Invalid{Field: "helper_path"}
	}
	info, err := os.Stat(c.HelperPath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(runtime.Invalid{Field: "helper_path regular file"}, err)
	}
	return nil
}
