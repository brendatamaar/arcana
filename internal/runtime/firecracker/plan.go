package firecracker

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/brendatamaar/arcana/internal/runtime"
)

const (
	// vmmOverheadMiB is memory the VMM itself needs beyond the guest's RAM; the cgroup
	// limit includes it so the guest can use all of MemoryMB.
	vmmOverheadMiB = 128
	// vmmThreads bounds the VMM's own threads in the jail's pids cgroup.
	vmmThreads  = 64
	maxVCPUs    = 32
	minMemoryMB = 128
	maxMemoryMB = 1 << 20
	maxDiskMB   = 1 << 20
	guestCID    = 3
	cpuPeriod   = 100000
)

var sandboxIDPattern = regexp.MustCompile(`^sbx_[a-z2-7]{20}$`)

// vmPlan is everything decided about one VM before any effect happens.
type vmPlan struct {
	VMID       string
	SandboxID  string
	VCPUs      int
	MemoryMiB  int
	DiskMiB    int
	TmpfsSpace bool // /workspace is guest tmpfs instead of a disk image
	Rootfs     string
	KernelArgs string
	// Cgroup limits applied by the jailer to the VMM process (cgroup v2 file=value).
	Cgroup []string
	PIDs   int64
}

// imageFile maps a template image reference to its rootfs file name.
func imageFile(image string) string {
	var b strings.Builder
	for _, r := range image {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ".ext4"
}

func vmID(sandboxID string) string { return "sbx-" + strings.ReplaceAll(sandboxID, "_", "-") }

// planVM validates a spec and derives the VM shape. CPU quotas below a whole vCPU are
// enforced by the jailer's cpu.max, because a VM can only have whole vCPUs.
func planVM(cfg Config, spec runtime.Spec) (vmPlan, error) {
	if !sandboxIDPattern.MatchString(spec.SandboxID) {
		return vmPlan{}, runtime.Invalid{Field: "sandbox_id"}
	}
	if spec.Image == "" || len(spec.Image) > 1024 {
		return vmPlan{}, runtime.Invalid{Field: "image"}
	}
	if spec.NetworkMode != "" && spec.NetworkMode != "none" {
		return vmPlan{}, runtime.Unsupported{Feature: "firecracker network mode " + spec.NetworkMode}
	}
	if spec.WorkspaceBackend != "" && spec.WorkspaceBackend != "volume" && spec.WorkspaceBackend != "tmpfs" {
		return vmPlan{}, runtime.Invalid{Field: "workspace_backend"}
	}
	r := spec.Resources
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 0.01 || r.CPUs > maxVCPUs {
		return vmPlan{}, runtime.Invalid{Field: "cpus"}
	}
	if r.MemoryMB < minMemoryMB || r.MemoryMB > maxMemoryMB {
		return vmPlan{}, runtime.Invalid{Field: "memory_mb"}
	}
	if r.DiskMB <= 0 || r.DiskMB > maxDiskMB {
		return vmPlan{}, runtime.Invalid{Field: "disk_mb"}
	}
	if r.PIDs <= 0 || r.PIDs > 1<<22 {
		return vmPlan{}, runtime.Invalid{Field: "pids"}
	}
	vcpus := max(1, int(math.Ceil(r.CPUs)))
	plan := vmPlan{
		VMID: vmID(spec.SandboxID), SandboxID: spec.SandboxID, VCPUs: vcpus,
		MemoryMiB: int(r.MemoryMB), DiskMiB: int(r.DiskMB), TmpfsSpace: spec.WorkspaceBackend == "tmpfs",
		Rootfs: filepath.Join(cfg.RootfsDir, imageFile(spec.Image)), PIDs: r.PIDs,
	}
	quota := int(math.Round(r.CPUs * cpuPeriod))
	plan.Cgroup = []string{
		fmt.Sprintf("cpu.max=%d %d", quota, cpuPeriod),
		fmt.Sprintf("memory.max=%d", (int64(plan.MemoryMiB)+vmmOverheadMiB)*1024*1024),
		"memory.swap.max=0",
		fmt.Sprintf("pids.max=%d", vmmThreads+vcpus),
	}
	plan.KernelArgs = kernelArgs(cfg, plan)
	return plan, nil
}

// kernelArgs fixes the hardening-relevant boot arguments; operator args come last but
// cannot be used to pass the arcana.* settings the init reads.
func kernelArgs(cfg Config, plan vmPlan) string {
	args := []string{
		"console=ttyS0", "quiet", "reboot=k", "panic=1", "pci=off", "nomodules",
		"root=/dev/vda", "ro", "rootfstype=ext4", "init=/sbin/arcana-init",
		"arcana.vsock_port=" + strconv.FormatUint(uint64(cfg.VsockPort), 10),
		"arcana.pids=" + strconv.FormatInt(plan.PIDs, 10),
		"arcana.nofile=1024",
	}
	if plan.TmpfsSpace {
		args = append(args, "arcana.workspace=tmpfs:"+strconv.Itoa(plan.DiskMiB)+"m")
	} else {
		args = append(args, "arcana.workspace=/dev/vdb")
	}
	for _, extra := range strings.Fields(cfg.KernelArgs) {
		if !strings.HasPrefix(extra, "arcana.") {
			args = append(args, extra)
		}
	}
	return strings.Join(args, " ")
}
