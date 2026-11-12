// Package bench measures Arcana through its public API against the PRD §8.2 targets.
//
// It is a client: it needs a running Arcana, creates real sandboxes with the key it is given and
// removes everything it created. Results describe the host and configuration they were measured on.
package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/brendatamaar/arcana/pkg/client"
)

// Scenario names accepted by Config.Scenarios.
const (
	ColdCreate   = "cold-create"
	PoolAcquire  = "pool-acquire"
	ExecOverhead = "exec-overhead"
	Density      = "density"
	Snapshot     = "snapshot"
)

// AllScenarios lists every scenario in execution order.
var AllScenarios = []string{ColdCreate, PoolAcquire, ExecOverhead, Density, Snapshot}

// API is the part of the Go client the benchmarks use; *client.Client satisfies it.
type API interface {
	CreateSandbox(context.Context, client.CreateSandboxRequest) (client.Sandbox, error)
	DestroySandbox(context.Context, string) error
	Exec(context.Context, string, client.ExecRequest) (client.ExecResult, error)
	CreateSnapshot(context.Context, string) (client.Snapshot, error)
	DeleteSnapshot(context.Context, string) error
	Fork(context.Context, string, client.ForkRequest) (client.ForkResult, error)
}

var _ API = (*client.Client)(nil)

// Clock makes timing and waiting deterministic in tests.
type Clock interface {
	Now() time.Time
	Sleep(context.Context, time.Duration) error
}

// SystemClock is wall time.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// Sleep waits for d or the context.
func (SystemClock) Sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Config selects and sizes scenarios. Zero values take the documented defaults.
type Config struct {
	Scenarios []string
	Template  string
	// Iterations is the sample count for the create, pool and snapshot scenarios; ExecIterations for exec.
	Iterations     int
	ExecIterations int
	// DensityMax stops the density scenario even if the server would accept more.
	DensityMax int
	// DensityMemoryMB is each density sandbox's memory limit (PRD §8.2 uses 256).
	DensityMemoryMB int64
	SnapshotMiB     int
	ForkCounts      []int
	// RunID labels every sandbox the run creates, so leftovers are recognisable.
	RunID string
}

// WithDefaults fills unset fields.
func (c Config) WithDefaults() Config {
	if len(c.Scenarios) == 0 {
		c.Scenarios = slices.Clone(AllScenarios)
	}
	if c.Template == "" {
		c.Template = "base"
	}
	if c.Iterations == 0 {
		c.Iterations = 20
	}
	if c.ExecIterations == 0 {
		c.ExecIterations = 100
	}
	if c.DensityMax == 0 {
		c.DensityMax = 50
	}
	if c.DensityMemoryMB == 0 {
		c.DensityMemoryMB = 256
	}
	if c.SnapshotMiB == 0 {
		c.SnapshotMiB = 10
	}
	if len(c.ForkCounts) == 0 {
		c.ForkCounts = []int{2, 4}
	}
	return c
}

// Validate rejects unknown scenarios and unbounded sizes.
func (c Config) Validate() error {
	for _, name := range c.Scenarios {
		if !slices.Contains(AllScenarios, name) {
			return fmt.Errorf("unknown scenario %q (have %v)", name, AllScenarios)
		}
	}
	if c.Iterations < 1 || c.Iterations > 1000 || c.ExecIterations < 1 || c.ExecIterations > 10000 ||
		c.DensityMax < 1 || c.DensityMax > 1000 || c.DensityMemoryMB < 16 || c.DensityMemoryMB > 8192 || c.SnapshotMiB < 1 || c.SnapshotMiB > 500 {
		return errors.New("iterations 1..1000, exec iterations 1..10000, density max 1..1000, density memory 16..8192 MB and snapshot size 1..500 MiB")
	}
	for _, n := range c.ForkCounts {
		if n < 1 || n > 100 {
			return errors.New("fork counts must be 1..100")
		}
	}
	return nil
}

// Result is one measured metric.
type Result struct {
	Metric   string  `json:"metric"`
	Samples  int     `json:"samples"`
	Failures int     `json:"failures"`
	MinMS    float64 `json:"min_ms"`
	P50MS    float64 `json:"p50_ms"`
	P95MS    float64 `json:"p95_ms"`
	P99MS    float64 `json:"p99_ms"`
	MaxMS    float64 `json:"max_ms"`
	MeanMS   float64 `json:"mean_ms"`
	// Value carries a non-latency outcome (for example the density count).
	Value *float64 `json:"value,omitempty"`
	// Capped means the scenario stopped at a configured cap, not at a limit of the system under
	// test, so a Value below its target proves nothing.
	Capped bool   `json:"capped,omitempty"`
	Note   string `json:"note,omitempty"`
	// Target is the PRD §8.2 target for this metric, when one exists.
	Target *Target `json:"target,omitempty"`
	Met    *bool   `json:"met,omitempty"`
}

// Report is the complete output of a run.
type Report struct {
	StartedAt time.Time `json:"started_at"`
	Duration  float64   `json:"duration_seconds"`
	Server    string    `json:"server"`
	Runtime   string    `json:"runtime,omitempty"`
	Template  string    `json:"template"`
	HostNote  string    `json:"host_note,omitempty"`
	Client    string    `json:"client"`
	Config    Config    `json:"config"`
	Results   []Result  `json:"results"`
}

// Runner executes scenarios and always cleans up what it created.
type Runner struct {
	API   API
	Clock Clock
	Log   io.Writer

	mu        sync.Mutex
	sandboxes []string
	snapshots []string
	runtime   string
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		_, _ = fmt.Fprintf(r.Log, format+"\n", args...)
	}
}

func (r *Runner) track(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sandboxes = append(r.sandboxes, id)
}

func (r *Runner) trackSnapshot(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshots = append(r.snapshots, id)
}

// Cleanup destroys every sandbox and snapshot the runner created, using a fresh context so a
// cancelled run still cleans up. It returns the first failures it meets.
func (r *Runner) Cleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	r.mu.Lock()
	sandboxes, snapshots := r.sandboxes, r.snapshots
	r.sandboxes, r.snapshots = nil, nil
	r.mu.Unlock()
	var result error
	for _, id := range sandboxes {
		result = errors.Join(result, r.API.DestroySandbox(ctx, id))
	}
	for _, id := range snapshots {
		result = errors.Join(result, r.API.DeleteSnapshot(ctx, id))
	}
	return result
}

// create makes a labelled sandbox and records it for cleanup.
func (r *Runner) create(ctx context.Context, cfg Config, request client.CreateSandboxRequest) (client.Sandbox, error) {
	labels := map[string]string{"arcana-bench": cfg.RunID}
	if request.Labels != nil {
		for k, v := range *request.Labels {
			labels[k] = v
		}
	}
	request.Labels = &labels
	row, err := r.API.CreateSandbox(ctx, request)
	if err != nil {
		return row, err
	}
	r.track(row.Id)
	if r.runtime == "" {
		r.runtime = row.Runtime
	}
	return row, nil
}

func (r *Runner) destroy(ctx context.Context, id string) {
	if err := r.API.DestroySandbox(ctx, id); err != nil {
		r.logf("warning: destroying %s: %v", id, err)
	}
}

// timed measures f with the runner's clock.
func (r *Runner) timed(f func() error) (time.Duration, error) {
	start := r.Clock.Now()
	err := f()
	return r.Clock.Now().Sub(start), err
}

// Run executes the configured scenarios in order. A failing scenario is reported in its result and
// does not stop the others; context cancellation does.
func (r *Runner) Run(ctx context.Context, server string, cfg Config, hostNote string) (Report, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return Report{}, err
	}
	started := r.Clock.Now()
	report := Report{StartedAt: started.UTC(), Server: server, Template: cfg.Template, HostNote: hostNote, Config: cfg}
	defer func() { r.logf("cleanup: %v", r.Cleanup()) }()
	for _, name := range cfg.Scenarios {
		if ctx.Err() != nil {
			break
		}
		r.logf("== %s", name)
		var results []Result
		switch name {
		case ColdCreate:
			results = r.coldCreate(ctx, cfg)
		case PoolAcquire:
			results = r.poolAcquire(ctx, cfg)
		case ExecOverhead:
			results = r.execOverhead(ctx, cfg)
		case Density:
			results = r.density(ctx, cfg)
		case Snapshot:
			results = r.snapshotAndFork(ctx, cfg)
		}
		report.Results = append(report.Results, results...)
	}
	report.Runtime = r.runtime
	report.Duration = r.Clock.Now().Sub(started).Seconds()
	ApplyTargets(report.Results)
	return report, ctx.Err()
}

// summarize turns latency samples into a Result using nearest-rank percentiles.
func summarize(metric string, samples []time.Duration, failures int) Result {
	res := Result{Metric: metric, Samples: len(samples), Failures: failures}
	if len(samples) == 0 {
		return res
	}
	sorted := slices.Clone(samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total time.Duration
	for _, s := range sorted {
		total += s
	}
	ms := func(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*100) / 100 }
	res.MinMS, res.MaxMS = ms(sorted[0]), ms(sorted[len(sorted)-1])
	res.P50MS, res.P95MS, res.P99MS = ms(percentile(sorted, 50)), ms(percentile(sorted, 95)), ms(percentile(sorted, 99))
	res.MeanMS = ms(total / time.Duration(len(sorted)))
	return res
}

// percentile is the nearest-rank percentile of an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[min(max(rank, 1), len(sorted))-1]
}

func failed(metric string, err error) []Result {
	return []Result{{Metric: metric, Failures: 1, Note: "scenario failed: " + errText(err)}}
}

func errText(err error) string {
	var apiErr *client.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code + ": " + apiErr.Message
	}
	return err.Error()
}
