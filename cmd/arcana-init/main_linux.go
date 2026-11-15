package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	envdPath    = "/opt/arcana/envd"
	envFilePath = "/etc/arcana/env"
	sandboxID   = 1000
)

func main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "arcana-init must run as PID 1")
		os.Exit(2)
	}
	err := run()
	fmt.Fprintln(os.Stderr, "arcana-init:", err)
	// Without a working envd the VM is useless; power it off so the driver notices.
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

func run() error {
	if err := mountAll([]mountSpec{{"proc", "/proc", "proc", msNoSuid | msNoDev | msNoExec, ""}}); err != nil {
		return err
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return err
	}
	s, err := parseCmdline(string(cmdline))
	if err != nil {
		return err
	}
	if err = mountAll(mountPlan(s)[1:]); err != nil {
		return err
	}
	if err = os.Chown("/workspace", sandboxID, sandboxID); err != nil {
		return fmt.Errorf("handing /workspace to the sandbox user: %w", err)
	}
	if err = os.MkdirAll("/run/sbx", 0o1777); err != nil { // #nosec G301 -- sticky world-writable controller state directory, like /tmp
		return err
	}
	if err = os.Chmod("/run/sbx", 0o1777); err != nil { // #nosec G302 -- the controller state directory must be writable by the sandbox user, like /tmp
		return err
	}
	if err = loopbackUp(); err != nil {
		return err
	}
	// Limits are inherited by every command: RLIMIT_NPROC is per UID, which is the one
	// sandbox user, so it bounds the sandbox's processes as the container PID limit does.
	for resource, value := range map[int]uint64{unix.RLIMIT_NPROC: s.PIDs, unix.RLIMIT_NOFILE: s.NoFile} {
		if err = unix.Setrlimit(resource, &unix.Rlimit{Cur: value, Max: value}); err != nil {
			return fmt.Errorf("setting resource limit %d: %w", resource, err)
		}
	}
	env, err := imageEnv()
	if err != nil {
		return err
	}
	return supervise(s, env)
}

func mountAll(plan []mountSpec) error {
	for _, m := range plan {
		if m.Target != "/proc" {
			if err := os.MkdirAll(m.Target, 0o755); err != nil { // #nosec G301 -- mount points in the guest's own tmpfs/rootfs
				return err
			}
		}
		if err := unix.Mount(m.Source, m.Target, m.Type, m.Flags, m.Data); err != nil {
			return fmt.Errorf("mounting %s: %w", m.Target, err)
		}
	}
	return nil
}

// loopbackUp brings lo up so localhost works inside the sandbox.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	request, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	request.SetUint16(unix.IFF_UP | unix.IFF_RUNNING)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, request)
}

// imageEnv returns the template image's ENV (PATH, package-manager locations) captured
// at rootfs build time, since a filesystem export does not carry it.
func imageEnv() ([]string, error) {
	data, err := os.ReadFile(envFilePath)
	if errors.Is(err, os.ErrNotExist) {
		return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 64*1024 {
		return nil, errors.New("environment file is too large")
	}
	return parseEnvFile(string(data))
}

// supervise runs envd and, as PID 1, reaps orphaned processes until envd exits.
func supervise(s settings, env []string) error {
	// #nosec G204 -- fixed binary, validated numeric settings
	cmd := exec.Command(envdPath, "serve", "--listen", "vsock:"+strconv.FormatUint(uint64(s.VsockPort), 10),
		"--user", fmt.Sprintf("%d:%d", sandboxID, sandboxID), "--root", "/workspace", "--root", "/tmp")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting envd: %w", err)
	}
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("waiting for envd: %w", err)
		}
		if pid == cmd.Process.Pid {
			return fmt.Errorf("envd exited (%v)", status)
		}
	}
}
