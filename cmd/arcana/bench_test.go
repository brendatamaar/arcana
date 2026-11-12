package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/bench"
	"github.com/brendatamaar/arcana/pkg/client"
)

// instantAPI answers immediately; it exercises output handling, not timing.
type instantAPI struct {
	next      int
	destroyed int
}

func (a *instantAPI) CreateSandbox(context.Context, client.CreateSandboxRequest) (client.Sandbox, error) {
	a.next++
	return client.Sandbox{Id: "sbx_x", Source: "cold", Runtime: "runsc"}, nil
}
func (a *instantAPI) DestroySandbox(context.Context, string) error { a.destroyed++; return nil }
func (a *instantAPI) Exec(context.Context, string, client.ExecRequest) (client.ExecResult, error) {
	return client.ExecResult{}, nil
}

func (a *instantAPI) CreateSnapshot(context.Context, string) (client.Snapshot, error) {
	return client.Snapshot{Id: "snp_x"}, nil
}
func (a *instantAPI) DeleteSnapshot(context.Context, string) error { return nil }
func (a *instantAPI) Fork(context.Context, string, client.ForkRequest) (client.ForkResult, error) {
	return client.ForkResult{}, nil
}

func TestRunBench_WritesJSONAndMarkdownWithoutSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "results")
	api := &instantAPI{}
	var stdout, stderr bytes.Buffer
	cfg := bench.Config{Scenarios: []string{bench.ColdCreate, bench.ExecOverhead}, Iterations: 2, ExecIterations: 3, RunID: "t"}
	if err := runBench(context.Background(), api, "http://arcana:7070", cfg, dir, "test host", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "| cold_create |") || !strings.Contains(stdout.String(), "test host") {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "results.json")) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	var report bench.Report
	if err = json.Unmarshal(data, &report); err != nil || len(report.Results) != 2 || report.Server != "http://arcana:7070" || report.Client == "" || report.Runtime != "runsc" {
		t.Fatalf("%+v %v", report, err)
	}
	if md, readErr := os.ReadFile(filepath.Join(dir, "results.md")); // #nosec G304 -- test temp dir
	readErr != nil || !strings.Contains(string(md), "Arcana benchmark results") {
		t.Fatal(readErr)
	}
	if api.destroyed == 0 {
		t.Fatal("benchmark left sandboxes behind")
	}
}

func TestRunBench_RejectsUnknownScenariosAndNeverWritesOnFailure(t *testing.T) {
	dir := t.TempDir()
	err := runBench(context.Background(), &instantAPI{}, "x", bench.Config{Scenarios: []string{"bogus"}}, filepath.Join(dir, "out"), "", &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "unknown scenario") {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out")); statErr == nil {
		t.Fatal("output directory created for an invalid run")
	}
}

func TestBenchCommand_FlagsAndHelp(t *testing.T) {
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"bench", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--scenario", "--host-note", "--density-max", "warm_pool", "arcana-bench"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	root = newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--server", "http://127.0.0.1:1", "--api-key", "k", "bench", "--scenario", "bogus"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "unknown scenario") {
		t.Fatal(err)
	}
}
