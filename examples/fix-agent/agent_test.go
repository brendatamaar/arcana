package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type scriptedLLM struct {
	mu      sync.Mutex
	replies []Completion
	err     error
	calls   [][]Message
	temps   []float64
}

func (l *scriptedLLM) Chat(_ context.Context, messages []Message, _ []ToolSpec, temperature float64) (Completion, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, append([]Message(nil), messages...))
	l.temps = append(l.temps, temperature)
	if l.err != nil {
		return Completion{}, l.err
	}
	if len(l.replies) == 0 {
		return Completion{Message: Message{Role: "assistant", Content: "out of script"}, TotalTokens: 1}, nil
	}
	reply := l.replies[0]
	l.replies = l.replies[1:]
	return reply, nil
}

func call(id, name string, args any) ToolCall {
	data, _ := json.Marshal(args)
	var c ToolCall
	c.ID, c.Type = id, "function"
	c.Function.Name, c.Function.Arguments = name, string(data)
	return c
}

func turn(tokens int, calls ...ToolCall) Completion {
	return Completion{Message: Message{Role: "assistant", ToolCalls: calls}, TotalTokens: tokens}
}

type memWorkspace struct {
	mu       sync.Mutex
	files    map[string]string
	commands []string
	runErr   error
}

func newMem() *memWorkspace {
	return &memWorkspace{files: map[string]string{"/workspace/code.py": "buggy", "/workspace/README.md": "spec"}}
}

func (m *memWorkspace) Run(_ context.Context, shell string, _ int64) (RunResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, shell)
	if m.runErr != nil {
		return RunResult{}, m.runErr
	}
	return RunResult{ExitCode: 1, Output: strings.Repeat("o", 20000)}, nil
}

func (m *memWorkspace) Read(_ context.Context, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[path]
	if !ok {
		return nil, errors.New("file_not_found: " + path)
	}
	return []byte(data), nil
}

func (m *memWorkspace) Write(_ context.Context, path string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[path] = string(data)
	return nil
}

func (m *memWorkspace) List(_ context.Context, _ string, _ int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for name := range m.files {
		out = append(out, "file "+name)
	}
	return out, nil
}

// fixedJudge solves the task once code.py holds the fix, and counts how often it was consulted.
type fixedJudge struct {
	ws     *memWorkspace
	checks int
	err    error
}

func (j *fixedJudge) Solved(context.Context) (bool, error) {
	j.checks++
	if j.err != nil {
		return false, j.err
	}
	j.ws.mu.Lock()
	defer j.ws.mu.Unlock()
	return j.ws.files["/workspace/code.py"] == "fixed", nil
}

func newAgent(llm LLM, ws *memWorkspace, judge Judge, budget Budget) *Agent {
	return &Agent{LLM: llm, Workspace: ws, Judge: judge, Budget: budget, Temperature: 0.2}
}

func TestAgent_FixesAfterReadingAndStopsWhenTheJudgeSaysSolved(t *testing.T) {
	ws := newMem()
	judge := &fixedJudge{ws: ws}
	llm := &scriptedLLM{replies: []Completion{
		turn(10, call("1", "read_file", map[string]any{"path": "/workspace/code.py"})),
		turn(20, call("2", "write_file", map[string]any{"path": "/workspace/code.py", "content": "fixed"})),
		turn(30, call("3", "run_command", map[string]any{"command": "never reached"})),
	}}
	out := newAgent(llm, ws, judge, Budget{MaxSteps: 10, MaxTokens: 1000}).Run(context.Background(), "task")
	if !out.Solved || out.Steps != 2 || out.Tokens != 30 || out.Reason != "solved" || out.Err != nil {
		t.Fatalf("%+v", out)
	}
	if judge.checks != 1 {
		t.Fatalf("judge consulted %d times; reads must not trigger it", judge.checks)
	}
	// The model saw the file it read and the first prompt, and nothing about hidden tests.
	if got := llm.calls[1][len(llm.calls[1])-1]; got.Role != "tool" || got.Content != "buggy" || got.ToolCallID != "1" {
		t.Fatalf("tool result not fed back: %+v", got)
	}
	if llm.calls[0][0].Role != "system" || llm.calls[0][1].Content != "task" {
		t.Fatal("prompt order")
	}
}

func TestAgent_BudgetsAndModelStopping(t *testing.T) {
	spin := func(n int) []Completion {
		var out []Completion
		for i := 0; i < n; i++ {
			out = append(out, turn(400, call(fmt.Sprint(i), "list_files", map[string]any{"path": "/workspace"})))
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		llm    *scriptedLLM
		budget Budget
		reason string
		steps  int
	}{
		{"step budget", &scriptedLLM{replies: spin(10)}, Budget{MaxSteps: 3, MaxTokens: 100000}, "step budget", 3},
		{"token budget", &scriptedLLM{replies: spin(10)}, Budget{MaxSteps: 50, MaxTokens: 1000}, "token budget", 3},
		{"model stops", &scriptedLLM{replies: []Completion{{Message: Message{Role: "assistant", Content: "all done"}, TotalTokens: 5}}}, Budget{MaxSteps: 5, MaxTokens: 1000}, "model stopped", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := newMem()
			out := newAgent(tc.llm, ws, &fixedJudge{ws: ws}, tc.budget).Run(context.Background(), "t")
			if out.Solved || out.Reason != tc.reason || out.Steps != tc.steps {
				t.Fatalf("%+v", out)
			}
		})
	}
}

func TestAgent_FinalJudgeCheckCatchesAFixMadeByTheLastStep(t *testing.T) {
	ws := newMem()
	ws.files["/workspace/code.py"] = "fixed" // already fixed, no tool call ever mutated it
	out := newAgent(&scriptedLLM{replies: []Completion{{Message: Message{Role: "assistant", Content: "done"}, TotalTokens: 1}}}, ws, &fixedJudge{ws: ws}, Budget{MaxSteps: 2, MaxTokens: 100}).Run(context.Background(), "t")
	if !out.Solved {
		t.Fatalf("%+v", out)
	}
}

func TestAgent_UntrustedModelOutputNeverCrashesTheLoop(t *testing.T) {
	ws := newMem()
	var many []ToolCall
	for i := 0; i < maxToolCallsPerTurn+3; i++ {
		many = append(many, call(fmt.Sprint("m", i), "list_files", map[string]any{"path": "/workspace"}))
	}
	bad := ToolCall{ID: "bad"}
	bad.Function.Name, bad.Function.Arguments = "run_command", `{"command": 5}`
	extra := ToolCall{ID: "extra"}
	extra.Function.Name, extra.Function.Arguments = "write_file", `{"path":"/workspace/x","content":"y","surprise":1}`
	llm := &scriptedLLM{replies: []Completion{
		turn(1, bad, extra, call("unknown", "rm_rf", map[string]any{})),
		turn(1, many...),
		turn(1, call("big", "run_command", map[string]any{"command": "yes", "timeout_seconds": 100000}), call("empty", "run_command", map[string]any{"command": "  "}), call("miss", "read_file", map[string]any{"path": "/workspace/nope"})),
	}}
	// A fourth call lets the model see the third turn's results; it then runs out of script and stops.
	out := newAgent(llm, ws, &fixedJudge{ws: ws}, Budget{MaxSteps: 4, MaxTokens: 100}).Run(context.Background(), "t")
	if out.Err != nil || out.Reason != "model stopped" {
		t.Fatalf("%+v", out)
	}
	if _, wrote := ws.files["/workspace/x"]; wrote {
		t.Fatal("a call with unknown fields was executed")
	}
	results := map[string]string{}
	for _, messages := range llm.calls[1:] {
		for _, m := range messages {
			if m.Role == "tool" {
				results[m.ToolCallID] = m.Content
			}
		}
	}
	for id, want := range map[string]string{"bad": "arguments must be", "extra": "arguments must be", "unknown": "unknown tool", "empty": "command must be", "miss": "file_not_found"} {
		if !strings.Contains(results[id], want) {
			t.Errorf("%s: %q", id, results[id])
		}
	}
	if !strings.Contains(results[fmt.Sprint("m", maxToolCallsPerTurn)], "too many tool calls") {
		t.Fatal("tool calls beyond the per-turn cap were executed")
	}
	if len(results["big"]) > maxToolOutput+20 || !strings.Contains(results["big"], "[truncated]") {
		t.Fatalf("tool output not bounded: %d", len(results["big"]))
	}
	if len(ws.commands) != 1 {
		t.Fatalf("commands: %v", ws.commands)
	}
}

func TestAgent_PropagatesLLMJudgeAndCancellationFailures(t *testing.T) {
	ws := newMem()
	boom := errors.New("endpoint down")
	if out := newAgent(&scriptedLLM{err: boom}, ws, &fixedJudge{ws: ws}, Budget{MaxSteps: 2, MaxTokens: 10}).Run(context.Background(), "t"); !errors.Is(out.Err, boom) || out.Reason != "error" {
		t.Fatalf("%+v", out)
	}
	judge := &fixedJudge{ws: ws, err: errors.New("judge broke")}
	llm := &scriptedLLM{replies: []Completion{turn(1, call("1", "write_file", map[string]any{"path": "/workspace/a", "content": "x"}))}}
	if out := newAgent(llm, ws, judge, Budget{MaxSteps: 2, MaxTokens: 10}).Run(context.Background(), "t"); out.Err == nil || out.Reason != "error" {
		t.Fatalf("%+v", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	llm = &scriptedLLM{replies: []Completion{turn(1, call("1", "list_files", map[string]any{"path": "/workspace"}))}}
	if out := newAgent(llm, ws, &fixedJudge{ws: ws}, Budget{MaxSteps: 5, MaxTokens: 100}).Run(ctx, "t"); !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("%+v", out)
	}
}

func TestTruncate_NeverSplitsRunes(t *testing.T) {
	got := truncate(strings.Repeat("a", 9)+"é", 10)
	if !strings.HasPrefix(got, strings.Repeat("a", 9)) || strings.ContainsRune(got, '�') {
		t.Fatalf("%q", got)
	}
	if truncate("short", 10) != "short" {
		t.Fatal("changed short text")
	}
}
