// Package docker implements hardened Docker Engine sandbox instances.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/brendatamaar/arcana/internal/runtime"
)

const (
	maxManaged = 1000
	megabyte   = int64(1024 * 1024)
)

// Clock supplies bounded exec-status polling delays.
type Clock interface {
	After(time.Duration) <-chan time.Time
}
type systemClock struct{}

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config contains operator-controlled Docker and helper settings.
type Config struct {
	Host                 string
	OCIRuntime           string
	AllowInsecureRuntime bool
	InstallID            string
	HelperPath           string
	Clock                Clock
}

// Driver owns the Docker client and serializes create operations to avoid volume-name races.
type Driver struct {
	client      *client.Client
	config      Config
	createGate  chan struct{}
	sessionsMu  sync.Mutex
	sessions    map[uint64]activeSession
	nextSession uint64
	sessionsWG  sync.WaitGroup
	closed      bool
}

var _ runtime.Driver = (*Driver)(nil)

// New validates safe defaults and connects through the official client.
func New(config Config) (*Driver, error) {
	if config.OCIRuntime == "" {
		config.OCIRuntime = "runsc"
	}
	if config.OCIRuntime != "runsc" && config.OCIRuntime != "runc" {
		return nil, runtime.Invalid{Field: "oci_runtime"}
	}
	if config.OCIRuntime == "runc" && !config.AllowInsecureRuntime {
		return nil, runtime.Invalid{Field: "allow_insecure_runtime"}
	}
	if config.InstallID == "" || len(config.InstallID) > 128 {
		return nil, runtime.Invalid{Field: "install_id"}
	}
	if !filepath.IsAbs(config.HelperPath) {
		return nil, runtime.Invalid{Field: "helper_path"}
	}
	info, err := os.Stat(config.HelperPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, runtime.Invalid{Field: "helper_path regular file"}
	}
	if config.Host == "" {
		config.Host = "unix:///var/run/docker.sock"
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	engine, err := client.New(client.WithHost(config.Host))
	if err != nil {
		return nil, fmt.Errorf("creating Docker client: %w", err)
	}
	return &Driver{client: engine, config: config, sessions: make(map[uint64]activeSession), createGate: make(chan struct{}, 1)}, nil
}

// Close releases client resources after all sessions have ended.
func (d *Driver) Close() error {
	d.sessionsMu.Lock()
	d.closed = true
	callbacks := make([]context.CancelFunc, 0, len(d.sessions))
	for _, session := range d.sessions {
		callbacks = append(callbacks, session.cancel)
	}
	d.sessionsMu.Unlock()
	for _, cancel := range callbacks {
		cancel()
	}
	d.sessionsWG.Wait()
	return d.client.Close()
}

// Name includes the explicitly configured OCI runtime.
func (d *Driver) Name() string { return "docker/" + d.config.OCIRuntime }

// Capabilities reports Docker's supported exec features.
func (*Driver) Capabilities() runtime.Capabilities { return runtime.Capabilities{SupportsTTY: true} }

func validSandboxID(id string) bool {
	if len(id) != 24 || !strings.HasPrefix(id, "sbx_") {
		return false
	}
	for _, c := range id[4:] {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
func sandboxName(id string) string { return "sbx-" + id }
func sandboxFromName(name string) (string, bool) {
	id := strings.TrimPrefix(name, "sbx-")
	return id, strings.HasPrefix(name, "sbx-") && validSandboxID(id)
}

func validateSpec(spec runtime.Spec) error {
	if !validSandboxID(spec.SandboxID) {
		return runtime.Invalid{Field: "sandbox_id"}
	}
	if spec.Image == "" || len(spec.Image) > 1024 {
		return runtime.Invalid{Field: "image"}
	}
	if spec.WorkspaceBackend != "" && spec.WorkspaceBackend != "volume" && spec.WorkspaceBackend != "tmpfs" {
		return runtime.Invalid{Field: "workspace_backend"}
	}
	if spec.NetworkMode != "" && spec.NetworkMode != "none" {
		return runtime.Invalid{Field: "network_mode"}
	}
	r := spec.Resources
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 0.01 || r.CPUs > 1024 {
		return runtime.Invalid{Field: "cpus"}
	}
	if r.MemoryMB <= 0 || r.MemoryMB > 1<<30 {
		return runtime.Invalid{Field: "memory_mb"}
	}
	if r.DiskMB <= 0 || r.DiskMB > 1<<30 {
		return runtime.Invalid{Field: "disk_mb"}
	}
	if r.PIDs <= 0 || r.PIDs > 1<<22 {
		return runtime.Invalid{Field: "pids"}
	}
	if spec.Workdir != "" && (!path.IsAbs(spec.Workdir) || strings.ContainsRune(spec.Workdir, 0)) {
		return runtime.Invalid{Field: "workdir"}
	}
	if _, err := environment(spec.Env); err != nil {
		return err
	}
	if len(spec.Labels) > 100 {
		return runtime.Invalid{Field: "labels"}
	}
	for k, v := range spec.Labels {
		if strings.HasPrefix(k, "arcana.") || len(k) > 128 || len(v) > 1024 || strings.ContainsRune(k, 0) || strings.ContainsRune(v, 0) {
			return runtime.Invalid{Field: "labels"}
		}
	}
	return nil
}

func environment(values map[string]string) ([]string, error) {
	if len(values) > 256 {
		return nil, runtime.Invalid{Field: "env count"}
	}
	result := make([]string, 0, len(values))
	total := 0
	for k, v := range values {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, runtime.Invalid{Field: "env"}
		}
		total += len(k) + len(v) + 1
		if total > 131072 {
			return nil, runtime.Invalid{Field: "env bytes"}
		}
		result = append(result, k+"="+v)
	}
	sort.Strings(result)
	return result, nil
}

func (d *Driver) labels(spec runtime.Spec) map[string]string {
	labels := maps.Clone(spec.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["arcana.managed"] = "true"
	labels["arcana.instance"] = d.config.InstallID
	labels["arcana.sandbox_id"] = spec.SandboxID
	return labels
}

func (d *Driver) createOptions(spec runtime.Spec) client.ContainerCreateOptions {
	labels := d.labels(spec)
	pidLimit := spec.Resources.PIDs
	initProcess := true
	env, _ := environment(spec.Env)
	workdir := spec.Workdir
	if workdir == "" {
		workdir = "/workspace"
	}
	host := &container.HostConfig{
		Runtime: d.config.OCIRuntime, NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, ReadonlyRootfs: true, Init: &initProcess,
		LogConfig: container.LogConfig{Type: "none"},
		Resources: container.Resources{NanoCPUs: int64(spec.Resources.CPUs * 1e9), Memory: spec.Resources.MemoryMB * megabyte, MemorySwap: spec.Resources.MemoryMB * megabyte, PidsLimit: &pidLimit, Ulimits: []*container.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}},
		Tmpfs:     map[string]string{"/tmp": "rw,nosuid,nodev,size=64m,mode=1777", "/run/sbx": "rw,nosuid,nodev,size=4m,uid=1000,gid=1000,mode=0700"},
		Mounts:    []mount.Mount{{Type: mount.TypeBind, Source: d.config.HelperPath, Target: "/opt/arcana/sbx-exec", ReadOnly: true}},
	}
	if spec.WorkspaceBackend == "tmpfs" {
		host.Tmpfs["/workspace"] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%d,uid=1000,gid=1000,mode=0755", spec.Resources.DiskMB*megabyte)
	} else {
		host.Mounts = append(host.Mounts, mount.Mount{Type: mount.TypeVolume, Source: sandboxName(spec.SandboxID) + "-ws", Target: "/workspace"})
	}
	return client.ContainerCreateOptions{Name: sandboxName(spec.SandboxID), Config: &container.Config{Image: spec.Image, User: "1000:1000", WorkingDir: workdir, Env: env, Labels: labels, Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"infinity"}, NetworkDisabled: true, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: host}
}

// Create starts a safely configured instance, cleaning up partially-created resources.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (instance runtime.Instance, resultErr error) {
	if err := validateSpec(spec); err != nil {
		return runtime.Instance{}, err
	}
	select {
	case d.createGate <- struct{}{}:
	case <-ctx.Done():
		return runtime.Instance{}, ctx.Err()
	}
	defer func() { <-d.createGate }()
	if err := ctx.Err(); err != nil {
		return runtime.Instance{}, err
	}
	image, err := d.client.ImageInspect(ctx, spec.Image)
	if err != nil {
		return runtime.Instance{}, fmt.Errorf("inspecting template image: %w", err)
	}
	if image.Config != nil {
		for target := range image.Config.Volumes {
			if target != "/workspace" && target != "/tmp" && target != "/run/sbx" {
				return runtime.Instance{}, runtime.Invalid{Field: "image declares unsupported writable volume"}
			}
		}
	}
	name := sandboxName(spec.SandboxID)
	workspace := name + "-ws"
	volumeCreated := false
	containerCreated := false
	defer func() {
		if !containerCreated && volumeCreated {
			cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, cleanupErr := d.client.VolumeRemove(cleanup, workspace, client.VolumeRemoveOptions{}); cleanupErr != nil && !errdefs.IsNotFound(cleanupErr) {
				resultErr = errors.Join(resultErr, fmt.Errorf("cleaning failed workspace: %w", cleanupErr))
			}
		}
	}()
	if spec.WorkspaceBackend != "tmpfs" {
		_, err = d.client.VolumeInspect(ctx, workspace, client.VolumeInspectOptions{})
		if err == nil {
			return runtime.Instance{}, runtime.Invalid{Field: "workspace already exists"}
		}
		if !errdefs.IsNotFound(err) {
			return runtime.Instance{}, fmt.Errorf("checking workspace collision: %w", err)
		}
		if _, err = d.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: workspace, Labels: d.labels(spec)}); err != nil {
			return runtime.Instance{}, fmt.Errorf("creating workspace volume: %w", err)
		}
		volumeCreated = true
	}
	created, err := d.client.ContainerCreate(ctx, d.createOptions(spec))
	if err != nil {
		return runtime.Instance{}, fmt.Errorf("creating sandbox container: %w", err)
	}
	if _, err = d.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, removeErr := d.client.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
		if removeErr != nil && !errdefs.IsNotFound(removeErr) {
			return runtime.Instance{}, errors.Join(fmt.Errorf("starting sandbox: %w", err), fmt.Errorf("cleaning failed container: %w", removeErr))
		}
		return runtime.Instance{}, fmt.Errorf("starting sandbox: %w", err)
	}
	containerCreated = true
	return runtime.Instance{ID: name, SandboxID: spec.SandboxID, Labels: d.labels(spec)}, nil
}

func (d *Driver) inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	inspected, err := d.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return container.InspectResponse{}, runtime.NotFound{ID: id}
	}
	if err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspecting sandbox: %w", err)
	}
	if inspected.Container.Config == nil || inspected.Container.Config.Labels["arcana.managed"] != "true" || inspected.Container.Config.Labels["arcana.instance"] != d.config.InstallID {
		return inspected.Container, runtime.NotFound{ID: id}
	}
	return inspected.Container, nil
}

// Destroy removes the container before its named workspace and ignores missing resources.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	sandboxID, valid := sandboxFromName(id)
	if !valid {
		return runtime.NotFound{ID: id}
	}
	inspected, err := d.inspect(ctx, id)
	var missing runtime.NotFound
	if err != nil && inspected.Config != nil {
		return err
	}
	if err != nil && !errors.As(err, &missing) {
		return err
	}
	if err == nil {
		if inspected.Config.Labels["arcana.sandbox_id"] != sandboxID {
			return runtime.NotFound{ID: id}
		}
		if _, err = d.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("destroying sandbox container: %w", err)
		}
	}
	d.cancelSessions(id)
	workspace := id + "-ws"
	volume, err := d.client.VolumeInspect(ctx, workspace, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting workspace volume: %w", err)
	}
	if volume.Volume.Labels["arcana.managed"] != "true" || volume.Volume.Labels["arcana.instance"] != d.config.InstallID || volume.Volume.Labels["arcana.sandbox_id"] != sandboxID {
		return runtime.NotFound{ID: id}
	}
	if _, err = d.client.VolumeRemove(ctx, workspace, client.VolumeRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("removing workspace volume: %w", err)
	}
	return nil
}

// List returns a bounded set of this installation's managed containers.
func (d *Driver) List(ctx context.Context) ([]runtime.Instance, error) {
	result, err := d.client.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: maxManaged + 1, Filters: make(client.Filters).Add("label", "arcana.managed=true", "arcana.instance="+d.config.InstallID)})
	if err != nil {
		return nil, fmt.Errorf("listing managed containers: %w", err)
	}
	if len(result.Items) > maxManaged {
		return nil, runtime.Invalid{Field: "managed instance limit"}
	}
	instances := make([]runtime.Instance, 0, len(result.Items))
	for _, item := range result.Items {
		sandboxID := item.Labels["arcana.sandbox_id"]
		if !validSandboxID(sandboxID) || item.Labels["arcana.managed"] != "true" || item.Labels["arcana.instance"] != d.config.InstallID {
			continue
		}
		instances = append(instances, runtime.Instance{ID: sandboxName(sandboxID), SandboxID: sandboxID, Labels: maps.Clone(item.Labels), Stopped: string(item.State) != "running"})
	}
	return instances, nil
}

// Stats returns one bounded JSON sample without starting a statistics stream.
func (d *Driver) Stats(ctx context.Context, id string) (runtime.Stats, error) {
	if _, err := d.inspect(ctx, id); err != nil {
		return runtime.Stats{}, err
	}
	sample, err := d.client.ContainerStats(ctx, id, client.ContainerStatsOptions{})
	if err != nil {
		return runtime.Stats{}, fmt.Errorf("reading sandbox stats: %w", err)
	}
	defer func() { _ = sample.Body.Close() }()
	var data container.StatsResponse
	if err = json.NewDecoder(io.LimitReader(sample.Body, 1048576)).Decode(&data); err != nil {
		return runtime.Stats{}, fmt.Errorf("decoding sandbox stats: %w", err)
	}
	return runtime.Stats{CPUUsageNano: data.CPUStats.CPUUsage.TotalUsage, MemoryUsageBytes: data.MemoryStats.Usage, PIDs: data.PidsStats.Current}, nil
}
