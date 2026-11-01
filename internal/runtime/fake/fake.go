// Package fake provides a concurrency-safe scripted in-memory runtime.
package fake

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"sync"

	"github.com/brendatamaar/arcana/internal/runtime"
)

const (
	maxInstances     = 1000
	maxScripts       = 256
	maxRecordedInput = 1048576
	maxScriptOutput  = 16777216
)

// Script specifies output, completion gates and injected exec failures.
// Gates model delays without clocks or background goroutines.
type Script struct {
	Stdout      []byte
	Stderr      []byte
	Exit        runtime.ExitInfo
	ExecError   error
	WaitError   error
	ResizeError error
	OutputGate  <-chan struct{}
	ExitGate    <-chan struct{}
}

// Failures injects driver-operation failures independently of exec scripts.
type Failures struct {
	Create  error
	Destroy error
	Stats   error
	List    error
}

// Driver owns fake instances and a bounded FIFO of exec scripts.
type Driver struct {
	mu        sync.Mutex
	instances map[string]runtime.Instance
	specs     map[string]runtime.Spec
	scripts   []Script
	lifetimes map[string]lifetime
	failures  Failures
}

var _ runtime.Driver = (*Driver)(nil)

// New returns an empty fake driver.
func New() *Driver {
	return &Driver{instances: make(map[string]runtime.Instance), specs: make(map[string]runtime.Spec), lifetimes: make(map[string]lifetime)}
}

// Name identifies this driver.
func (*Driver) Name() string { return "fake" }

// Capabilities reports the fake's supported features.
func (*Driver) Capabilities() runtime.Capabilities { return runtime.Capabilities{SupportsTTY: true} }

// SetFailures replaces the failure configuration.
func (d *Driver) SetFailures(failures Failures) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failures = failures
}

// QueueExec appends one script; output is copied and bounded.
func (d *Driver) QueueExec(script Script) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.scripts) >= maxScripts || len(script.Stdout) > maxScriptOutput || len(script.Stderr) > maxScriptOutput-len(script.Stdout) {
		return runtime.Invalid{Field: "script limits"}
	}
	script.Stdout = slices.Clone(script.Stdout)
	script.Stderr = slices.Clone(script.Stderr)
	d.scripts = append(d.scripts, script)
	return nil
}

// Create stores an independent instance/spec snapshot.
func (d *Driver) Create(ctx context.Context, spec runtime.Spec) (runtime.Instance, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Instance{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures.Create != nil {
		return runtime.Instance{}, d.failures.Create
	}
	if spec.SandboxID == "" || spec.Image == "" {
		return runtime.Instance{}, runtime.Invalid{Field: "sandbox_id/image"}
	}
	id := "fake-" + spec.SandboxID
	if _, ok := d.instances[id]; ok {
		return runtime.Instance{}, runtime.Invalid{Field: "duplicate sandbox_id"}
	}
	if len(d.instances) >= maxInstances {
		return runtime.Instance{}, runtime.Invalid{Field: "instance limit"}
	}
	instance := runtime.Instance{ID: id, SandboxID: spec.SandboxID, Labels: maps.Clone(spec.Labels)}
	d.instances[id] = instance
	d.specs[id] = cloneSpec(spec)
	lifeCtx, cancel := context.WithCancel(context.Background())
	d.lifetimes[id] = lifetime{lifeCtx, cancel}
	return cloneInstance(instance), nil
}

// Destroy removes an instance and is idempotent for missing instances.
func (d *Driver) Destroy(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures.Destroy != nil {
		return d.failures.Destroy
	}
	if life, ok := d.lifetimes[id]; ok {
		life.cancel()
		delete(d.lifetimes, id)
	}
	delete(d.instances, id)
	delete(d.specs, id)
	return nil
}

// Exec consumes the next script, or succeeds with empty output by default.
func (d *Driver) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.instances[id]; !ok {
		return nil, runtime.NotFound{ID: id}
	}
	if len(request.Cmd) == 0 || request.Cmd[0] == "" {
		return nil, runtime.Invalid{Field: "cmd"}
	}
	var script Script
	if len(d.scripts) > 0 {
		script = d.scripts[0]
		d.scripts[0] = Script{}
		d.scripts = d.scripts[1:]
	}
	if script.ExecError != nil {
		return nil, script.ExecError
	}
	if request.TTY {
		script.Stdout = append(slices.Clone(script.Stdout), script.Stderr...)
		script.Stderr = nil
	}
	session := &Session{ctx: ctx, instanceCtx: d.lifetimes[id].ctx, script: script, request: cloneRequest(request), stdout: bytes.NewReader(script.Stdout), stderr: bytes.NewReader(script.Stderr)}
	return session, nil
}

// Stats returns zero usage for present fake instances.
func (d *Driver) Stats(ctx context.Context, id string) (runtime.Stats, error) {
	if err := ctx.Err(); err != nil {
		return runtime.Stats{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures.Stats != nil {
		return runtime.Stats{}, d.failures.Stats
	}
	if _, ok := d.instances[id]; !ok {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	return runtime.Stats{}, nil
}

// List returns stable, independent snapshots of all fake instances.
func (d *Driver) List(ctx context.Context) ([]runtime.Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failures.List != nil {
		return nil, d.failures.List
	}
	result := make([]runtime.Instance, 0, len(d.instances))
	for _, instance := range d.instances {
		result = append(result, cloneInstance(instance))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// Spec returns an independent copy for assertions.
func (d *Driver) Spec(id string) (runtime.Spec, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	spec, ok := d.specs[id]
	return cloneSpec(spec), ok
}

func cloneInstance(instance runtime.Instance) runtime.Instance {
	instance.Labels = maps.Clone(instance.Labels)
	return instance
}

func cloneSpec(spec runtime.Spec) runtime.Spec {
	spec.Labels = maps.Clone(spec.Labels)
	spec.Env = maps.Clone(spec.Env)
	return spec
}

func cloneRequest(request runtime.ExecRequest) runtime.ExecRequest {
	request.Cmd = slices.Clone(request.Cmd)
	request.Env = maps.Clone(request.Env)
	return request
}

type lifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// Session is a scripted exec with bounded stdin recording.
type Session struct {
	mu          sync.Mutex
	ctx         context.Context
	instanceCtx context.Context
	script      Script
	request     runtime.ExecRequest
	stdout      *bytes.Reader
	stderr      *bytes.Reader
	input       bytes.Buffer
	closed      bool
	cols        uint16
	rows        uint16
}

var _ runtime.ExecSession = (*Session)(nil)

// Stdin returns the session's bounded input writer.
func (s *Session) Stdin() io.WriteCloser { return s }

// Stdout returns gated script output.
func (s *Session) Stdout() io.Reader {
	return gatedReader{ctx: s.ctx, instanceCtx: s.instanceCtx, gate: s.script.OutputGate, reader: s.stdout}
}

// Stderr is absent in TTY mode.
func (s *Session) Stderr() io.Reader {
	if s.request.TTY {
		return nil
	}
	return gatedReader{ctx: s.ctx, instanceCtx: s.instanceCtx, gate: s.script.OutputGate, reader: s.stderr}
}

// Write records stdin up to a fixed test-memory bound.
func (s *Session) Write(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.instanceCtx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if len(p) > maxRecordedInput-s.input.Len() {
		return 0, runtime.Invalid{Field: "recorded stdin limit"}
	}
	return s.input.Write(p)
}

// Close closes stdin without ending the command.
func (s *Session) Close() error { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; return nil }

// Resize records dimensions for TTY sessions.
func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.request.TTY {
		return runtime.Unsupported{Feature: "resize without TTY"}
	}
	if cols == 0 || rows == 0 {
		return runtime.Invalid{Field: "terminal size"}
	}
	if s.script.ResizeError != nil {
		return s.script.ResizeError
	}
	s.cols, s.rows = cols, rows
	return nil
}

// Wait waits on an optional gate while respecting both caller and exec contexts.
func (s *Session) Wait(ctx context.Context) (runtime.ExitInfo, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ExitInfo{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return runtime.ExitInfo{}, err
	}
	if err := s.instanceCtx.Err(); err != nil {
		return runtime.ExitInfo{}, err
	}
	if s.script.ExitGate != nil {
		select {
		case <-ctx.Done():
			return runtime.ExitInfo{}, ctx.Err()
		case <-s.ctx.Done():
			return runtime.ExitInfo{}, s.ctx.Err()
		case <-s.instanceCtx.Done():
			return runtime.ExitInfo{}, s.instanceCtx.Err()
		case <-s.script.ExitGate:
		}
	}
	return s.script.Exit, s.script.WaitError
}

// Input returns a copy of recorded stdin.
func (s *Session) Input() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.input.Bytes())
}

// Request returns the independent exec request snapshot.
func (s *Session) Request() runtime.ExecRequest { return cloneRequest(s.request) }

// Size returns the last terminal dimensions.
func (s *Session) Size() (uint16, uint16) { s.mu.Lock(); defer s.mu.Unlock(); return s.cols, s.rows }

type gatedReader struct {
	ctx         context.Context
	instanceCtx context.Context
	gate        <-chan struct{}
	reader      io.Reader
}

func (r gatedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if err := r.instanceCtx.Err(); err != nil {
		return 0, err
	}
	if r.gate != nil {
		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-r.instanceCtx.Done():
			return 0, r.instanceCtx.Err()
		case <-r.gate:
		}
	}
	return r.reader.Read(p)
}

// String intentionally omits script output, environment values and stdin.
func (d *Driver) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprintf("fake runtime (%d instances)", len(d.instances))
}
