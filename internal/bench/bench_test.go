package bench

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/pkg/client"
)

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error { c.advance(d); return nil }

// fakeAPI charges a fixed latency per operation on the shared clock and records what is alive.
type fakeAPI struct {
	clock       *fakeClock
	mu          sync.Mutex
	next        int
	alive       map[string]bool
	snapshots   map[string]bool
	createDelay time.Duration
	execDelay   time.Duration
	poolHits    func(n int) bool // whether unlabelled create number n comes from the pool
	capacity    int              // creates beyond this fail with quota_exceeded; 0 = unlimited
	labels      []map[string]string
	snapshotErr error
	forkFail    int // members reported failed per fork
}

func newAPI(clock *fakeClock) *fakeAPI {
	return &fakeAPI{clock: clock, alive: map[string]bool{}, snapshots: map[string]bool{}, createDelay: 1500 * time.Millisecond, execDelay: 40 * time.Millisecond, poolHits: func(int) bool { return false }}
}

func (f *fakeAPI) CreateSandbox(_ context.Context, in client.CreateSandboxRequest) (client.Sandbox, error) {
	f.clock.advance(f.createDelay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.capacity > 0 && len(f.alive) >= f.capacity {
		return client.Sandbox{}, &client.Error{StatusCode: 429, Code: "quota_exceeded", Message: "sandbox quota exceeded"}
	}
	f.next++
	id := fmt.Sprintf("sbx_%d", f.next)
	f.alive[id] = true
	var source gen.SandboxSource = "cold"
	if in.Labels == nil && f.poolHits(f.next) {
		source = "pool"
	}
	if in.FromSnapshot != nil {
		source = "snapshot"
	}
	if in.Labels != nil {
		f.labels = append(f.labels, *in.Labels)
	}
	return client.Sandbox{Id: id, Source: source, Runtime: "runsc"}, nil
}

func (f *fakeAPI) DestroySandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, id)
	return nil
}

func (f *fakeAPI) Exec(context.Context, string, client.ExecRequest) (client.ExecResult, error) {
	f.clock.advance(f.execDelay)
	return client.ExecResult{}, nil
}

func (f *fakeAPI) CreateSnapshot(context.Context, string) (client.Snapshot, error) {
	f.clock.advance(2 * time.Second)
	if f.snapshotErr != nil {
		return client.Snapshot{}, f.snapshotErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("snp_%d", f.next)
	f.snapshots[id] = true
	return client.Snapshot{Id: id}, nil
}

func (f *fakeAPI) DeleteSnapshot(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.snapshots, id)
	return nil
}

func (f *fakeAPI) Fork(_ context.Context, _ string, in client.ForkRequest) (client.ForkResult, error) {
	f.clock.advance(time.Duration(in.Count) * time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	var out client.ForkResult
	for i := 0; i < int(in.Count); i++ {
		if i < f.forkFail {
			out.Failures = append(out.Failures, client.ForkFailure{Index: int64(i), Code: "quota_exceeded"})
			continue
		}
		f.next++
		id := fmt.Sprintf("sbx_%d", f.next)
		f.alive[id] = true
		out.Items = append(out.Items, client.Sandbox{Id: id})
	}
	return out, nil
}

func runner(api *fakeAPI, clock *fakeClock) *Runner { return &Runner{API: api, Clock: clock} }

func find(t *testing.T, results []Result, metric string) Result {
	t.Helper()
	for _, r := range results {
		if r.Metric == metric {
			return r
		}
	}
	t.Fatalf("no %s in %+v", metric, results)
	return Result{}
}

func TestSummarize_NearestRankPercentiles(t *testing.T) {
	var samples []time.Duration
	for i := 100; i >= 1; i-- {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	r := summarize("m", samples, 2)
	if r.Samples != 100 || r.Failures != 2 || r.MinMS != 1 || r.P50MS != 50 || r.P95MS != 95 || r.P99MS != 99 || r.MaxMS != 100 || r.MeanMS != 50.5 {
		t.Fatalf("%+v", r)
	}
	one := summarize("m", []time.Duration{7 * time.Millisecond}, 0)
	if one.P50MS != 7 || one.P99MS != 7 {
		t.Fatalf("%+v", one)
	}
	if empty := summarize("m", nil, 3); empty.Samples != 0 || empty.P50MS != 0 || empty.Failures != 3 {
		t.Fatalf("%+v", empty)
	}
	// The input must not be reordered: callers keep their sample order.
	if samples[0] != 100*time.Millisecond {
		t.Fatal("summarize sorted the caller's slice")
	}
}

func TestConfig_DefaultsAndValidation(t *testing.T) {
	c := Config{}.WithDefaults()
	if len(c.Scenarios) != len(AllScenarios) || c.Template != "base" || c.Iterations != 20 || c.DensityMemoryMB != 256 {
		t.Fatalf("%+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Config{{Scenarios: []string{"nope"}}, {Iterations: -1}, {Iterations: 5000}, {DensityMax: 5000}, {SnapshotMiB: 9999}, {DensityMemoryMB: 4}, {ForkCounts: []int{0}}} {
		if bad.WithDefaults().Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestColdCreate_ForcesColdStartsAndCleansUp(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	res := runner(api, clock).coldCreate(context.Background(), Config{Iterations: 5, RunID: "r1", Template: "base"})
	r := find(t, res, "cold_create")
	if r.Samples != 5 || r.P50MS != 1500 || r.Note != "" {
		t.Fatalf("%+v", r)
	}
	for _, labels := range api.labels {
		if labels["arcana-bench"] != "r1" {
			t.Fatalf("labels %v", labels)
		}
	}
	if len(api.alive) != 0 {
		t.Fatal("sandboxes left alive")
	}
	api.poolHits = func(int) bool { return true } // would only matter for unlabelled requests
	if r = find(t, runner(api, clock).coldCreate(context.Background(), Config{Iterations: 2, RunID: "r"}), "cold_create"); r.Note != "" {
		t.Fatalf("labelled creates must stay cold: %+v", r)
	}
}

func TestPoolAcquire_CountsHitsOnlyAndExplainsMissingPools(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	api.createDelay = 60 * time.Millisecond
	api.poolHits = func(n int) bool { return n%3 != 0 } // every third request misses
	res := runner(api, clock).poolAcquire(context.Background(), Config{Iterations: 4, RunID: "r"})
	r := find(t, res, "pool_acquire")
	if r.Samples != 4 || r.P50MS != 60 || !strings.Contains(r.Note, "pool misses") {
		t.Fatalf("%+v", r)
	}
	for _, labels := range api.labels {
		if len(labels) != 0 {
			t.Fatal("pool requests must not carry labels: they would disqualify the pool")
		}
	}
	if len(api.alive) != 0 {
		t.Fatal("leaked")
	}
	// No pool configured: stop early with an actionable note instead of looping.
	none := newAPI(clock)
	r = find(t, runner(none, clock).poolAcquire(context.Background(), Config{Iterations: 20, RunID: "r", Template: "python"}), "pool_acquire")
	if r.Samples != 0 || !strings.Contains(r.Note, "warm_pool") || none.next > 5 {
		t.Fatalf("%+v after %d creates", r, none.next)
	}
}

func TestExecOverhead_WarmsUpAndMeasuresEachCall(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	r := find(t, runner(api, clock).execOverhead(context.Background(), Config{ExecIterations: 10, RunID: "r"}), "exec_true")
	if r.Samples != 10 || r.P50MS != 40 || r.Failures != 0 || len(api.alive) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestDensity_StopsAtTheServerLimitAndReleasesEverything(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	api.capacity = 7
	r := find(t, runner(api, clock).density(context.Background(), Config{DensityMax: 50, DensityMemoryMB: 256, RunID: "r"}), "density")
	if r.Value == nil || *r.Value != 7 || r.Capped || !strings.Contains(r.Note, "quota_exceeded") || len(api.alive) != 0 {
		t.Fatalf("%+v alive=%d", r, len(api.alive))
	}
	api.capacity = 0
	r = find(t, runner(api, clock).density(context.Background(), Config{DensityMax: 4, DensityMemoryMB: 256, RunID: "r"}), "density")
	if *r.Value != 4 || !r.Capped || !strings.Contains(r.Note, "--density-max") {
		t.Fatalf("%+v", r)
	}
}

func TestSnapshotAndFork_MeasuresEachPhaseAndKeepsOnlyWhatItNeeds(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	api.forkFail = 1
	run := runner(api, clock)
	res := run.snapshotAndFork(context.Background(), Config{Iterations: 3, SnapshotMiB: 10, ForkCounts: []int{2, 4}, RunID: "r", Template: "base"}.WithDefaults())
	if r := find(t, res, "snapshot_create"); r.Samples != 3 || r.P50MS != 2000 || !strings.Contains(r.Note, "10 MiB") {
		t.Fatalf("%+v", r)
	}
	if r := find(t, res, "snapshot_restore"); r.Samples != 3 || r.P50MS != 1500 {
		t.Fatalf("%+v", r)
	}
	if r := find(t, res, "fork_2"); r.Samples != 3 || r.P50MS != 2000 || !strings.Contains(r.Note, "1 of 2 members failed") {
		t.Fatalf("%+v", r)
	}
	if r := find(t, res, "fork_4"); r.P50MS != 4000 {
		t.Fatalf("%+v", r)
	}
	// Only the source sandbox and the final snapshot remain until cleanup.
	if len(api.snapshots) != 1 {
		t.Fatalf("snapshots kept: %d", len(api.snapshots))
	}
	if err := run.Cleanup(); err != nil || len(api.alive) != 0 || len(api.snapshots) != 0 {
		t.Fatalf("cleanup: %v alive=%d snaps=%d", err, len(api.alive), len(api.snapshots))
	}
	// A failing snapshot is reported, not fatal, and fork measurements are skipped.
	failing := newAPI(clock)
	failing.snapshotErr = &client.Error{Code: "quota_exceeded", Message: "snapshot quota"}
	res = runner(failing, clock).snapshotAndFork(context.Background(), Config{Iterations: 2, ForkCounts: []int{2}, RunID: "r", Template: "base"}.WithDefaults())
	if r := find(t, res, "snapshot_create"); r.Samples != 0 || r.Failures != 2 || len(res) != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestRun_ReportsTargetsAndAlwaysCleansUp(t *testing.T) {
	clock := &fakeClock{at: time.Unix(1_700_000_000, 0)}
	api := newAPI(clock)
	api.createDelay = 100 * time.Millisecond
	api.poolHits = func(int) bool { return true }
	run := runner(api, clock)
	report, err := run.Run(context.Background(), "http://arcana", Config{Iterations: 3, ExecIterations: 5, DensityMax: 3, SnapshotMiB: 1, ForkCounts: []int{2}, RunID: "r", Scenarios: AllScenarios}, "2 vCPU test host")
	if err != nil {
		t.Fatal(err)
	}
	if report.Runtime != "runsc" || report.HostNote != "2 vCPU test host" || len(report.Results) != 7 {
		t.Fatalf("%+v", report)
	}
	if met := find(t, report.Results, "cold_create").Met; met == nil || !*met {
		t.Fatal("100 ms cold create should meet the 2 s target")
	}
	if d := find(t, report.Results, "density"); d.Met != nil || !d.Capped || d.Target == nil {
		t.Fatalf("a run stopped by its own cap of 3 must not claim a verdict: %+v", d)
	}
	if len(api.alive) != 0 || len(api.snapshots) != 0 {
		t.Fatal("leaked resources")
	}
	md := Markdown(report)
	for _, want := range []string{"| cold_create |", "met", "not measured", "2 vCPU test host", "Not measured by this harness"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	if _, err = run.Run(context.Background(), "x", Config{Scenarios: []string{"bogus"}}, ""); err == nil {
		t.Fatal("bad config accepted")
	}
}

func TestRun_CancellationStopsAndStillCleansUp(t *testing.T) {
	clock := &fakeClock{at: time.Unix(0, 0)}
	api := newAPI(clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := runner(api, clock).Run(ctx, "x", Config{RunID: "r"}, "")
	if !errors.Is(err, context.Canceled) || len(report.Results) != 0 || len(api.alive) != 0 {
		t.Fatalf("%v %+v", err, report)
	}
}

func TestApplyTargets_DensityVerdictOnlyWhenTheSystemSetTheLimit(t *testing.T) {
	count := func(v float64) *float64 { return &v }
	results := []Result{
		{Metric: "density", Value: count(12), Capped: true}, // our cap: inconclusive
		{Metric: "density", Value: count(12)},               // the server refused at 12: a real miss
		{Metric: "density", Value: count(25), Capped: true}, // our cap, but already past the target
	}
	ApplyTargets(results)
	if results[0].Met != nil || results[1].Met == nil || *results[1].Met || results[2].Met == nil || !*results[2].Met {
		t.Fatalf("%+v %+v %+v", results[0].Met, results[1].Met, results[2].Met)
	}
}

func TestApplyTargets_PoolNeedsBothPercentilesAndNoSamplesGetNoVerdict(t *testing.T) {
	results := []Result{
		{Metric: "pool_acquire", Samples: 5, P50MS: 100, P99MS: 600},
		{Metric: "exec_true", Samples: 0},
		{Metric: "fork_2", Samples: 3, P50MS: 1},
	}
	ApplyTargets(results)
	if results[0].Met == nil || *results[0].Met {
		t.Fatal("p99 above 500 ms must miss")
	}
	if results[1].Met != nil || results[1].Target == nil {
		t.Fatal("a metric with no samples must not claim a verdict")
	}
	if results[2].Target != nil {
		t.Fatal("untargeted metric got a target")
	}
}
