package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/arcana/pkg/client"
)

type (
	createInput struct {
		Template     string   `json:"template" jsonschema:"template name, for example base, python, node or go"`
		TTLSeconds   *int64   `json:"ttl_seconds,omitempty" jsonschema:"lifetime in seconds; capped by the server"`
		NetworkMode  string   `json:"network_mode,omitempty" jsonschema:"none (default, no network) or allowlist"`
		Allow        []string `json:"allow,omitempty" jsonschema:"hostnames reachable in allowlist mode"`
		Presets      []string `json:"presets,omitempty" jsonschema:"allowlist presets such as python, node or go"`
		FromSnapshot string   `json:"from_snapshot,omitempty" jsonschema:"snapshot ID to restore into /workspace; template must match it"`
		Keep         bool     `json:"keep,omitempty" jsonschema:"keep the sandbox when this MCP session ends instead of destroying it"`
	}
	sandboxOutput struct {
		ID        string `json:"id"`
		State     string `json:"state"`
		Template  string `json:"template"`
		Source    string `json:"source"`
		ExpiresAt string `json:"expires_at"`
		Session   string `json:"mcp_session"`
	}
	runInput struct {
		SandboxID string            `json:"sandbox_id"`
		Cmd       []string          `json:"cmd,omitempty" jsonschema:"argv array, run without a shell; set exactly one of cmd or shell"`
		Shell     string            `json:"shell,omitempty" jsonschema:"shell string run with /bin/sh -c; set exactly one of cmd or shell"`
		Cwd       string            `json:"cwd,omitempty" jsonschema:"working directory, default /workspace"`
		Env       map[string]string `json:"env,omitempty"`
		Timeout   *int64            `json:"timeout_seconds,omitempty" jsonschema:"kill the whole process tree after this many seconds; server default applies when omitted"`
	}
	runOutput struct {
		ExitCode       int    `json:"exit_code"`
		Stdout         string `json:"stdout"`
		Stderr         string `json:"stderr"`
		TimedOut       bool   `json:"timed_out"`
		Truncated      bool   `json:"truncated"`
		ReplyTruncated bool   `json:"reply_truncated"`
		DurationMillis int64  `json:"duration_ms"`
		ExecutionID    string `json:"exec_id"`
	}
	readInput struct {
		SandboxID string `json:"sandbox_id"`
		Path      string `json:"path" jsonschema:"absolute path under /workspace or /tmp"`
		MaxBytes  int64  `json:"max_bytes,omitempty" jsonschema:"most bytes to return (default 262144, at most 1048576)"`
	}
	readOutput struct {
		Content   string `json:"content"`
		Encoding  string `json:"encoding" jsonschema:"utf8, or base64 when the bytes are not valid UTF-8"`
		Truncated bool   `json:"truncated"`
	}
	writeInput struct {
		SandboxID string `json:"sandbox_id"`
		Path      string `json:"path" jsonschema:"absolute path under /workspace or /tmp; parent directories are created"`
		Content   string `json:"content"`
		Encoding  string `json:"encoding,omitempty" jsonschema:"utf8 (default) or base64"`
	}
	writeOutput struct {
		Path         string `json:"path"`
		BytesWritten int    `json:"bytes_written"`
	}
	listInput struct {
		SandboxID string `json:"sandbox_id"`
		Path      string `json:"path,omitempty" jsonschema:"directory, default /workspace"`
		Depth     int64  `json:"depth,omitempty" jsonschema:"1..100, default 1"`
	}
	fileEntry struct {
		Path string `json:"path"`
		Type string `json:"type"`
		Size int64  `json:"size_bytes"`
	}
	listOutput struct {
		Items []fileEntry `json:"items"`
	}
	snapshotInput struct {
		SandboxID string `json:"sandbox_id"`
	}
	snapshotOutput struct {
		ID        string `json:"id"`
		Template  string `json:"template"`
		SizeBytes int64  `json:"size_bytes"`
		Scope     string `json:"scope"`
	}
	forkInput struct {
		SnapshotID string `json:"snapshot_id"`
		Count      int64  `json:"count" jsonschema:"number of sandboxes to create, at most the server's snapshots.max_fork"`
		TTLSeconds *int64 `json:"ttl_seconds,omitempty"`
		Keep       bool   `json:"keep,omitempty" jsonschema:"keep the sandboxes when this MCP session ends"`
	}
	forkFailure struct {
		Index   int64  `json:"index"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	forkOutput struct {
		Sandboxes []sandboxOutput `json:"sandboxes"`
		Failures  []forkFailure   `json:"failures"`
	}
	destroyInput struct {
		SandboxID string `json:"sandbox_id"`
	}
	destroyOutput struct {
		Destroyed bool `json:"destroyed"`
	}
)

func (s *Server) sandbox(row client.Sandbox) sandboxOutput {
	return sandboxOutput{ID: row.Id, State: string(row.State), Template: row.Template, Source: string(row.Source), ExpiresAt: row.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"), Session: s.session}
}

func (s *Server) register(server *sdk.Server) {
	yes := true
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true}
	additive := &sdk.ToolAnnotations{DestructiveHint: new(bool)}
	destructive := &sdk.ToolAnnotations{DestructiveHint: &yes}
	sdk.AddTool(server, &sdk.Tool{Name: "create_sandbox", Annotations: additive, Description: "Create an isolated sandbox (gVisor, non-root, read-only root filesystem, no network by default) and return its ID. " +
		"Files live in /workspace. The sandbox is destroyed when this MCP session ends unless keep is true, and also after its TTL or when idle. " +
		"With from_snapshot the snapshot's /workspace is restored; the template must match the snapshot."}, s.create)
	sdk.AddTool(server, &sdk.Tool{Name: "run_command", Annotations: destructive, Description: "Run one command in a sandbox and wait for it. Use cmd (argv, no shell) or shell (/bin/sh -c), not both. " +
		"The command is killed with its whole process tree at timeout_seconds (server default and maximum apply). Combined stdout and stderr are capped by the server (10 MiB by default; truncated is set beyond that), and this tool returns at most 64 KiB of each stream (reply_truncated). " +
		"Processes do not outlive the command. Network is off unless the sandbox was created in allowlist mode."}, s.run)
	sdk.AddTool(server, &sdk.Tool{Name: "read_file", Annotations: readOnly, Description: "Read a file from a sandbox (paths under /workspace or /tmp). Returns at most max_bytes (default 256 KiB, limit 1 MiB); truncated is set when the file is larger. " +
		"Non-UTF-8 content is returned base64-encoded."}, s.read)
	sdk.AddTool(server, &sdk.Tool{Name: "write_file", Annotations: destructive, Description: "Write a file in a sandbox (paths under /workspace or /tmp), creating parent directories and replacing any existing file atomically. Content is limited to 1 MiB; the server's per-file limit also applies."}, s.write)
	sdk.AddTool(server, &sdk.Tool{Name: "list_files", Annotations: readOnly, Description: "List a sandbox directory (default /workspace), optionally recursively to depth 1..100. The server limits a listing to 1000 entries."}, s.list)
	sdk.AddTool(server, &sdk.Tool{Name: "snapshot_sandbox", Annotations: additive, Description: "Save a copy of a running sandbox's /workspace as a snapshot and return its ID. " +
		"Only /workspace is captured: not running processes, not files outside /workspace and not environment values. Stop writers first; a workspace that changes during the snapshot can make it fail. Snapshots persist after this session ends."}, s.snapshot)
	sdk.AddTool(server, &sdk.Tool{Name: "fork_sandbox", Annotations: additive, Description: "Create count sandboxes from one snapshot in parallel, each with its own independent copy of the snapshot's /workspace, to try alternatives side by side. " +
		"Returns the sandboxes created and a failure per index that could not be created (for example quota exceeded). Forks are destroyed when this session ends unless keep is true."}, s.fork)
	sdk.AddTool(server, &sdk.Tool{Name: "destroy_sandbox", Annotations: destructive, Description: "Destroy a sandbox and its workspace. Safe to repeat."}, s.destroy)
}

func (s *Server) create(ctx context.Context, _ *sdk.CallToolRequest, in createInput) (*sdk.CallToolResult, sandboxOutput, error) {
	labels := map[string]string{SessionLabel: s.session}
	request := client.CreateSandboxRequest{Template: in.Template, TtlSeconds: in.TTLSeconds, Labels: &labels}
	if in.FromSnapshot != "" {
		request.FromSnapshot = &in.FromSnapshot
	}
	if in.NetworkMode != "" || len(in.Allow) > 0 || len(in.Presets) > 0 {
		mode := in.NetworkMode
		if mode == "" {
			mode = "allowlist"
		}
		request.Network = &client.Network{Mode: client.NetworkMode(mode)}
		if len(in.Allow) > 0 {
			request.Network.Allow = &in.Allow
		}
		if len(in.Presets) > 0 {
			request.Network.Presets = &in.Presets
		}
	}
	row, err := s.api.CreateSandbox(ctx, request)
	if err != nil {
		return nil, sandboxOutput{}, apiError(err)
	}
	s.track(row.Id, in.Keep)
	return nil, s.sandbox(row), nil
}

func (s *Server) run(ctx context.Context, _ *sdk.CallToolRequest, in runInput) (*sdk.CallToolResult, runOutput, error) {
	if (len(in.Cmd) == 0) == (in.Shell == "") {
		return nil, runOutput{}, errors.New("set exactly one of cmd or shell")
	}
	request := client.ExecRequest{Cmd: in.Cmd, Cwd: in.Cwd, Env: in.Env, TimeoutSeconds: in.Timeout}
	if in.Shell != "" {
		request.Shell = &in.Shell
	}
	result, err := s.api.Exec(ctx, in.SandboxID, request)
	if err != nil {
		return nil, runOutput{}, apiError(err)
	}
	stdout, cutOut := clip(result.Stdout)
	stderr, cutErr := clip(result.Stderr)
	return nil, runOutput{ExitCode: int(result.ExitCode), Stdout: stdout, Stderr: stderr, TimedOut: result.TimedOut, Truncated: result.Truncated, ReplyTruncated: cutOut || cutErr, DurationMillis: result.DurationMs, ExecutionID: result.ExecId}, nil
}

// clip keeps the head of s within the reply cap without splitting a UTF-8 sequence.
func clip(text string) (string, bool) {
	if len(text) <= maxReturnedText {
		return text, false
	}
	cut := maxReturnedText
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

func (s *Server) read(ctx context.Context, _ *sdk.CallToolRequest, in readInput) (*sdk.CallToolResult, readOutput, error) {
	limit := in.MaxBytes
	if limit == 0 {
		limit = DefaultReadBytes
	}
	if limit < 1 || limit > MaxReadBytes {
		return nil, readOutput{}, fmt.Errorf("max_bytes must be within 1..%d", MaxReadBytes)
	}
	body, err := s.api.ReadFile(ctx, in.SandboxID, in.Path)
	if err != nil {
		return nil, readOutput{}, apiError(err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, readOutput{}, apiError(err)
	}
	truncated := int64(len(data)) > limit
	if truncated {
		data = data[:limit]
	}
	if utf8.Valid(data) {
		return nil, readOutput{Content: string(data), Encoding: "utf8", Truncated: truncated}, nil
	}
	// A cut can land inside a multi-byte rune; drop that tail before judging validity.
	if truncated {
		trimmed := data
		for i := 0; i < utf8.UTFMax && len(trimmed) > 0 && !utf8.Valid(trimmed); i++ {
			trimmed = trimmed[:len(trimmed)-1]
		}
		if utf8.Valid(trimmed) {
			return nil, readOutput{Content: string(trimmed), Encoding: "utf8", Truncated: true}, nil
		}
	}
	return nil, readOutput{Content: base64.StdEncoding.EncodeToString(data), Encoding: "base64", Truncated: truncated}, nil
}

func (s *Server) write(ctx context.Context, _ *sdk.CallToolRequest, in writeInput) (*sdk.CallToolResult, writeOutput, error) {
	var data []byte
	switch in.Encoding {
	case "", "utf8":
		data = []byte(in.Content)
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			return nil, writeOutput{}, errors.New("content is not valid base64")
		}
		data = decoded
	default:
		return nil, writeOutput{}, errors.New("encoding must be utf8 or base64")
	}
	if len(data) > MaxWriteBytes {
		return nil, writeOutput{}, fmt.Errorf("content exceeds %d bytes", MaxWriteBytes)
	}
	if err := s.api.WriteFile(ctx, in.SandboxID, in.Path, bytesReader(data)); err != nil {
		return nil, writeOutput{}, apiError(err)
	}
	return nil, writeOutput{Path: in.Path, BytesWritten: len(data)}, nil
}

func (s *Server) list(ctx context.Context, _ *sdk.CallToolRequest, in listInput) (*sdk.CallToolResult, listOutput, error) {
	path, depth := in.Path, in.Depth
	if path == "" {
		path = "/workspace"
	}
	if depth == 0 {
		depth = 1
	}
	list, err := s.api.ListFiles(ctx, in.SandboxID, path, depth)
	if err != nil {
		return nil, listOutput{}, apiError(err)
	}
	out := listOutput{Items: make([]fileEntry, 0, len(list.Items))}
	for _, item := range list.Items {
		out.Items = append(out.Items, fileEntry{Path: item.Path, Type: string(item.Type), Size: item.SizeBytes})
	}
	return nil, out, nil
}

func (s *Server) snapshot(ctx context.Context, _ *sdk.CallToolRequest, in snapshotInput) (*sdk.CallToolResult, snapshotOutput, error) {
	snap, err := s.api.CreateSnapshot(ctx, in.SandboxID)
	if err != nil {
		return nil, snapshotOutput{}, apiError(err)
	}
	return nil, snapshotOutput{ID: snap.Id, Template: snap.Template, SizeBytes: snap.SizeBytes, Scope: "/workspace only"}, nil
}

func (s *Server) fork(ctx context.Context, _ *sdk.CallToolRequest, in forkInput) (*sdk.CallToolResult, forkOutput, error) {
	labels := map[string]string{SessionLabel: s.session}
	result, err := s.api.Fork(ctx, in.SnapshotID, client.ForkRequest{Count: in.Count, TtlSeconds: in.TTLSeconds, Labels: &labels})
	if err != nil {
		return nil, forkOutput{}, apiError(err)
	}
	out := forkOutput{Sandboxes: make([]sandboxOutput, 0, len(result.Items)), Failures: make([]forkFailure, 0, len(result.Failures))}
	for _, row := range result.Items {
		s.track(row.Id, in.Keep)
		out.Sandboxes = append(out.Sandboxes, s.sandbox(row))
	}
	for _, failure := range result.Failures {
		out.Failures = append(out.Failures, forkFailure{Index: failure.Index, Code: failure.Code, Message: failure.Message})
	}
	return nil, out, nil
}

func (s *Server) destroy(ctx context.Context, _ *sdk.CallToolRequest, in destroyInput) (*sdk.CallToolResult, destroyOutput, error) {
	if err := s.api.DestroySandbox(ctx, in.SandboxID); err != nil {
		return nil, destroyOutput{}, apiError(err)
	}
	s.untrack(in.SandboxID)
	return nil, destroyOutput{Destroyed: true}, nil
}

// apiError reports Arcana's own code and message to the model and nothing else, so request
// details such as URLs or keys never reach tool output.
func apiError(err error) error {
	var failure *client.Error
	if errors.As(err, &failure) {
		return fmt.Errorf("%s: %s", failure.Code, failure.Message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errors.New("request to the Arcana API failed")
}

func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }
