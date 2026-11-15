// Package firecracker implements runtime.Driver on Firecracker microVMs. Each sandbox is
// a VM whose commands and files are served by the in-guest envd daemon over vsock
// (ADR 0006, ADR 0007). It needs /dev/kvm and has not been run on real KVM yet.
package firecracker

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// Config selects binaries, images and directories. Every path is absolute.
type Config struct {
	FirecrackerBinary string
	// JailerBinary confines each VMM process (chroot, namespaces, cgroups, a dropped
	// UID). It is required unless AllowUnjailed is set, which is for development only.
	JailerBinary  string
	AllowUnjailed bool
	KernelImage   string
	// KernelArgs are appended to the fixed hardening arguments.
	KernelArgs string
	// RootfsDir holds one read-only ext4 image per template image, named
	// <sanitised image>.ext4 (scripts/build-rootfs.sh creates them).
	RootfsDir string
	// StateDir keeps per-VM metadata and workspace images. ChrootBaseDir holds jails and
	// must be on the same filesystem as KernelImage and RootfsDir, because jailed VMs
	// get hard links to them.
	StateDir      string
	ChrootBaseDir string
	// JailerUID and JailerGID are the unprivileged identity the VMM runs as.
	JailerUID, JailerGID int
	VsockPort            uint32
	BootTimeout          time.Duration
	InstallID            string
	// MkfsBinary creates the workspace filesystem; default mke2fs.
	MkfsBinary string
}

const defaultVsockPort = 1024

func (c *Config) applyDefaults() {
	if c.VsockPort == 0 {
		c.VsockPort = defaultVsockPort
	}
	if c.BootTimeout == 0 {
		c.BootTimeout = 30 * time.Second
	}
	if c.ChrootBaseDir == "" && c.StateDir != "" {
		c.ChrootBaseDir = filepath.Join(c.StateDir, "jail")
	}
	if c.MkfsBinary == "" {
		c.MkfsBinary = "/usr/sbin/mke2fs"
	}
}

// Validate rejects unsafe or incomplete settings before any VM is started.
func (c Config) Validate() error {
	for name, path := range map[string]string{
		"firecracker_binary": c.FirecrackerBinary, "kernel_image": c.KernelImage, "rootfs_dir": c.RootfsDir,
		"state_dir": c.StateDir, "chroot_base_dir": c.ChrootBaseDir, "mkfs_binary": c.MkfsBinary,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return runtime.Invalid{Field: name}
		}
	}
	if c.InstallID == "" || len(c.InstallID) > 128 {
		return runtime.Invalid{Field: "install_id"}
	}
	if c.JailerBinary == "" && !c.AllowUnjailed {
		return errors.Join(runtime.Invalid{Field: "jailer_binary"}, errors.New("running Firecracker without the jailer requires allow_unjailed"))
	}
	if c.JailerBinary != "" && (!filepath.IsAbs(c.JailerBinary) || filepath.Clean(c.JailerBinary) != c.JailerBinary) {
		return runtime.Invalid{Field: "jailer_binary"}
	}
	if c.JailerBinary != "" && (c.JailerUID <= 0 || c.JailerGID <= 0) {
		return errors.Join(runtime.Invalid{Field: "jailer_uid"}, errors.New("the VMM must run as an unprivileged, non-root identity"))
	}
	if c.VsockPort == 0 || c.VsockPort > 1<<31 {
		return runtime.Invalid{Field: "vsock_port"}
	}
	if c.BootTimeout < time.Second || c.BootTimeout > 5*time.Minute {
		return runtime.Invalid{Field: "boot_timeout"}
	}
	if len(c.KernelArgs) > 512 {
		return fmt.Errorf("%w: kernel_args exceeds 512 bytes", runtime.Invalid{Field: "kernel_args"})
	}
	return nil
}
