package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/arcana/pkg/client"
)

type fakeAPI struct {
	mu        sync.Mutex
	next      int
	created   []client.CreateSandboxRequest
	exec      []client.ExecRequest
	execOut   client.ExecResult
	file      []byte
	written   []byte
	forks     []client.ForkRequest
	forkOut   client.ForkResult
	destroyed []string
	failWith  error
	destroyEr error
}

func (f *fakeAPI) CreateSandbox(_ context.Context, in client.CreateSandboxRequest) (client.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return client.Sandbox{}, f.failWith
	}
	f.created = append(f.created, in)
	f.next++
	return client.Sandbox{Id: "sbx_" + strconv.Itoa(f.next), State: "running", Template: in.Template, Source: "cold", ExpiresAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeAPI) DestroySandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, id)
	return f.destroyEr
}

func (f *fakeAPI) Exec(_ context.Context, _ string, in client.ExecRequest) (client.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return client.ExecResult{}, f.failWith
	}
	f.exec = append(f.exec, in)
	return f.execOut, nil
}

func (f *fakeAPI) ReadFile(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.file)), nil
}

func (f *fakeAPI) WriteFile(_ context.Context, _, _ string, in io.Reader) error {
	data, _ := io.ReadAll(in)
	f.mu.Lock()
	f.written = data
	f.mu.Unlock()
	return nil
}

func (f *fakeAPI) ListFiles(_ context.Context, _, path string, depth int64) (client.FileList, error) {
	return client.FileList{Items: []client.FileEntry{{Path: path, Type: "directory", SizeBytes: depth}}}, nil
}

func (f *fakeAPI) CreateSnapshot(context.Context, string) (client.Snapshot, error) {
	return client.Snapshot{Id: "snp_x", Template: "base", SizeBytes: 7}, nil
}

func (f *fakeAPI) Fork(_ context.Context, _ string, in client.ForkRequest) (client.ForkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forks = append(f.forks, in)
	return f.forkOut, nil
}

// connect serves a new session over in-memory transports and returns a client for it.
func connect(t *testing.T, api API) (*Server, *sdk.ClientSession, func() error) {
	t.Helper()
	srv, protocol, err := New(api, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := sdk.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), protocol, serverSide) }()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "1"}, nil).Connect(context.Background(), clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	var result error
	finish := func() error {
		once.Do(func() {
			_ = session.Close()
			result = <-done
		})
		return result
	}
	t.Cleanup(func() { _ = finish() })
	return srv, session, finish
}

func call(t *testing.T, session *sdk.ClientSession, name string, args any) (*sdk.CallToolResult, map[string]any) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	if !result.IsError {
		data, _ := json.Marshal(result.StructuredContent)
		_ = json.Unmarshal(data, &out)
	}
	return result, out
}

func errorText(result *sdk.CallToolResult) string {
	var parts []string
	for _, c := range result.Content {
		if text, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, " ")
}

func TestTools_AllRequiredToolsStateTheirLimits(t *testing.T) {
	_, session, _ := connect(t, &fakeAPI{})
	list, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"create_sandbox":   {"no network by default", "keep"},
		"run_command":      {"timeout_seconds", "10 MiB", "64 KiB"},
		"read_file":        {"256 KiB", "1 MiB"},
		"write_file":       {"1 MiB"},
		"list_files":       {"1000"},
		"snapshot_sandbox": {"/workspace only"[:10], "not running processes"},
		"fork_sandbox":     {"independent", "keep"},
		"destroy_sandbox":  {"Destroy"},
	}
	got := map[string]bool{}
	for _, tool := range list.Tools {
		got[tool.Name] = true
		for _, phrase := range want[tool.Name] {
			if !strings.Contains(tool.Description, phrase) {
				t.Errorf("%s description lacks %q: %s", tool.Name, phrase, tool.Description)
			}
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	if len(list.Tools) != len(want) {
		t.Errorf("unexpected tools: %d", len(list.Tools))
	}
}

func TestCreateSandbox_LabelsSessionAndMapsNetwork(t *testing.T) {
	api := &fakeAPI{}
	srv, session, _ := connect(t, api)
	_, out := call(t, session, "create_sandbox", map[string]any{"template": "python", "presets": []string{"python"}, "ttl_seconds": 60})
	if out["id"] == "" || out["mcp_session"] != srv.Session() {
		t.Fatalf("%v", out)
	}
	req := api.created[0]
	if (*req.Labels)[SessionLabel] != srv.Session() || req.Network == nil || req.Network.Mode != "allowlist" || (*req.Network.Presets)[0] != "python" || *req.TtlSeconds != 60 {
		t.Fatalf("%+v", req)
	}
	call(t, session, "create_sandbox", map[string]any{"template": "base"})
	if api.created[1].Network != nil {
		t.Fatal("network must stay at the server default when unspecified")
	}
	result, _ := call(t, session, "create_sandbox", map[string]any{})
	if !result.IsError {
		t.Fatal("missing template accepted")
	}
}

func TestRunCommand_ExclusiveArgvOrShellAndBoundedReply(t *testing.T) {
	api := &fakeAPI{execOut: client.ExecResult{ExitCode: 3, Stdout: strings.Repeat("é", maxReturnedText/2+10), Stderr: "e", DurationMs: 5, ExecId: "exe_1", Truncated: true}}
	_, session, _ := connect(t, api)
	for _, args := range []map[string]any{{"sandbox_id": "sbx_a"}, {"sandbox_id": "sbx_a", "cmd": []string{"ls"}, "shell": "ls"}} {
		if result, _ := call(t, session, "run_command", args); !result.IsError || !strings.Contains(errorText(result), "exactly one") {
			t.Fatalf("%v: %v", args, errorText(result))
		}
	}
	if len(api.exec) != 0 {
		t.Fatal("invalid request reached Arcana")
	}
	_, out := call(t, session, "run_command", map[string]any{"sandbox_id": "sbx_a", "cmd": []string{"echo", "; not a shell"}, "timeout_seconds": 9})
	if api.exec[0].Shell != nil || api.exec[0].Cmd[1] != "; not a shell" || *api.exec[0].TimeoutSeconds != 9 {
		t.Fatalf("argv changed: %+v", api.exec[0])
	}
	stdout := out["stdout"].(string)
	if out["exit_code"].(float64) != 3 || len(stdout) > maxReturnedText || len(stdout) < maxReturnedText-4 || out["reply_truncated"] != true || out["truncated"] != true {
		t.Fatalf("bad result: exit=%v len=%d %v", out["exit_code"], len(stdout), out["reply_truncated"])
	}
	for i := 0; i < len(stdout); {
		r := []rune(stdout[i:])[0]
		if r == '�' {
			t.Fatal("reply was cut inside a rune")
		}
		i += len(string(r))
	}
	call(t, session, "run_command", map[string]any{"sandbox_id": "sbx_a", "shell": "echo hi | cat"})
	if api.exec[1].Shell == nil || *api.exec[1].Shell != "echo hi | cat" {
		t.Fatal("shell string lost")
	}
}

func TestReadFile_TextBinaryAndTruncation(t *testing.T) {
	api := &fakeAPI{file: []byte("héllo")}
	_, session, _ := connect(t, api)
	_, out := call(t, session, "read_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a"})
	if out["content"] != "héllo" || out["encoding"] != "utf8" || out["truncated"] != false {
		t.Fatal(out)
	}
	// Cutting between the two bytes of é must not turn text into base64.
	_, out = call(t, session, "read_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a", "max_bytes": 2})
	if out["content"] != "h" || out["encoding"] != "utf8" || out["truncated"] != true {
		t.Fatal(out)
	}
	api.file = []byte{0, 255, 1}
	_, out = call(t, session, "read_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a"})
	if out["encoding"] != "base64" || out["content"] != base64.StdEncoding.EncodeToString(api.file) {
		t.Fatal(out)
	}
	for _, bad := range []int64{-1, MaxReadBytes + 1} {
		if result, _ := call(t, session, "read_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a", "max_bytes": bad}); !result.IsError {
			t.Fatalf("max_bytes %d accepted", bad)
		}
	}
}

func TestWriteFile_EncodingsAndBounds(t *testing.T) {
	api := &fakeAPI{}
	_, session, _ := connect(t, api)
	call(t, session, "write_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a", "content": "text"})
	if string(api.written) != "text" {
		t.Fatal(api.written)
	}
	call(t, session, "write_file", map[string]any{"sandbox_id": "sbx_a", "path": "/workspace/a", "content": base64.StdEncoding.EncodeToString([]byte{0, 255}), "encoding": "base64"})
	if !bytes.Equal(api.written, []byte{0, 255}) {
		t.Fatal(api.written)
	}
	for _, args := range []map[string]any{
		{"content": "!!!", "encoding": "base64"},
		{"content": "x", "encoding": "latin1"},
		{"content": strings.Repeat("x", MaxWriteBytes+1)},
	} {
		args["sandbox_id"], args["path"] = "sbx_a", "/workspace/a"
		if result, _ := call(t, session, "write_file", args); !result.IsError {
			t.Fatal("accepted", args["encoding"])
		}
	}
}

func TestListFiles_Defaults(t *testing.T) {
	_, session, _ := connect(t, &fakeAPI{})
	_, out := call(t, session, "list_files", map[string]any{"sandbox_id": "sbx_a"})
	item := out["items"].([]any)[0].(map[string]any)
	if item["path"] != "/workspace" || item["size_bytes"].(float64) != 1 {
		t.Fatal(item)
	}
}

func TestSessionCleanup_DestroysOnlyUnkeptSandboxes(t *testing.T) {
	api := &fakeAPI{forkOut: client.ForkResult{
		Items:    []client.Sandbox{{Id: "sbx_f1", State: "running"}, {Id: "sbx_f2", State: "running"}},
		Failures: []client.ForkFailure{{Index: 2, Code: "quota_exceeded", Message: "sandbox quota exceeded"}},
	}}
	srv, session, finish := connect(t, api)
	_, a := call(t, session, "create_sandbox", map[string]any{"template": "base"})
	_, kept := call(t, session, "create_sandbox", map[string]any{"template": "base", "keep": true})
	_, gone := call(t, session, "create_sandbox", map[string]any{"template": "base"})
	_, snap := call(t, session, "snapshot_sandbox", map[string]any{"sandbox_id": a["id"]})
	if snap["id"] != "snp_x" || snap["scope"] != "/workspace only" {
		t.Fatal(snap)
	}
	_, forked := call(t, session, "fork_sandbox", map[string]any{"snapshot_id": "snp_x", "count": 3})
	if len(forked["failures"].([]any)) != 1 || len(forked["sandboxes"].([]any)) != 2 {
		t.Fatal(forked)
	}
	if got := *api.forks[0].Labels; got[SessionLabel] != srv.Session() || api.forks[0].Count != 3 {
		t.Fatalf("%+v", api.forks[0])
	}
	if _, out := call(t, session, "destroy_sandbox", map[string]any{"sandbox_id": gone["id"]}); out["destroyed"] != true {
		t.Fatal(out)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	destroyed := map[string]int{}
	for _, id := range api.destroyed {
		destroyed[id]++
	}
	// kept survives; the explicitly destroyed one is not destroyed a second time.
	if destroyed[kept["id"].(string)] != 0 || destroyed[gone["id"].(string)] != 1 || destroyed[a["id"].(string)] != 1 || destroyed["sbx_f1"] != 1 || destroyed["sbx_f2"] != 1 {
		t.Fatalf("destroyed: %v (kept=%v gone=%v)", destroyed, kept["id"], gone["id"])
	}
}

func TestSessionCleanup_ReportsFailureAndKeepsForkedWhenAsked(t *testing.T) {
	api := &fakeAPI{destroyEr: errors.New("daemon down"), forkOut: client.ForkResult{Items: []client.Sandbox{{Id: "sbx_f1"}}}}
	_, session, finish := connect(t, api)
	call(t, session, "create_sandbox", map[string]any{"template": "base"})
	call(t, session, "fork_sandbox", map[string]any{"snapshot_id": "snp_x", "count": 1, "keep": true})
	if err := finish(); err == nil || !strings.Contains(err.Error(), "daemon down") {
		t.Fatalf("cleanup failure hidden: %v", err)
	}
	for _, id := range api.destroyed {
		if id == "sbx_f1" {
			t.Fatal("kept fork destroyed")
		}
	}
}

func TestErrors_ExposeArcanaCodesWithoutInternals(t *testing.T) {
	api := &fakeAPI{failWith: &client.Error{StatusCode: 429, Code: "quota_exceeded", Message: "sandbox quota exceeded", RequestID: "req_1"}}
	_, session, _ := connect(t, api)
	result, _ := call(t, session, "create_sandbox", map[string]any{"template": "base"})
	if !result.IsError || !strings.Contains(errorText(result), "quota_exceeded: sandbox quota exceeded") {
		t.Fatal(errorText(result))
	}
	api.failWith = errors.New(`Post "http://127.0.0.1:7070/v1/sandboxes": Authorization arc_live_SECRET`)
	result, _ = call(t, session, "run_command", map[string]any{"sandbox_id": "sbx_a", "cmd": []string{"ls"}})
	if !result.IsError || strings.Contains(errorText(result), "arc_live_SECRET") || strings.Contains(errorText(result), "127.0.0.1") {
		t.Fatalf("internal detail leaked: %s", errorText(result))
	}
}

func TestClip_NeverSplitsRunes(t *testing.T) {
	in := strings.Repeat("a", maxReturnedText-1) + "é"
	out, cut := clip(in)
	if !cut || len(out) != maxReturnedText-1 {
		t.Fatalf("%d %t", len(out), cut)
	}
	if short, cut := clip("ok"); short != "ok" || cut {
		t.Fatal("short text changed")
	}
}
