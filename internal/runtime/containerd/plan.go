package containerd

import (
	"context"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/brendatamaar/arcana/internal/runtime"
)

const (
	helperTarget = "/opt/arcana/sbx-exec"
	sandboxUID   = 1000
	sandboxGID   = 1000
	megabyte     = int64(1024 * 1024)
	cpuPeriod    = uint64(100000)
	// maxFiles mirrors the Docker driver's nofile ulimit.
	maxFiles = 1024
)

var sandboxIDPattern = regexp.MustCompile(`^sbx_[a-z2-7]{20}$`)

// containerID names a sandbox's container, mirroring the Docker driver's `sbx-<id>`.
func containerID(sandboxID string) string { return "sbx-" + sandboxID }

func sandboxFromContainer(id string) (string, bool) {
	sandbox, ok := strings.CutPrefix(id, "sbx-")
	return sandbox, ok && sandboxIDPattern.MatchString(sandbox)
}

// plan is everything decided about one container before containerd is contacted.
type plan struct {
	ID        string
	Image     string
	Labels    map[string]string
	Env       []string
	Workdir   string
	SpecOpts  []oci.SpecOpts // hardening and limits, applied after the image config
	Resources runtime.Resources
}

func environment(values map[string]string) ([]string, error) {
	if len(values) > 256 {
		return nil, runtime.Invalid{Field: "env count"}
	}
	out := make([]string, 0, len(values))
	total := 0
	for k, v := range values {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, runtime.Invalid{Field: "env"}
		}
		total += len(k) + len(v) + 1
		if total > 131072 {
			return nil, runtime.Invalid{Field: "env bytes"}
		}
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out, nil
}

// planContainer validates a spec and derives the container. Only what this driver can
// provide safely is accepted: network mode none and a tmpfs workspace.
func planContainer(cfg Config, spec runtime.Spec) (plan, error) {
	if !sandboxIDPattern.MatchString(spec.SandboxID) {
		return plan{}, runtime.Invalid{Field: "sandbox_id"}
	}
	if spec.Image == "" || len(spec.Image) > 1024 {
		return plan{}, runtime.Invalid{Field: "image"}
	}
	if spec.NetworkMode != "" && spec.NetworkMode != "none" {
		return plan{}, runtime.Unsupported{Feature: "containerd network mode " + spec.NetworkMode}
	}
	if spec.WorkspaceBackend != "tmpfs" {
		return plan{}, runtime.Unsupported{Feature: "containerd workspace backend " + fmt.Sprintf("%q (only tmpfs)", spec.WorkspaceBackend)}
	}
	r := spec.Resources
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 0.01 || r.CPUs > 1024 {
		return plan{}, runtime.Invalid{Field: "cpus"}
	}
	if r.MemoryMB <= 0 || r.MemoryMB > 1<<30 {
		return plan{}, runtime.Invalid{Field: "memory_mb"}
	}
	if r.DiskMB <= 0 || r.DiskMB > 1<<30 {
		return plan{}, runtime.Invalid{Field: "disk_mb"}
	}
	if r.PIDs <= 0 || r.PIDs > 1<<22 {
		return plan{}, runtime.Invalid{Field: "pids"}
	}
	workdir := spec.Workdir
	if workdir == "" {
		workdir = "/workspace"
	}
	if !path.IsAbs(workdir) || strings.ContainsRune(workdir, 0) {
		return plan{}, runtime.Invalid{Field: "workdir"}
	}
	env, err := environment(spec.Env)
	if err != nil {
		return plan{}, err
	}
	if len(spec.Labels) > 100 {
		return plan{}, runtime.Invalid{Field: "labels"}
	}
	labels := make(map[string]string, len(spec.Labels)+4)
	for k, v := range spec.Labels {
		if strings.HasPrefix(k, "arcana.") || len(k) > 128 || len(v) > 1024 || strings.ContainsRune(k, 0) || strings.ContainsRune(v, 0) {
			return plan{}, runtime.Invalid{Field: "labels"}
		}
		labels[k] = v
	}
	labels["arcana.managed"] = "true"
	labels["arcana.instance"] = cfg.InstallID
	labels["arcana.sandbox_id"] = spec.SandboxID
	if spec.PoolTemplate != "" {
		labels["arcana.pool"] = spec.PoolTemplate
	}
	return plan{
		ID: containerID(spec.SandboxID), Image: spec.Image, Labels: labels, Env: env, Workdir: workdir, Resources: r,
		SpecOpts: hardening(cfg, containerID(spec.SandboxID), workdir, r),
	}, nil
}

// hardening is FR-RL-2 and FR-RL-1 as OCI spec edits: the same guarantees the Docker
// driver sets through container flags.
func hardening(cfg Config, id, workdir string, r runtime.Resources) []oci.SpecOpts {
	memory := r.MemoryMB * megabyte
	opts := []oci.SpecOpts{
		oci.WithUIDGID(sandboxUID, sandboxGID),
		oci.WithNoNewPrivileges,
		oci.WithCapabilities([]string{}),
		oci.WithRootFSReadonly(),
		oci.WithProcessCwd(workdir),
		oci.WithMemoryLimit(uint64(memory)), // #nosec G115 -- validated positive
		oci.WithMemorySwap(memory),          // swap equals memory: no swap
		oci.WithCPUCFS(int64(math.Round(r.CPUs*float64(cpuPeriod))), cpuPeriod),
		oci.WithPidsLimit(r.PIDs),
		oci.WithRlimit(&specs.POSIXRlimit{Type: "RLIMIT_NOFILE", Soft: maxFiles, Hard: maxFiles}),
		oci.WithMounts(mounts(cfg, r)),
		// A flat cgroup per sandbox. The default, /<namespace>/<id>, makes gVisor try to remove the shared
		// <namespace> cgroup when one sandbox is destroyed, which fails while others run.
		oci.WithCgroup("/arcana-" + id),
	}
	return opts
}

// mounts are the only writable places (tmpfs /tmp, /run/sbx and the workspace) plus the
// read-only helper. No other host path is mounted.
func mounts(cfg Config, r runtime.Resources) []specs.Mount {
	uid, gid := fmt.Sprintf("uid=%d", sandboxUID), fmt.Sprintf("gid=%d", sandboxGID)
	workspaceSize := fmt.Sprintf("size=%dm", r.DiskMB)
	return []specs.Mount{
		{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", uid, gid, "mode=1777", "size=64m"}},
		{Destination: "/run/sbx", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "noexec", uid, gid, "mode=0700", "size=4m"}},
		// Builds run from the workspace, so it allows exec like the Docker tmpfs workspace.
		{Destination: "/workspace", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", uid, gid, "mode=0755", workspaceSize}},
		{Destination: helperTarget, Type: "bind", Source: cfg.HelperPath, Options: []string{"rbind", "ro"}},
	}
}

// applySpec applies the options to a default Linux spec; it exists so tests can inspect the
// resulting OCI spec without containerd.
func applySpec(ctx context.Context, opts []oci.SpecOpts) (*specs.Spec, error) {
	spec := &specs.Spec{}
	container := &containers.Container{}
	all := append([]oci.SpecOpts{oci.WithDefaultSpec(), oci.WithDefaultUnixDevices}, opts...)
	if err := oci.ApplyOpts(namespaces.WithNamespace(ctx, "arcana"), nil, container, spec, all...); err != nil {
		return nil, err
	}
	return spec, nil
}
