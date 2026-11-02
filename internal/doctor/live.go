package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
)

// Live performs diagnostics against the operator-configured local host and daemon.
type Live struct{ Config config.Config }

func boundedFile(name string, limit int64) (string, error) {
	var file *os.File
	var err error
	switch name {
	case "/proc/sys/kernel/osrelease":
		file, err = os.Open("/proc/sys/kernel/osrelease")
	case "/proc/self/mountinfo":
		file, err = os.Open("/proc/self/mountinfo")
	default:
		return "", errors.New("unknown host diagnostic path")
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return "", errors.New("host diagnostic file exceeds limit")
	}
	return strings.TrimSpace(string(data)), err
}

func versionAtLeast(value string, major, minor int) bool {
	parts := strings.SplitN(value, ".", 3)
	if len(parts) < 2 {
		return false
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	b, err := strconv.Atoi(parts[1])
	return err == nil && (a > major || a == major && b >= minor)
}

// Check performs one bounded probe. Errors contain no credentials or sandbox output.
func (l Live) Check(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch name {
	case "os":
		if goruntime.GOOS != "linux" {
			return "", errors.New("native runtime requires Linux")
		}
		return "Linux", nil
	case "kernel":
		value, err := boundedFile("/proc/sys/kernel/osrelease", 256)
		if err != nil {
			return "", err
		}
		if !versionAtLeast(value, 5, 3) {
			return "", errors.New("kernel is older than 5.3 or its version is unrecognized")
		}
		return value, nil
	case "cgroups":
		value, err := boundedFile("/proc/self/mountinfo", 1<<20)
		if err != nil {
			return "", err
		}
		if !strings.Contains(value, " - cgroup2 ") {
			return "", errors.New("cgroups v2 is not mounted")
		}
		return "cgroups v2 mounted", nil
	case "kvm":
		if _, err := os.Stat("/dev/kvm"); err != nil {
			return "", errors.New("/dev/kvm is absent or inaccessible")
		}
		return "/dev/kvm present", nil
	case "data_dir":
		if err := os.MkdirAll(l.Config.DataDir, 0o700); err != nil {
			return "", err
		}
		file, err := os.CreateTemp(l.Config.DataDir, ".doctor-*")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(file.Name()) }()
		_, writeErr := file.Write([]byte("arcana doctor\n"))
		syncErr := file.Sync()
		closeErr := file.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return "", err
		}
		return "data directory writable", nil
	case "database":
		database, err := store.Open(ctx, filepath.Join(l.Config.DataDir, "arcana.db"))
		if err != nil {
			return "", err
		}
		if err = database.Close(); err != nil {
			return "", err
		}
		return "SQLite opens; embedded migrations current", nil
	case "runsc", "runc":
		return l.runtimeProbe(ctx, name)
	}
	engine, err := client.New(client.WithHost(l.Config.Runtime.Docker.Host))
	if err != nil {
		return "", err
	}
	defer func() { _ = engine.Close() }()
	if name == "docker" {
		result, err := engine.Info(ctx, client.InfoOptions{})
		if err != nil {
			return "", fmt.Errorf("docker is unreachable: %w", err)
		}
		if !versionAtLeast(result.Info.ServerVersion, 24, 0) {
			return "", errors.New("docker Engine >= 24 is required")
		}
		if result.Info.CgroupVersion != "2" {
			return "", errors.New("docker must use cgroups v2")
		}
		return fmt.Sprintf("Docker %s; storage %s; cgroups %s", result.Info.ServerVersion, result.Info.Driver, result.Info.CgroupVersion), nil
	}
	if strings.HasPrefix(name, "image:") {
		image := strings.TrimPrefix(name, "image:")
		if _, err = engine.ImageInspect(ctx, image); err != nil {
			return "", errors.New("configured image is not locally available; build or pull it")
		}
		return "image present locally", nil
	}
	return "", errors.New("unknown host probe")
}

func (l Live) runtimeProbe(ctx context.Context, oci string) (detail string, failure error) {
	if goruntime.GOOS != "linux" {
		return "", errors.New("runtime test requires Linux")
	}
	if oci == "runc" && !l.Config.Runtime.Docker.AllowInsecureRuntime {
		return "", errors.New("insecure runtime opt-in is required")
	}
	database, err := store.Open(ctx, filepath.Join(l.Config.DataDir, "arcana.db"))
	if err != nil {
		return "", err
	}
	install := database.InstallID
	if err = database.Close(); err != nil {
		return "", err
	}
	helperPath, err := helper.Install(ctx, l.Config.DataDir)
	if err != nil {
		return "", err
	}
	driver, err := docker.New(docker.Config{Host: l.Config.Runtime.Docker.Host, OCIRuntime: oci, AllowInsecureRuntime: l.Config.Runtime.Docker.AllowInsecureRuntime, InstallID: install, HelperPath: helperPath})
	if err != nil {
		return "", err
	}
	defer func() { failure = errors.Join(failure, driver.Close()) }()
	engine, err := client.New(client.WithHost(l.Config.Runtime.Docker.Host))
	if err != nil {
		return "", err
	}
	defer func() { _ = engine.Close() }()
	info, err := engine.Info(ctx, client.InfoOptions{})
	if err != nil {
		return "", err
	}
	if _, ok := info.Info.Runtimes[oci]; !ok {
		return "", fmt.Errorf("%s runtime is not registered", oci)
	}
	image := ""
	for i, template := range l.Config.Templates {
		if i >= 1000 {
			break
		}
		if _, err = engine.ImageInspect(ctx, template.Image); err == nil {
			image = template.Image
			break
		}
	}
	if image == "" {
		return "", errors.New("runtime test needs at least one locally built or pulled configured template")
	}
	id, err := ids.New("sbx")
	if err != nil {
		return "", err
	}
	// Destroy by canonical name even if Create fails after allocating resources.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		failure = errors.Join(failure, driver.Destroy(cleanup, "sbx-"+id))
	}()
	instance, err := driver.Create(ctx, runtime.Spec{SandboxID: id, Image: image, Resources: runtime.Resources{CPUs: .25, MemoryMB: 128, DiskMB: 16, PIDs: 64}, WorkspaceBackend: "tmpfs", NetworkMode: "none", Workdir: "/workspace"})
	if err != nil {
		return "", fmt.Errorf("hardened runtime container failed: %w", err)
	}
	execID, err := ids.New("exe")
	if err != nil {
		return "", err
	}
	output, exit, err := probeExec(ctx, driver, instance.ID, []string{"/opt/arcana/sbx-exec", "run", "--id", execID, "--timeout", "2", "--", "/bin/sleep", "0"})
	if err != nil || exit.ExitCode != 0 || len(output) != 0 {
		return "", errors.New("runtime helper failed; child subreapers and pidfd signalling must be supported")
	}
	output, exit, err = probeExec(ctx, driver, instance.ID, []string{"/opt/arcana/sbx-exec", "status", "--id", execID, "--consume"})
	var status protocol.Status
	if err != nil || exit.ExitCode != 0 || json.Unmarshal(output, &status) != nil || status.ID != execID || !status.CleanupComplete || status.ExitCode != 0 {
		return "", errors.New("runtime did not prove helper process cleanup")
	}
	return oci + " registered; hardened container and helper cleanup passed", nil
}

func probeExec(ctx context.Context, driver runtime.Driver, instance string, argv []string) ([]byte, runtime.ExitInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	session, err := driver.Exec(ctx, instance, runtime.ExecRequest{Cmd: argv, Cwd: "/workspace"})
	if err != nil {
		return nil, runtime.ExitInfo{}, err
	}
	if err = session.Stdin().Close(); err != nil {
		return nil, runtime.ExitInfo{}, err
	}
	var output bytes.Buffer
	_, readErr := io.Copy(&output, io.LimitReader(session.Stdout(), 4097))
	stderrBytes, stderrErr := io.Copy(io.Discard, io.LimitReader(session.Stderr(), 4097))
	if output.Len() > 4096 || stderrBytes > 0 {
		cancel()
	}
	exit, waitErr := session.Wait(ctx)
	if output.Len() > 4096 {
		return nil, exit, errors.New("runtime probe output limit exceeded")
	}
	if stderrBytes > 0 {
		return nil, exit, errors.New("runtime probe produced unexpected diagnostics")
	}
	return output.Bytes(), exit, errors.Join(readErr, stderrErr, waitErr)
}
