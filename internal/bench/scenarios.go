package bench

import (
	"context"
	"fmt"
	"time"

	"github.com/brendatamaar/arcana/pkg/client"
)

// coldCreate times creation with the image already cached. A label disqualifies a request from the
// warm pool, so every sample is a real cold start; one unmeasured create warms the image first.
func (r *Runner) coldCreate(ctx context.Context, cfg Config) []Result {
	metric := "cold_create"
	request := client.CreateSandboxRequest{Template: cfg.Template}
	warm, err := r.create(ctx, cfg, request)
	if err != nil {
		return failed(metric, err)
	}
	r.destroy(ctx, warm.Id)
	var samples []time.Duration
	failures, notCold := 0, 0
	for i := 0; i < cfg.Iterations && ctx.Err() == nil; i++ {
		var row client.Sandbox
		d, err := r.timed(func() error {
			var createErr error
			row, createErr = r.create(ctx, cfg, request)
			return createErr
		})
		if err != nil {
			failures++
			continue
		}
		if row.Source != "cold" {
			notCold++
		}
		samples = append(samples, d)
		r.destroy(ctx, row.Id)
	}
	res := summarize(metric, samples, failures)
	if notCold > 0 {
		res.Note = fmt.Sprintf("%d of %d samples were not cold starts", notCold, len(samples))
	}
	return []Result{res}
}

// poolAcquire times creation from the warm pool. It needs templates[].warm_pool > 0 on the server;
// misses (a cold start because the pool was momentarily empty) are counted but not timed as hits.
func (r *Runner) poolAcquire(ctx context.Context, cfg Config) []Result {
	metric := "pool_acquire"
	request := client.CreateSandboxRequest{Template: cfg.Template}
	var samples []time.Duration
	misses, failures := 0, 0
	for attempts := 0; len(samples) < cfg.Iterations && attempts < cfg.Iterations*3 && ctx.Err() == nil; attempts++ {
		// Give the filler time to replace the member taken by the previous sample.
		if err := r.Clock.Sleep(ctx, 2*time.Second); err != nil {
			break
		}
		var row client.Sandbox
		d, err := r.timed(func() error {
			var createErr error
			row, createErr = r.createUnlabelled(ctx, cfg, request)
			return createErr
		})
		if err != nil {
			failures++
			continue
		}
		hit := row.Source == "pool"
		r.destroy(ctx, row.Id)
		if !hit {
			misses++
			if misses >= 3 && len(samples) == 0 {
				return []Result{{Metric: metric, Failures: failures, Note: fmt.Sprintf("no pool hits in %d attempts: set warm_pool > 0 for template %q on the server", misses, cfg.Template)}}
			}
			continue
		}
		samples = append(samples, d)
	}
	res := summarize(metric, samples, failures)
	if misses > 0 {
		res.Note = fmt.Sprintf("%d pool misses (cold starts) not counted", misses)
	}
	return []Result{res}
}

// createUnlabelled creates without the run label: any label makes a request ineligible for the pool.
// The sandbox is still tracked so cleanup removes it.
func (r *Runner) createUnlabelled(ctx context.Context, _ Config, request client.CreateSandboxRequest) (client.Sandbox, error) {
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

// execOverhead times a synchronous exec of /bin/true, which is almost entirely platform overhead.
func (r *Runner) execOverhead(ctx context.Context, cfg Config) []Result {
	metric := "exec_true"
	row, err := r.create(ctx, cfg, client.CreateSandboxRequest{Template: cfg.Template})
	if err != nil {
		return failed(metric, err)
	}
	argv := []string{"/bin/true"}
	run := func() error {
		result, execErr := r.API.Exec(ctx, row.Id, client.ExecRequest{Cmd: argv})
		if execErr == nil && result.ExitCode != 0 {
			return fmt.Errorf("/bin/true exited %d", result.ExitCode)
		}
		return execErr
	}
	for i := 0; i < 3 && ctx.Err() == nil; i++ { // warm up
		_ = run()
	}
	var samples []time.Duration
	failures := 0
	for i := 0; i < cfg.ExecIterations && ctx.Err() == nil; i++ {
		d, runErr := r.timed(run)
		if runErr != nil {
			failures++
			continue
		}
		samples = append(samples, d)
	}
	r.destroy(ctx, row.Id)
	return []Result{summarize(metric, samples, failures)}
}

// density creates idle sandboxes with a small memory limit until the server refuses, the host
// runs out of room, or DensityMax is reached. It reports how many it held at once.
func (r *Runner) density(ctx context.Context, cfg Config) []Result {
	metric := "density"
	request := client.CreateSandboxRequest{Template: cfg.Template, Resources: &client.Resources{MemoryMb: &cfg.DensityMemoryMB}}
	var ids []string
	var samples []time.Duration
	reason := fmt.Sprintf("reached the --density-max cap of %d", cfg.DensityMax)
	capped := true
	for len(ids) < cfg.DensityMax && ctx.Err() == nil {
		var row client.Sandbox
		d, err := r.timed(func() error {
			var createErr error
			row, createErr = r.create(ctx, cfg, request)
			return createErr
		})
		if err != nil {
			reason, capped = "server refused: "+errText(err), false
			break
		}
		ids = append(ids, row.Id)
		samples = append(samples, d)
	}
	count := float64(len(ids))
	for _, id := range ids {
		r.destroy(ctx, id)
	}
	res := summarize(metric, samples, 0)
	res.Value, res.Capped = &count, capped && ctx.Err() == nil
	res.Note = fmt.Sprintf("%d idle sandboxes of %d MB held at once; stopped: %s. Create latency is while filling.", len(ids), cfg.DensityMemoryMB, reason)
	return []Result{res}
}

// snapshotAndFork times snapshot creation, restore into a new sandbox, and forks of several sizes.
func (r *Runner) snapshotAndFork(ctx context.Context, cfg Config) []Result {
	row, err := r.create(ctx, cfg, client.CreateSandboxRequest{Template: cfg.Template})
	if err != nil {
		return failed("snapshot_create", err)
	}
	size := fmt.Sprintf("dd if=/dev/urandom of=/workspace/data.bin bs=1M count=%d 2>/dev/null", cfg.SnapshotMiB)
	timeout := int64(120)
	if result, execErr := r.API.Exec(ctx, row.Id, client.ExecRequest{Shell: &size, TimeoutSeconds: &timeout}); execErr != nil || result.ExitCode != 0 {
		if execErr == nil {
			execErr = fmt.Errorf("writing test data exited %d", result.ExitCode)
		}
		return failed("snapshot_create", execErr)
	}
	note := fmt.Sprintf("%d MiB of incompressible data in /workspace", cfg.SnapshotMiB)
	var snapSamples, restoreSamples []time.Duration
	snapFailures, restoreFailures := 0, 0
	for i := 0; i < cfg.Iterations && ctx.Err() == nil; i++ {
		var snap client.Snapshot
		d, snapErr := r.timed(func() error {
			var e error
			snap, e = r.API.CreateSnapshot(ctx, row.Id)
			return e
		})
		if snapErr != nil {
			snapFailures++
			r.logf("snapshot failed: %s", errText(snapErr))
			continue
		}
		r.trackSnapshot(snap.Id)
		snapSamples = append(snapSamples, d)
		from := snap.Id
		var restored client.Sandbox
		d, restoreErr := r.timed(func() error {
			var e error
			restored, e = r.create(ctx, cfg, client.CreateSandboxRequest{Template: cfg.Template, FromSnapshot: &from})
			return e
		})
		if restoreErr != nil {
			restoreFailures++
			r.logf("restore failed: %s", errText(restoreErr))
		} else {
			restoreSamples = append(restoreSamples, d)
			r.destroy(ctx, restored.Id)
		}
		if i < cfg.Iterations-1 { // keep the last snapshot for the fork measurements
			_ = r.API.DeleteSnapshot(ctx, snap.Id)
		}
	}
	results := []Result{summarize("snapshot_create", snapSamples, snapFailures), summarize("snapshot_restore", restoreSamples, restoreFailures)}
	results[0].Note, results[1].Note = note, note
	r.mu.Lock()
	var snapshot string
	if n := len(r.snapshots); n > 0 {
		snapshot = r.snapshots[n-1]
	}
	r.mu.Unlock()
	if snapshot == "" {
		return results
	}
	for _, count := range cfg.ForkCounts {
		var samples []time.Duration
		failures := 0
		partial := ""
		for i := 0; i < min(cfg.Iterations, 5) && ctx.Err() == nil; i++ {
			var forked client.ForkResult
			d, forkErr := r.timed(func() error {
				var e error
				forked, e = r.API.Fork(ctx, snapshot, client.ForkRequest{Count: int64(count)})
				return e
			})
			for _, member := range forked.Items {
				r.track(member.Id)
			}
			if forkErr != nil {
				failures++
				partial = errText(forkErr)
			} else {
				samples = append(samples, d)
				if len(forked.Failures) > 0 {
					partial = fmt.Sprintf("%d of %d members failed: %s", len(forked.Failures), count, forked.Failures[0].Code)
				}
			}
			for _, member := range forked.Items {
				r.destroy(ctx, member.Id)
			}
		}
		res := summarize(fmt.Sprintf("fork_%d", count), samples, failures)
		res.Note = note + "; time until all members are running"
		if partial != "" {
			res.Note += "; " + partial
		}
		results = append(results, res)
	}
	return results
}
