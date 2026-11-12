package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Limits that keep one task bounded regardless of what the model does.
const (
	maxToolCallsPerTurn = 8
	maxToolOutput       = 8 << 10
	maxFileRead         = 64 << 10
	maxFileWrite        = 256 << 10
	maxCommandSeconds   = 120
)

// RunResult is a command's outcome as the model sees it.
type RunResult struct {
	ExitCode int
	Output   string
}

// Workspace is the sandbox as the agent uses it. The production implementation calls Arcana.
type Workspace interface {
	Run(ctx context.Context, shell string, timeoutSeconds int64) (RunResult, error)
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, path string, depth int64) ([]string, error)
}

// Judge decides, outside the model's view, whether the task is solved. It may place and remove
// hidden tests, so the model never sees them.
type Judge interface {
	Solved(ctx context.Context) (bool, error)
}

// Budget bounds one agent run.
type Budget struct {
	MaxSteps  int
	MaxTokens int
}

// Outcome reports how a run ended.
type Outcome struct {
	Solved bool
	Steps  int
	Tokens int
	// Reason is one of: solved, step budget, token budget, model stopped, error.
	Reason string
	Err    error
}

// Agent drives the tool loop for one workspace.
type Agent struct {
	LLM         LLM
	Workspace   Workspace
	Judge       Judge
	Budget      Budget
	Temperature float64
}

const systemPrompt = `You are a software engineer fixing a bug in a small project inside an isolated sandbox. ` +
	`Files are under /workspace and there is no network. Read the README and the failing test, find the cause, and fix the ` +
	`source code, not the tests. Use the tools to run commands, read files, write files and list files. ` +
	`Keep changes minimal. When the tests pass, reply with a short summary and no tool calls.`

func toolSpecs() []ToolSpec {
	spec := func(name, description string, props map[string]any, required ...string) ToolSpec {
		var tool ToolSpec
		tool.Type = "function"
		tool.Function.Name = name
		tool.Function.Description = description
		tool.Function.Parameters = map[string]any{"type": "object", "properties": props, "required": required}
		return tool
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	num := func(description string) map[string]any {
		return map[string]any{"type": "integer", "description": description}
	}
	return []ToolSpec{
		spec("run_command", "Run a shell command in /workspace and return its exit code and combined output (truncated).", map[string]any{"command": str("shell command"), "timeout_seconds": num("1-120, default 60")}, "command"),
		spec("read_file", "Read a text file under /workspace.", map[string]any{"path": str("absolute path")}, "path"),
		spec("write_file", "Create or replace a file under /workspace with the given content.", map[string]any{"path": str("absolute path"), "content": str("full new file content")}, "path", "content"),
		spec("list_files", "List files under a directory.", map[string]any{"path": str("absolute directory path"), "depth": num("1-5, default 1")}, "path"),
	}
}

// Run works the task until the judge reports it solved or a budget ends the run.
func (a *Agent) Run(ctx context.Context, task string) Outcome {
	messages := []Message{{Role: "system", Content: systemPrompt}, {Role: "user", Content: task}}
	out := Outcome{}
	tools := toolSpecs()
	for out.Steps < a.Budget.MaxSteps {
		if out.Tokens >= a.Budget.MaxTokens {
			out.Reason = "token budget"
			return out
		}
		out.Steps++
		reply, err := a.LLM.Chat(ctx, messages, tools, a.Temperature)
		if err != nil {
			out.Reason, out.Err = "error", err
			return out
		}
		out.Tokens += reply.TotalTokens
		messages = append(messages, reply.Message)
		if len(reply.Message.ToolCalls) == 0 {
			// The model considers itself done; the judge has the final word.
			return a.finish(ctx, out, "model stopped")
		}
		changed := false
		for i, call := range reply.Message.ToolCalls {
			if i >= maxToolCallsPerTurn {
				messages = append(messages, Message{Role: "tool", ToolCallID: call.ID, Content: "error: too many tool calls in one turn; call fewer tools"})
				continue
			}
			result, mutates := a.dispatch(ctx, call)
			changed = changed || mutates
			messages = append(messages, Message{Role: "tool", ToolCallID: call.ID, Content: truncate(result, maxToolOutput)})
		}
		if changed {
			solved, err := a.Judge.Solved(ctx)
			if err != nil {
				out.Reason, out.Err = "error", err
				return out
			}
			if solved {
				out.Solved, out.Reason = true, "solved"
				return out
			}
		}
		if ctx.Err() != nil {
			out.Reason, out.Err = "error", ctx.Err()
			return out
		}
	}
	return a.finish(ctx, out, "step budget")
}

// finish asks the judge once more so a fix made by the last step is not missed.
func (a *Agent) finish(ctx context.Context, out Outcome, reason string) Outcome {
	solved, err := a.Judge.Solved(ctx)
	switch {
	case err != nil:
		out.Reason, out.Err = "error", err
	case solved:
		out.Solved, out.Reason = true, "solved"
	default:
		out.Reason = reason
	}
	return out
}

type toolArguments struct {
	Command string `json:"command"`
	Timeout int64  `json:"timeout_seconds"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Depth   int64  `json:"depth"`
}

// dispatch runs one tool call. Failures are reported to the model as text, never as a crash: the
// model's output is untrusted input. mutates is true for calls that may change the workspace.
func (a *Agent) dispatch(ctx context.Context, call ToolCall) (result string, mutates bool) {
	var args toolArguments
	decoder := json.NewDecoder(strings.NewReader(call.Function.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return "error: arguments must be a JSON object with the documented fields", false
	}
	switch call.Function.Name {
	case "run_command":
		timeout := args.Timeout
		if timeout < 1 || timeout > maxCommandSeconds {
			timeout = 60
		}
		if strings.TrimSpace(args.Command) == "" || len(args.Command) > 8192 {
			return "error: command must be 1-8192 characters", false
		}
		run, err := a.Workspace.Run(ctx, args.Command, timeout)
		if err != nil {
			return toolError(err), true
		}
		return fmt.Sprintf("exit code %d\n%s", run.ExitCode, run.Output), true
	case "read_file":
		data, err := a.Workspace.Read(ctx, args.Path)
		if err != nil {
			return toolError(err), false
		}
		if len(data) > maxFileRead {
			return string(data[:maxFileRead]) + "\n[truncated]", false
		}
		return string(data), false
	case "write_file":
		if len(args.Content) > maxFileWrite {
			return "error: content too large", false
		}
		if err := a.Workspace.Write(ctx, args.Path, []byte(args.Content)); err != nil {
			return toolError(err), true
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), true
	case "list_files":
		depth := args.Depth
		if depth < 1 || depth > 5 {
			depth = 1
		}
		names, err := a.Workspace.List(ctx, args.Path, depth)
		if err != nil {
			return toolError(err), false
		}
		return strings.Join(names, "\n"), false
	default:
		return "error: unknown tool " + fmt.Sprintf("%q", call.Function.Name), false
	}
}

func toolError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "error: operation cancelled"
	}
	return "error: " + truncate(err.Error(), 300)
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + "\n[truncated]"
}
