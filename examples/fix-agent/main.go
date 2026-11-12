// Command fix-agent is a reference coding agent: given a project with a failing test it creates an
// Arcana sandbox, lets an LLM explore and fix the code through tools, and verifies the fix with tests.
//
// It exists to demonstrate Arcana (sandboxes, files, snapshots and forks), not as a product.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/brendatamaar/arcana/pkg/client"
)

const usage = `usage:
  fix-agent run    --fixture DIR [--parallel N] [flags]       fix one fixture
  fix-agent run    --dir DIR --template T --test CMD [flags]  fix any project with a failing test
  fix-agent eval   [--fixtures DIR] [--only a,b] [--parallel N] [--json]
  fix-agent verify-fixtures [--fixtures DIR]                  prove each fixture is a real, fixable bug

Environment:
  ARCANA_URL, ARCANA_API_KEY            Arcana server (or --server / --api-key)
  FIXAGENT_BASE_URL                     OpenAI-compatible endpoint (default http://localhost:11434/v1, Ollama)
  FIXAGENT_MODEL                        model name (default qwen2.5-coder:7b)
  FIXAGENT_API_KEY                      endpoint key, if it needs one`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "fix-agent:", err)
		os.Exit(1)
	}
}

type commonFlags struct {
	server, apiKey        string
	fixtures              string
	parallel              int
	maxSteps, maxTokens   int
	temperature           float64
	jsonOut               bool
	only, fixture, dir    string
	template, testCommand string
}

func parseFlags(name string, args []string, getenv func(string) string, stderr io.Writer) (*commonFlags, error) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(stderr)
	f := &commonFlags{}
	set.StringVar(&f.server, "server", envOr(getenv, "ARCANA_URL", "http://127.0.0.1:7070"), "Arcana server URL")
	set.StringVar(&f.apiKey, "api-key", getenv("ARCANA_API_KEY"), "Arcana API key")
	set.StringVar(&f.fixtures, "fixtures", "examples/fixtures", "fixtures directory")
	set.IntVar(&f.parallel, "parallel", 1, "attempts to run in parallel from a snapshot (1 = single attempt)")
	set.IntVar(&f.maxSteps, "max-steps", 20, "model calls per attempt")
	set.IntVar(&f.maxTokens, "max-tokens", 60000, "total tokens per attempt (sum of per-request totals)")
	set.Float64Var(&f.temperature, "temperature", 0.2, "sampling temperature of the first attempt")
	set.BoolVar(&f.jsonOut, "json", false, "print JSON")
	set.StringVar(&f.only, "only", "", "comma-separated fixture names")
	set.StringVar(&f.fixture, "fixture", "", "fixture directory to run")
	set.StringVar(&f.dir, "dir", "", "project directory (with --template and --test)")
	set.StringVar(&f.template, "template", "", "template for --dir")
	set.StringVar(&f.testCommand, "test", "", "test command for --dir")
	if err := set.Parse(args); err != nil {
		return nil, err
	}
	if f.parallel < 1 || f.parallel > 10 || f.maxSteps < 1 || f.maxSteps > 100 || f.maxTokens < 1000 || f.maxTokens > 2_000_000 {
		return nil, errors.New("--parallel must be 1..10, --max-steps 1..100 and --max-tokens 1000..2000000")
	}
	return f, nil
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(stdout, usage)
		return err
	}
	f, err := parseFlags(args[0], args[1:], getenv, stderr)
	if err != nil {
		return err
	}
	arcana, err := client.New(f.server, f.apiKey, client.Options{})
	if err != nil {
		return err
	}
	deps := Deps{Arcana: arcana, Log: stderr}
	switch args[0] {
	case "verify-fixtures":
		return verifyFixtures(ctx, deps, f, stdout)
	case "run", "eval":
		model := envOr(getenv, "FIXAGENT_MODEL", "qwen2.5-coder:7b")
		llm, llmErr := NewOpenAIChat(envOr(getenv, "FIXAGENT_BASE_URL", "http://localhost:11434/v1"), model, getenv("FIXAGENT_API_KEY"))
		if llmErr != nil {
			return llmErr
		}
		deps.LLM = llm
		opts := TaskOptions{Parallel: f.parallel, Budget: Budget{MaxSteps: f.maxSteps, MaxTokens: f.maxTokens}, Temperature: f.temperature}
		if args[0] == "run" {
			return runOne(ctx, deps, f, opts, model, stdout)
		}
		return evalAll(ctx, deps, f, opts, model, stdout)
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

func runOne(ctx context.Context, d Deps, f *commonFlags, opts TaskOptions, model string, stdout io.Writer) error {
	var fx Fixture
	switch {
	case f.fixture != "":
		loaded, err := loadFixture(f.fixture)
		if err != nil {
			return err
		}
		fx = loaded
	case f.dir != "" && f.template != "" && f.testCommand != "":
		// No hidden tests: the visible test command judges success.
		fx = Fixture{Name: "project", Language: f.template, Template: f.template, VisibleTest: f.testCommand, projectDir: f.dir}
	default:
		return errors.New("run needs --fixture DIR, or --dir DIR with --template and --test")
	}
	result, err := RunTask(ctx, d, fx, opts)
	if err != nil {
		return err
	}
	return printResults(stdout, []TaskResult{result}, model, f.jsonOut)
}

func evalAll(ctx context.Context, d Deps, f *commonFlags, opts TaskOptions, model string, stdout io.Writer) error {
	fixtures, err := selectFixtures(f)
	if err != nil {
		return err
	}
	var results []TaskResult
	for _, fx := range fixtures {
		d.logf("== %s", fx.Name)
		result, runErr := RunTask(ctx, d, fx, opts)
		if runErr != nil {
			result.Fixture, result.Language, result.Reason = fx.Name, fx.Language, "error: "+truncate(runErr.Error(), 120)
		}
		results = append(results, result)
		if ctx.Err() != nil {
			break
		}
	}
	return printResults(stdout, results, model, f.jsonOut)
}

func selectFixtures(f *commonFlags) ([]Fixture, error) {
	all, err := LoadFixtures(f.fixtures)
	if err != nil {
		return nil, err
	}
	if f.only == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, name := range strings.Split(f.only, ",") {
		want[strings.TrimSpace(name)] = true
	}
	var out []Fixture
	for _, fx := range all {
		if want[fx.Name] {
			out = append(out, fx)
			delete(want, fx.Name)
		}
	}
	for name := range want {
		return nil, fmt.Errorf("unknown fixture %q", name)
	}
	return out, nil
}

func verifyFixtures(ctx context.Context, d Deps, f *commonFlags, stdout io.Writer) error {
	fixtures, err := selectFixtures(f)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "FIXTURE\tVISIBLE FAILS\tHIDDEN FAILS\tSOLUTION: VISIBLE PASSES\tHIDDEN PASSES\tRESULT")
	bad := 0
	for _, fx := range fixtures {
		v := d.VerifyFixture(ctx, fx)
		status := "ok"
		if !v.OK() {
			status = "FAILED"
			bad++
			if v.Err != nil {
				status += ": " + truncate(v.Err.Error(), 80)
			}
		}
		_, _ = fmt.Fprintf(w, "%s\t%t\t%t\t%t\t%t\t%s\n", v.Fixture, v.VisibleFailsBefore, v.HiddenFailsBefore, v.VisiblePassesAfter, v.HiddenPassesAfter, status)
	}
	if err = w.Flush(); err != nil {
		return err
	}
	if bad > 0 {
		return fmt.Errorf("%d fixture(s) failed verification", bad)
	}
	return nil
}

func printResults(stdout io.Writer, results []TaskResult, model string, asJSON bool) error {
	if asJSON {
		type row struct {
			Fixture  string  `json:"fixture"`
			Language string  `json:"language"`
			Solved   bool    `json:"solved"`
			Steps    int     `json:"steps"`
			Tokens   int     `json:"tokens"`
			Seconds  float64 `json:"seconds"`
			Reason   string  `json:"reason"`
			Attempts int     `json:"attempts"`
			Model    string  `json:"model"`
		}
		rows := make([]row, 0, len(results))
		for _, r := range results {
			rows = append(rows, row{r.Fixture, r.Language, r.Solved, r.Steps, r.Tokens, r.Duration.Seconds(), r.Reason, len(r.Attempts), model})
		}
		return json.NewEncoder(stdout).Encode(rows)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "FIXTURE\tLANG\tRESULT\tATTEMPTS\tSTEPS\tTOKENS\tSECONDS\tNOTE")
	solved := 0
	for _, r := range results {
		verdict := "FAIL"
		if r.Solved {
			verdict = "pass"
			solved++
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%.1f\t%s\n", r.Fixture, r.Language, verdict, len(r.Attempts), r.Steps, r.Tokens, r.Duration.Seconds(), r.Reason)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "\nmodel %s: %d of %d fixtures solved\n", model, solved, len(results))
	return err
}
