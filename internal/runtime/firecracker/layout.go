package firecracker

import (
	"path/filepath"
	"strconv"
)

// layout maps one VM onto the filesystem. Jailed VMs see only their chroot, so the paths
// given to Firecracker differ from the host paths of the same files.
type layout struct {
	Jailed  bool
	VMDir   string // metadata and, unjailed, every VM file
	Root    string // host directory that is the VMM's filesystem root
	APISock string // host path
	// VsockUDS is the host path of Firecracker's vsock unix socket.
	VsockUDS string
	// Paths as the VMM resolves them.
	Kernel, Rootfs, Workspace, Vsock string
	// Host files created or linked into Root before the VMM starts.
	WorkspaceHost, KernelHost, RootfsHost string
}

func layoutFor(cfg Config, id string) layout {
	l := layout{Jailed: cfg.JailerBinary != "", VMDir: filepath.Join(cfg.StateDir, id)}
	if !l.Jailed {
		l.Root = l.VMDir
		l.APISock = filepath.Join(l.Root, "api.sock")
		l.Kernel, l.Rootfs, l.Workspace, l.Vsock = cfg.KernelImage, "", filepath.Join(l.Root, "workspace.ext4"), filepath.Join(l.Root, "v.sock")
		l.VsockUDS = l.Vsock
		l.WorkspaceHost = l.Workspace
		return l
	}
	l.Root = filepath.Join(cfg.ChrootBaseDir, filepath.Base(cfg.FirecrackerBinary), id, "root")
	l.APISock = filepath.Join(l.Root, "run", "firecracker.socket")
	l.Kernel, l.Rootfs, l.Workspace, l.Vsock = "/kernel", "/rootfs.ext4", "/workspace.ext4", "/v.sock"
	l.VsockUDS = filepath.Join(l.Root, "v.sock")
	l.KernelHost, l.RootfsHost, l.WorkspaceHost = filepath.Join(l.Root, "kernel"), filepath.Join(l.Root, "rootfs.ext4"), filepath.Join(l.Root, "workspace.ext4")
	return l
}

// jailDir is the directory the jailer creates for one VM, which cleanup removes whole.
func (l layout) jailDir() string { return filepath.Dir(l.Root) }

// launchArgv builds the VMM command line. The jailer re-executes Firecracker inside the
// chroot as an unprivileged user with the plan's cgroup limits.
func launchArgv(cfg Config, plan vmPlan, l layout) []string {
	if !l.Jailed {
		return []string{cfg.FirecrackerBinary, "--api-sock", l.APISock, "--id", plan.VMID}
	}
	argv := []string{
		cfg.JailerBinary, "--id", plan.VMID, "--exec-file", cfg.FirecrackerBinary,
		"--uid", strconv.Itoa(cfg.JailerUID), "--gid", strconv.Itoa(cfg.JailerGID),
		"--chroot-base-dir", cfg.ChrootBaseDir, "--cgroup-version", "2",
		"--resource-limit", "no-file=2048",
	}
	for _, limit := range plan.Cgroup {
		argv = append(argv, "--cgroup", limit)
	}
	return argv
}
