package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/brendatamaar/arcana/pkg/client"
)

// Arcana is the part of the Go client the agent uses; *client.Client satisfies it.
type Arcana interface {
	CreateSandbox(context.Context, client.CreateSandboxRequest) (client.Sandbox, error)
	DestroySandbox(context.Context, string) error
	Exec(context.Context, string, client.ExecRequest) (client.ExecResult, error)
	ReadFile(context.Context, string, string) (io.ReadCloser, error)
	WriteFile(context.Context, string, string, io.Reader) error
	ListFiles(context.Context, string, string, int64) (client.FileList, error)
	UploadArchive(context.Context, string, string, io.Reader) error
	CreateSnapshot(context.Context, string) (client.Snapshot, error)
	DeleteSnapshot(context.Context, string) error
	Fork(context.Context, string, client.ForkRequest) (client.ForkResult, error)
}

var _ Arcana = (*client.Client)(nil)

// arcanaWorkspace implements Workspace over one sandbox.
type arcanaWorkspace struct {
	api Arcana
	id  string
}

func (w arcanaWorkspace) Run(ctx context.Context, shell string, timeout int64) (RunResult, error) {
	result, err := w.api.Exec(ctx, w.id, client.ExecRequest{Shell: &shell, TimeoutSeconds: &timeout})
	if err != nil {
		return RunResult{}, err
	}
	var output strings.Builder
	if result.Stdout != "" {
		output.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		output.WriteString("\n[stderr]\n" + result.Stderr)
	}
	if result.TimedOut {
		output.WriteString("\n[timed out]")
	}
	return RunResult{ExitCode: int(result.ExitCode), Output: output.String()}, nil
}

func (w arcanaWorkspace) Read(ctx context.Context, path string) ([]byte, error) {
	body, err := w.api.ReadFile(ctx, w.id, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(io.LimitReader(body, maxFileRead+1))
}

func (w arcanaWorkspace) Write(ctx context.Context, path string, data []byte) error {
	return w.api.WriteFile(ctx, w.id, path, bytes.NewReader(data))
}

func (w arcanaWorkspace) List(ctx context.Context, path string, depth int64) ([]string, error) {
	list, err := w.api.ListFiles(ctx, w.id, path, depth)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, string(item.Type)+" "+item.Path)
	}
	return out, nil
}

// hiddenJudge runs hidden tests inside the sandbox between model turns and removes them (and
// any bytecode caches that would reveal their names) afterwards, so the model never sees them.
type hiddenJudge struct {
	api     Arcana
	id      string
	fixture Fixture
	hidden  []byte
	paths   []string
}

func newHiddenJudge(api Arcana, id string, fx Fixture) (*hiddenJudge, error) {
	judge := &hiddenJudge{api: api, id: id, fixture: fx}
	if fx.HiddenTest == "" {
		return judge, nil
	}
	var err error
	if judge.hidden, err = fx.Archive("hidden"); err != nil {
		return nil, err
	}
	if judge.paths, err = fx.hiddenPaths(); err != nil {
		return nil, err
	}
	return judge, nil
}

func (j *hiddenJudge) Solved(ctx context.Context) (bool, error) {
	command := j.fixture.VisibleTest
	if j.fixture.HiddenTest != "" {
		command = j.fixture.HiddenTest
		if err := j.api.UploadArchive(ctx, j.id, "/workspace", bytes.NewReader(j.hidden)); err != nil {
			return false, fmt.Errorf("placing hidden tests: %w", err)
		}
	}
	timeout := int64(maxCommandSeconds)
	result, runErr := j.api.Exec(ctx, j.id, client.ExecRequest{Shell: &command, TimeoutSeconds: &timeout})
	var cleanErr error
	if j.fixture.HiddenTest != "" {
		cleanup := "rm -rf -- " + strings.Join(quoteAll(j.paths), " ") + "; find /workspace -path '*__pycache__*' -name '*hidden*' -delete 2>/dev/null; true"
		short := int64(30)
		// A cancelled run must still clear the hidden files before the sandbox is reused.
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
		defer cancel()
		if _, cleanErr = j.api.Exec(cleanCtx, j.id, client.ExecRequest{Shell: &cleanup, TimeoutSeconds: &short}); cleanErr != nil {
			cleanErr = fmt.Errorf("removing hidden tests: %w", cleanErr)
		}
	}
	if err := errors.Join(runErr, cleanErr); err != nil {
		return false, err
	}
	return result.ExitCode == 0 && !result.TimedOut, nil
}

func quoteAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return out
}

// Deps are the collaborators of a run.
type Deps struct {
	Arcana Arcana
	LLM    LLM
	Log    io.Writer
}

// TaskOptions configure one fixture run.
type TaskOptions struct {
	// Parallel > 1 snapshots after setup and forks that many independent attempts.
	Parallel    int
	Budget      Budget
	Temperature float64
}

// Attempt is one agent run inside one sandbox.
type Attempt struct {
	Outcome
	Sandbox string
}

// TaskResult summarises a fixture run.
type TaskResult struct {
	Fixture  string
	Language string
	Solved   bool
	// Steps is the winning attempt's step count (or the most any attempt used when none solved);
	// Tokens is the total cost across all attempts.
	Steps    int
	Tokens   int
	Duration time.Duration
	Reason   string
	Attempts []Attempt
	// Winner indexes Attempts, or -1.
	Winner int
}

func taskPrompt(fx Fixture) string {
	return "The project in /workspace has a failing test. README.md describes the intended behaviour. " +
		"Run the visible tests with:\n\n    " + fx.VisibleTest + "\n\nFix the bug in the source code."
}

func (d Deps) logf(format string, args ...any) {
	if d.Log != nil {
		_, _ = fmt.Fprintf(d.Log, format+"\n", args...)
	}
}

// cleanup uses a fresh context so cancellation never leaves sandboxes behind.
func (d Deps) destroy(ids []string, snapshot string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, id := range ids {
		if err := d.Arcana.DestroySandbox(ctx, id); err != nil {
			d.logf("warning: destroying %s: %v", id, err)
		}
	}
	if snapshot != "" {
		if err := d.Arcana.DeleteSnapshot(ctx, snapshot); err != nil {
			d.logf("warning: deleting snapshot %s: %v", snapshot, err)
		}
	}
}

func (d Deps) newSandbox(ctx context.Context, fx Fixture) (string, error) {
	ttl := int64(1800)
	labels := map[string]string{"fix-agent": fx.Name}
	row, err := d.Arcana.CreateSandbox(ctx, client.CreateSandboxRequest{Template: fx.Template, TtlSeconds: &ttl, Labels: &labels})
	if err != nil {
		return "", fmt.Errorf("creating sandbox: %w", err)
	}
	return row.Id, nil
}

func (d Deps) upload(ctx context.Context, id string, archive []byte) error {
	return d.Arcana.UploadArchive(ctx, id, "/workspace", bytes.NewReader(archive))
}

// RunTask works one fixture end to end and always removes the sandboxes and snapshot it created.
func RunTask(ctx context.Context, d Deps, fx Fixture, opts TaskOptions) (TaskResult, error) {
	started := time.Now()
	result := TaskResult{Fixture: fx.Name, Language: fx.Language, Winner: -1}
	project, err := fx.Archive("project")
	if err != nil {
		return result, err
	}
	var created []string
	snapshot := ""
	defer func() { d.destroy(created, snapshot) }()

	base, err := d.newSandbox(ctx, fx)
	if err != nil {
		return result, err
	}
	created = append(created, base)
	if err = d.upload(ctx, base, project); err != nil {
		return result, fmt.Errorf("uploading project: %w", err)
	}
	members := []string{base}
	if opts.Parallel > 1 {
		snap, snapErr := d.Arcana.CreateSnapshot(ctx, base)
		if snapErr != nil {
			return result, fmt.Errorf("snapshotting the prepared workspace: %w", snapErr)
		}
		snapshot = snap.Id
		// The prepared sandbox is no longer needed; freeing its slot leaves room for every fork.
		d.destroy([]string{base}, "")
		created = created[:0]
		forked, forkErr := d.Arcana.Fork(ctx, snapshot, client.ForkRequest{Count: int64(opts.Parallel)})
		if forkErr != nil {
			return result, fmt.Errorf("forking attempts: %w", forkErr)
		}
		members = members[:0]
		for _, row := range forked.Items {
			members = append(members, row.Id)
			created = append(created, row.Id)
		}
		for _, failure := range forked.Failures {
			d.logf("warning: fork %d failed: %s: %s", failure.Index, failure.Code, failure.Message)
		}
	}
	result.Attempts = d.runAttempts(ctx, fx, members, opts)
	finalize(&result, started)
	return result, nil
}

// runAttempts runs one agent per sandbox. The first attempt to solve the task cancels the rest.
func (d Deps) runAttempts(ctx context.Context, fx Fixture, members []string, opts TaskOptions) []Attempt {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	attempts := make([]Attempt, len(members))
	var wg sync.WaitGroup
	var once sync.Once
	winner := -1
	for i, id := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempts[i] = Attempt{Sandbox: id}
			judge, err := newHiddenJudge(d.Arcana, id, fx)
			if err != nil {
				attempts[i].Outcome = Outcome{Reason: "error", Err: err}
				return
			}
			agent := &Agent{LLM: d.LLM, Workspace: arcanaWorkspace{api: d.Arcana, id: id}, Judge: judge, Budget: opts.Budget, Temperature: min(1.0, opts.Temperature+0.2*float64(i))}
			outcome := agent.Run(runCtx, taskPrompt(fx))
			attempts[i].Outcome = outcome
			if outcome.Solved {
				once.Do(func() { winner = i; cancel() })
			} else if outcome.Err != nil && winner >= 0 && errors.Is(outcome.Err, context.Canceled) {
				attempts[i].Reason, attempts[i].Err = "cancelled", nil
			}
		}()
	}
	wg.Wait()
	return attempts
}

func finalize(result *TaskResult, started time.Time) {
	result.Duration = time.Since(started).Round(time.Millisecond)
	for i, a := range result.Attempts {
		result.Tokens += a.Tokens
		if a.Solved && result.Winner < 0 {
			result.Winner, result.Solved = i, true
		}
		if !result.Solved {
			result.Steps = max(result.Steps, a.Steps)
		}
	}
	switch {
	case result.Solved:
		result.Steps, result.Reason = result.Attempts[result.Winner].Steps, "solved"
	case len(result.Attempts) > 0:
		result.Reason = result.Attempts[0].Reason
		for _, a := range result.Attempts {
			if a.Err != nil {
				result.Reason = "error: " + truncate(a.Err.Error(), 120)
				break
			}
		}
	default:
		result.Reason = "no attempts ran"
	}
}
