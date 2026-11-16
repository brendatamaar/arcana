package containerd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// engine is the part of containerd this driver uses. The production implementation talks
// to a real containerd; tests supply a fake, because no containerd was reachable when this
// was written.
type engine interface {
	// Create makes the container from the image and starts its task. It leaves nothing
	// behind on failure.
	Create(ctx context.Context, p plan) error
	// Delete kills the task and removes the container and its snapshot. Missing is success.
	Delete(ctx context.Context, id string) error
	Exec(ctx context.Context, id string, req runtime.ExecRequest) (runtime.ExecSession, error)
	// List returns containers carrying the managed label, with their labels.
	List(ctx context.Context) ([]containerInfo, error)
	Stats(ctx context.Context, id string) (runtime.Stats, error)
	// Version returns containerd's version, proving the endpoint is reachable.
	Version(ctx context.Context) (string, error)
	// HasImage reports whether the image is in the driver's namespace.
	HasImage(ctx context.Context, image string) bool
	Close() error
}

type containerInfo struct {
	ID      string
	Labels  map[string]string
	Stopped bool
}

// Driver runs each sandbox as a containerd container under the configured shim.
type Driver struct {
	cfg    Config
	engine engine
	gate   chan struct{} // serialises creation, like the Docker driver
	mu     sync.Mutex
	closed bool
	// host and networks exist only with allowlist networking (network.go).
	host      hostNet
	networkMu sync.Mutex
	networks  map[string]*proxyState
}

var _ runtime.Driver = (*Driver)(nil)

// New validates the configuration and connects to containerd.
func New(cfg Config) (*Driver, error) {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	e, err := newContainerdEngine(cfg)
	if err != nil {
		return nil, err
	}
	d := &Driver{cfg: cfg, engine: e, gate: make(chan struct{}, 1)}
	if cfg.Network != nil {
		d.host = linuxHost{cfg: *cfg.Network}
	}
	return d, nil
}

// Name includes the runtime, like the Docker driver.
func (d *Driver) Name() string { return "containerd/" + d.cfg.OCIRuntime }

// Capabilities reports terminal support.
func (*Driver) Capabilities() runtime.Capabilities {
	return runtime.Capabilities{SupportsTTY: true}
}

// Close releases the containerd connection. Containers keep running so a restart can reconcile.
func (d *Driver) Close() error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	return d.engine.Close()
}

// Create starts a sandbox container.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Instance, error) {
	p, err := planContainer(d.cfg, spec)
	if err != nil {
		return runtime.Instance{}, err
	}
	select {
	case d.gate <- struct{}{}:
	case <-ctx.Done():
		return runtime.Instance{}, ctx.Err()
	}
	defer func() { <-d.gate }()
	if err = ctx.Err(); err != nil {
		return runtime.Instance{}, err
	}
	// Resources are built cheapest-failure first and unwound in reverse if anything fails.
	var cleanups []func(context.Context) error
	unwind := func(cause error) (runtime.Instance, error) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		errs := []error{cause}
		for i := len(cleanups) - 1; i >= 0; i-- {
			// Stop at the first failure: nothing may be removed from under a container that might still run.
			// PruneOrphans finishes the job at the next start.
			if err := cleanups[i](cleanup); err != nil {
				errs = append(errs, err)
				break
			}
		}
		return runtime.Instance{}, errors.Join(errs...)
	}
	if p.Workspace != "" {
		if err = prepareWorkspace(d.cfg.WorkspaceDir, p.Workspace); err != nil {
			return runtime.Instance{}, err
		}
		cleanups = append(cleanups, func(context.Context) error { return removeWorkspace(p.Workspace) })
	}
	if p.Allowlist {
		var netns string
		if netns, err = d.createNetwork(ctx, spec.SandboxID, p.Allow); err != nil {
			return unwind(err)
		}
		cleanups = append(cleanups, func(c context.Context) error { return d.removeNetwork(c, spec.SandboxID) })
		p.SpecOpts = append(p.SpecOpts, withNetns(netns))
	}
	if err = d.engine.Create(ctx, p); err != nil {
		// A half-created container must not survive: remove it with a context that outlives the failed request,
		// and only then what it used.
		cleanups = append(cleanups, func(c context.Context) error { return d.engine.Delete(c, p.ID) })
		return unwind(fmt.Errorf("creating sandbox container: %w", err))
	}
	return runtime.Instance{ID: p.ID, SandboxID: spec.SandboxID, Labels: maps.Clone(p.Labels)}, nil
}

func validID(id string) bool {
	_, ok := sandboxFromContainer(id)
	return ok
}

// Destroy is idempotent: a missing container is success. The workspace goes only after the container is
// gone, so a running sandbox never loses its directory and nothing writes while it is removed.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	sandbox, ok := sandboxFromContainer(id)
	if !ok {
		return runtime.NotFound{ID: id}
	}
	if err := d.engine.Delete(ctx, id); err != nil {
		return err
	}
	var errs []error
	if d.hasNetwork(sandbox) {
		errs = append(errs, d.removeNetwork(ctx, sandbox))
	}
	if d.cfg.WorkspaceDir != "" {
		errs = append(errs, removeWorkspace(d.cfg.workspacePath(sandbox)))
	}
	return errors.Join(errs...)
}

// PruneOrphans removes workspace directories no kept sandbox owns (a crash between creating the directory and
// the container, or a destroy that died halfway). It runs before request admission, like the Docker driver's.
func (d *Driver) PruneOrphans(ctx context.Context, keep map[string]bool) error {
	if err := d.pruneNetworks(ctx, keep); err != nil {
		return err
	}
	if d.cfg.WorkspaceDir == "" {
		return nil
	}
	ids, err := listWorkspaces(d.cfg.WorkspaceDir)
	if err != nil {
		return fmt.Errorf("listing workspaces: %w", err)
	}
	for _, id := range ids {
		if keep[id] {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = removeWorkspace(d.cfg.workspacePath(id)); err != nil {
			return err
		}
	}
	return nil
}

// Exec runs argv inside the sandbox.
func (d *Driver) Exec(ctx context.Context, id string, req runtime.ExecRequest) (runtime.ExecSession, error) {
	if !validID(id) {
		return nil, runtime.NotFound{ID: id}
	}
	if len(req.Cmd) == 0 {
		return nil, runtime.Invalid{Field: "cmd"}
	}
	return d.engine.Exec(ctx, id, req)
}

// Stats returns the container's cgroup accounting.
func (d *Driver) Stats(ctx context.Context, id string) (runtime.Stats, error) {
	if !validID(id) {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	return d.engine.Stats(ctx, id)
}

// List returns this installation's containers only.
func (d *Driver) List(ctx context.Context) ([]runtime.Instance, error) {
	all, err := d.engine.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []runtime.Instance
	for _, c := range all {
		sandbox, ok := sandboxFromContainer(c.ID)
		if !ok || c.Labels["arcana.managed"] != "true" || c.Labels["arcana.instance"] != d.cfg.InstallID || c.Labels["arcana.sandbox_id"] != sandbox {
			continue
		}
		out = append(out, runtime.Instance{ID: c.ID, SandboxID: sandbox, Labels: maps.Clone(c.Labels), Stopped: c.Stopped})
	}
	return out, nil
}

var _ runtime.Checker = (*Driver)(nil)

// Check connects to containerd and reports its version.
func (d *Driver) Check(ctx context.Context) (string, error) {
	version, err := d.engine.Version(ctx)
	if err != nil {
		return "", fmt.Errorf("containerd is unreachable at %s: %w", d.cfg.Address, err)
	}
	detail := fmt.Sprintf("containerd %s; namespace %s; %s", version, d.cfg.Namespace, d.cfg.shimName())
	if d.cfg.Network != nil {
		network, networkErr := d.checkNetwork(ctx)
		if networkErr != nil {
			return "", networkErr
		}
		detail += "; " + network
	}
	if d.cfg.WorkspaceDir != "" {
		if err = checkWorkspaceDir(d.cfg.WorkspaceDir); err != nil {
			return "", fmt.Errorf("workspace directory %s: %w", d.cfg.WorkspaceDir, err)
		}
		detail += "; volume workspaces in " + d.cfg.WorkspaceDir
	}
	return detail, nil
}

// HasImage reports whether the template image is in the configured containerd namespace.
func (d *Driver) HasImage(ctx context.Context, image string) bool {
	return d.engine.HasImage(ctx, image)
}
