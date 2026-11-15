// Command arcana-init is PID 1 inside an Arcana Firecracker microVM. It mounts the
// pseudo and scratch filesystems, applies the sandbox limits from the kernel command line,
// starts envd as the sandbox user and powers the VM off when envd ends.
package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var devicePattern = regexp.MustCompile(`^/dev/vd[a-z]$`)

// settings are the arcana.* values the driver puts on the kernel command line.
type settings struct {
	VsockPort uint32
	PIDs      uint64
	NoFile    uint64
	Workspace string // block device path, or "tmpfs:<n>m"
}

func parseCmdline(cmdline string) (settings, error) {
	values := make(map[string]string)
	for _, field := range strings.Fields(cmdline) {
		if name, value, ok := strings.Cut(field, "="); ok && strings.HasPrefix(name, "arcana.") {
			if _, dup := values[name]; dup {
				return settings{}, fmt.Errorf("duplicate %s", name)
			}
			values[name] = value
		}
	}
	var s settings
	port, err := strconv.ParseUint(values["arcana.vsock_port"], 10, 32)
	if err != nil || port == 0 {
		return s, errors.New("invalid arcana.vsock_port")
	}
	s.VsockPort = uint32(port)
	if s.PIDs, err = strconv.ParseUint(values["arcana.pids"], 10, 32); err != nil || s.PIDs == 0 {
		return s, errors.New("invalid arcana.pids")
	}
	if s.NoFile, err = strconv.ParseUint(values["arcana.nofile"], 10, 32); err != nil || s.NoFile == 0 {
		return s, errors.New("invalid arcana.nofile")
	}
	s.Workspace = values["arcana.workspace"]
	switch {
	case devicePattern.MatchString(s.Workspace):
	case strings.HasPrefix(s.Workspace, "tmpfs:"):
		if _, convErr := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(s.Workspace, "tmpfs:"), "m"), 10, 32); convErr != nil {
			return s, errors.New("invalid tmpfs size")
		}
	default:
		return s, errors.New("invalid arcana.workspace")
	}
	return s, nil
}

// parseEnvFile reads KEY=VALUE lines written by scripts/build-rootfs.sh from the image's
// configured environment. Malformed lines are rejected rather than skipped.
func parseEnvFile(text string) ([]string, error) {
	var env []string
	for i, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \x00") || strings.ContainsRune(line, 0) {
			return nil, fmt.Errorf("invalid environment line %d", i+1)
		}
		env = append(env, line)
	}
	if len(env) > 256 {
		return nil, errors.New("too many environment entries")
	}
	return env, nil
}

type mountSpec struct {
	Source, Target, Type string
	Flags                uintptr
	Data                 string
}

const (
	msNoSuid = 0x2
	msNoDev  = 0x4
	msNoExec = 0x8
)

// mountPlan lists the mounts in dependency order. The root filesystem stays read-only;
// /tmp, /run and /workspace are the only writable places, as in the container sandboxes.
func mountPlan(s settings) []mountSpec {
	plan := []mountSpec{
		{"proc", "/proc", "proc", msNoSuid | msNoDev | msNoExec, "hidepid=0"},
		{"sysfs", "/sys", "sysfs", msNoSuid | msNoDev | msNoExec, ""},
		{"devtmpfs", "/dev", "devtmpfs", msNoSuid | msNoExec, "mode=0755"},
		{"devpts", "/dev/pts", "devpts", msNoSuid | msNoExec, "mode=0620,ptmxmode=0666"},
		{"tmpfs", "/tmp", "tmpfs", msNoSuid | msNoDev, "mode=1777,size=64m"},
		{"tmpfs", "/run", "tmpfs", msNoSuid | msNoDev | msNoExec, "mode=0755,size=16m"},
	}
	if size, ok := strings.CutPrefix(s.Workspace, "tmpfs:"); ok {
		plan = append(plan, mountSpec{"tmpfs", "/workspace", "tmpfs", msNoSuid | msNoDev, "mode=0755,uid=1000,gid=1000,size=" + size})
	} else {
		plan = append(plan, mountSpec{s.Workspace, "/workspace", "ext4", msNoSuid | msNoDev, ""})
	}
	return plan
}
