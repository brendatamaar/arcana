// Package runtime defines the contract shared by sandbox runtime drivers.
package runtime

import (
	"context"
	"fmt"
	"io"
)

// Driver owns isolated instances. Destroy is idempotent; List returns managed instances.
type Driver interface {
	Name() string
	Capabilities() Capabilities
	Create(context.Context, Spec) (Instance, error)
	Destroy(context.Context, string) error
	Exec(context.Context, string, ExecRequest) (ExecSession, error)
	Stats(context.Context, string) (Stats, error)
	List(context.Context) ([]Instance, error)
}

// Capabilities describes optional runtime features.
type Capabilities struct {
	SupportsTTY            bool
	SupportsMemorySnapshot bool
}

// Resources contains per-instance resource bounds in API units.
type Resources struct {
	CPUs     float64
	MemoryMB int64
	DiskMB   int64
	PIDs     int64
}

// Spec describes an instance; runtime hardening is always applied by the driver.
type Spec struct {
	SandboxID        string
	Image            string
	Resources        Resources
	Env              map[string]string
	Labels           map[string]string
	WorkspaceBackend string
	NetworkMode      string
	Workdir          string
}

// Instance is a runtime object's identity and managed labels.
type Instance struct {
	ID        string
	SandboxID string
	Labels    map[string]string
}

// ExecRequest contains an explicit argv invocation; shells are resolved by the orchestrator.
type ExecRequest struct {
	Cmd  []string
	Cwd  string
	Env  map[string]string
	TTY  bool
	Cols uint16
	Rows uint16
}

// ExecSession owns streams until Wait finishes; callers drain stdout/stderr concurrently.
// Stderr is nil in TTY mode. Context cancellation must unblock stream reads and Wait.
type ExecSession interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Resize(cols, rows uint16) error
	Wait(context.Context) (ExitInfo, error)
}

// ExitInfo is the runtime exit result; helper metadata supplies timeout/signal details.
type ExitInfo struct {
	ExitCode int
	TimedOut bool
	Signal   string
}

// Stats is a bounded snapshot of runtime resource accounting.
type Stats struct {
	CPUUsageNano     uint64
	MemoryUsageBytes uint64
	PIDs             uint64
}

// NotFound means an instance is no longer present.
type NotFound struct{ ID string }

func (e NotFound) Error() string { return fmt.Sprintf("runtime instance %s not found", e.ID) }

// Invalid describes a rejected runtime request.
type Invalid struct{ Field string }

func (e Invalid) Error() string { return "invalid runtime field: " + e.Field }

// Unsupported means a requested runtime capability is unavailable.
type Unsupported struct{ Feature string }

func (e Unsupported) Error() string { return "unsupported runtime feature: " + e.Feature }
