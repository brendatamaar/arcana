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
	return &Driver{cfg: cfg, engine: e, gate: make(chan struct{}, 1)}, nil
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
	if err = d.engine.Create(ctx, p); err != nil {
		// A half-created container must not survive: remove it with a context that outlives
		// the failed request.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return runtime.Instance{}, errors.Join(fmt.Errorf("creating sandbox container: %w", err), d.engine.Delete(cleanup, p.ID))
	}
	return runtime.Instance{ID: p.ID, SandboxID: spec.SandboxID, Labels: maps.Clone(p.Labels)}, nil
}

func validID(id string) bool {
	_, ok := sandboxFromContainer(id)
	return ok
}

// Destroy is idempotent: a missing container is success.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	if !validID(id) {
		return runtime.NotFound{ID: id}
	}
	return d.engine.Delete(ctx, id)
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
