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
	// FIFODir holds the named pipes of exec sessions. Both Arcana and containerd's shim open them, so
	// it must be a path both can reach; the containerd default, /run/containerd/fifo, is root-only,
	// which a non-root Arcana cannot use. Created with mode 0700 if missing.
	FIFODir string
	// WorkspaceDir holds one host directory per sandbox, bind-mounted at /workspace for the volume
	// backend. Empty means only the tmpfs backend is available. The directory is created 0700.
	WorkspaceDir string
	// Network enables allowlist networking (needs root, CNI plugins and the proxy image); nil supports
	// network mode none only.
	Network *NetworkConfig
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
	if c.FIFODir == "" {
		c.FIFODir = "/run/containerd/fifo"
	}
	if c.Network != nil {
		c.Network.applyDefaults()
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
	if !filepath.IsAbs(c.FIFODir) || filepath.Clean(c.FIFODir) != c.FIFODir {
		return runtime.Invalid{Field: "fifo_dir"}
	}
	if c.WorkspaceDir != "" && (!filepath.IsAbs(c.WorkspaceDir) || filepath.Clean(c.WorkspaceDir) != c.WorkspaceDir) {
		return runtime.Invalid{Field: "workspace_dir"}
	}
	if c.Network != nil {
		if err := c.Network.Validate(); err != nil {
			return err
		}
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
