package bench

import (
	"fmt"
	"runtime"
	"strings"
)

// Target is a PRD §8.2 goal for one latency percentile (milliseconds) or a minimum count.
type Target struct {
	Percentile string  `json:"percentile"`
	LimitMS    float64 `json:"limit_ms,omitempty"`
	MinCount   float64 `json:"min_count,omitempty"`
	Source     string  `json:"source"`
}

// targets maps metrics to the PRD §8.2 table. The p99 pool target is checked separately below.
var targets = map[string]Target{
	"cold_create":  {Percentile: "p50", LimitMS: 2000, Source: "cold create p50 ≤ 2 s (image cached)"},
	"pool_acquire": {Percentile: "p50", LimitMS: 150, Source: "pool acquire p50 ≤ 150 ms and p99 ≤ 500 ms"},
	"exec_true":    {Percentile: "p50", LimitMS: 100, Source: "exec overhead for `true` p50 ≤ 100 ms"},
	"density":      {Percentile: "count", MinCount: 20, Source: "≥ 20 idle 256 MB sandboxes per 8 GB host"},
}

// ApplyTargets attaches PRD targets and whether each was met. Metrics with failures only or no
// samples are left without a verdict rather than counted as met.
func ApplyTargets(results []Result) {
	for i := range results {
		r := &results[i]
		target, ok := targets[r.Metric]
		if !ok {
			continue
		}
		t := target
		r.Target = &t
		var met bool
		switch {
		case r.Metric == "density":
			if r.Value == nil || r.Capped && *r.Value < t.MinCount {
				// Stopping at our own cap says nothing about the host's limit: no verdict.
				continue
			}
			met = *r.Value >= t.MinCount
		case r.Samples == 0:
			continue
		case r.Metric == "pool_acquire":
			met = r.P50MS <= t.LimitMS && r.P99MS <= 500
		default:
			met = r.P50MS <= t.LimitMS
		}
		r.Met = &met
	}
}

// ClientDescription identifies the machine running the benchmark (not necessarily the server).
func ClientDescription() string {
	return fmt.Sprintf("%s/%s, %s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.Version(), runtime.NumCPU())
}

// Markdown renders a report as a self-contained summary.
func Markdown(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Arcana benchmark results\n\n")
	fmt.Fprintf(&b, "- Started: %s (took %.0f s)\n- Server: %s\n- Runtime: %s\n- Template: %s\n- Benchmark client: %s\n", r.StartedAt.Format("2006-01-02 15:04:05 MST"), r.Duration, r.Server, orNone(r.Runtime), r.Template, r.Client)
	if r.HostNote != "" {
		fmt.Fprintf(&b, "- Host: %s\n", r.HostNote)
	}
	fmt.Fprintf(&b, "- Samples: %d create/pool/snapshot, %d exec; density cap %d at %d MB\n\n", r.Config.Iterations, r.Config.ExecIterations, r.Config.DensityMax, r.Config.DensityMemoryMB)
	fmt.Fprintf(&b, "| Metric | Samples | Failures | p50 | p95 | p99 | max | Target | Verdict |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, m := range r.Results {
		target, verdict := "-", "-"
		if m.Target != nil {
			target = m.Target.Source
			verdict = "not measured"
			if m.Met != nil {
				verdict = map[bool]string{true: "met", false: "MISSED"}[*m.Met]
			}
		}
		if m.Value != nil {
			fmt.Fprintf(&b, "| %s | %d | %d | count %.0f | | | | %s | %s |\n", m.Metric, m.Samples, m.Failures, *m.Value, target, verdict)
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %s | %s | %s | %s | %s |\n", m.Metric, m.Samples, m.Failures, ms(m.P50MS, m.Samples), ms(m.P95MS, m.Samples), ms(m.P99MS, m.Samples), ms(m.MaxMS, m.Samples), target, verdict)
	}
	b.WriteString("\n")
	for _, m := range r.Results {
		if m.Note != "" {
			fmt.Fprintf(&b, "- **%s**: %s\n", m.Metric, m.Note)
		}
	}
	b.WriteString("\nNot measured by this harness: Arcana idle RSS (≤ 80 MB target) and dashboard JavaScript size (≤ 200 KB gzip target).\n")
	return b.String()
}

func ms(v float64, samples int) string {
	if samples == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.1f ms", v)
}

func orNone(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
