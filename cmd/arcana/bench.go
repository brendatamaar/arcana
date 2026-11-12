package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/internal/bench"
)

func newBenchCommand() *cobra.Command {
	var (
		scenario                               string
		out, template, hostNote                string
		iterations, execIterations, densityMax int
		densityMemory, snapshotMiB             int64
		forkCounts                             []int
	)
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark a running Arcana against the PRD performance targets",
		Long: "Runs cold-create, pool-acquire, exec-overhead, density and snapshot/fork scenarios against the Arcana " +
			"selected by --server/--api-key and prints a Markdown table. With --out it also writes results.json and results.md. " +
			"It creates real sandboxes (labelled arcana-bench=<run id>) and removes everything it created, so it needs free capacity. " +
			"Pool-acquire requires templates[].warm_pool > 0 on the server. Density is bounded by the server's max_sandboxes " +
			"and the host. Record the host (--host-note): results are only meaningful with it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := apiClient(cmd)
			if err != nil {
				return err
			}
			server, _ := cmd.Flags().GetString("server")
			cfg := bench.Config{Template: template, Iterations: iterations, ExecIterations: execIterations, DensityMax: densityMax, DensityMemoryMB: densityMemory, SnapshotMiB: int(snapshotMiB), ForkCounts: forkCounts, RunID: fmt.Sprintf("%x", time.Now().UnixNano())}
			if scenario != "all" {
				cfg.Scenarios = strings.Split(scenario, ",")
			}
			return runBench(cmd.Context(), c, server, cfg, out, hostNote, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := cmd.Flags()
	f.StringVar(&scenario, "scenario", "all", "comma-separated scenarios: "+strings.Join(bench.AllScenarios, ", ")+", or all")
	f.StringVar(&out, "out", "", "directory for results.json and results.md")
	f.StringVar(&template, "template", "base", "template to benchmark")
	f.StringVar(&hostNote, "host-note", "", "describe the Arcana host (CPU, RAM, storage, runtime) for the report")
	f.IntVar(&iterations, "iterations", 20, "samples for create, pool and snapshot scenarios")
	f.IntVar(&execIterations, "exec-iterations", 100, "samples for the exec scenario")
	f.IntVar(&densityMax, "density-max", 50, "most sandboxes the density scenario will create")
	f.Int64Var(&densityMemory, "density-memory-mb", 256, "memory limit of each density sandbox")
	f.Int64Var(&snapshotMiB, "snapshot-mib", 10, "workspace data size for snapshot scenarios")
	f.IntSliceVar(&forkCounts, "fork-counts", []int{2, 4}, "fork sizes to measure")
	return cmd
}

// runBench is separate from the command so tests can supply a fake API.
func runBench(ctx context.Context, api bench.API, server string, cfg bench.Config, out, hostNote string, stdout, stderr io.Writer) error {
	runner := &bench.Runner{API: api, Clock: bench.SystemClock{}, Log: stderr}
	report, err := runner.Run(ctx, server, cfg, hostNote)
	report.Client = bench.ClientDescription()
	if err != nil && len(report.Results) == 0 {
		return err
	}
	markdown := bench.Markdown(report)
	if _, writeErr := io.WriteString(stdout, markdown); writeErr != nil {
		return writeErr
	}
	if out != "" {
		if writeErr := writeReport(out, report, markdown); writeErr != nil {
			return writeErr
		}
	}
	return err
}

func writeReport(dir string, report bench.Report, markdown string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	// The report names the server URL but never a key.
	if err = os.WriteFile(filepath.Join(dir, "results.json"), append(data, '\n'), 0o644); err != nil { // #nosec G306 -- benchmark output is meant to be shared
		return err
	}
	return os.WriteFile(filepath.Join(dir, "results.md"), []byte(markdown), 0o644) // #nosec G306 -- benchmark output is meant to be shared
}
