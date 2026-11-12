package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

type helperDriver struct {
	*fake.Driver
	mu sync.Mutex
}

type (
	inputSession struct {
		runtime.ExecSession
		closed chan struct{}
		once   sync.Once
	}
	inputWriter struct {
		io.WriteCloser
		session *inputSession
	}
)

func (w inputWriter) Close() error {
	err := w.WriteCloser.Close()
	w.session.once.Do(func() { close(w.session.closed) })
	return err
}
func (s *inputSession) Stdin() io.WriteCloser { return inputWriter{s.ExecSession.Stdin(), s} }
func (s *inputSession) Wait(ctx context.Context) (runtime.ExitInfo, error) {
	select {
	case <-s.closed:
		return s.ExecSession.Wait(ctx)
	case <-ctx.Done():
		return runtime.ExitInfo{}, ctx.Err()
	}
}

func (d *helperDriver) Exec(ctx context.Context, id string, r runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := fake.Script{}
	waitInput := false
	if r.Cmd[1] == "status" {
		script.Stdout, _ = json.Marshal(protocol.Status{ID: r.Cmd[3], CleanupComplete: true})
	} else if r.Cmd[1] == "run" {
		script.Stdout = []byte("output")
		script.Stderr = []byte("diagnostic")
		for i, arg := range r.Cmd {
			if arg == "--" && i+2 < len(r.Cmd) {
				switch r.Cmd[i+2] {
				case "write", "-xzf":
					waitInput = true
				case "read":
					script.Stdout = []byte("{\"size_bytes\":3}\n\x00\xffx")
				case "ls":
					script.Stdout = []byte(`{"items":[]}`)
				case "-czf":
					script.Stdout = []byte("opaque-gzip")
				}
				break
			}
		}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	session, err := d.Driver.Exec(ctx, id, r)
	if err != nil {
		return nil, err
	}
	if !waitInput {
		return session, nil
	}
	return &inputSession{ExecSession: session, closed: make(chan struct{})}, nil
}

func fixture(t *testing.T) (*client.Client, *client.Client, *client.Client) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(database, auth.SystemClock{})
	_, admin, err := keys.Create(ctx, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keys.Create(ctx, "other", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	driver := &helperDriver{Driver: fake.New()}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	service.WithEvents(events.NewBus(orchestrator.SystemClock{}, cfg.UI.EventBuffer, 10))
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service, api.Options{Version: "v-test", HeartbeatInterval: 50 * time.Millisecond}))
	t.Cleanup(server.Close)
	makeClient := func(key string) *client.Client {
		c, err := client.New(server.URL, key, client.Options{HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return makeClient(admin), makeClient(owner), makeClient(other)
}

func TestClient_RealAPILifecycleKeysExecAndFiles(t *testing.T) {
	admin, c, other := fixture(t)
	ctx := context.Background()
	if health, err := c.Health(ctx); err != nil || health.Status != "ok" {
		t.Fatal(health, err)
	}
	row, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetSandbox(ctx, row.Id); err != nil || got.Id != row.Id {
		t.Fatal(got, err)
	}
	page, err := c.ListSandboxes(ctx, client.ListOptions{Limit: 1, State: "running"})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	if _, err = c.Keepalive(ctx, row.Id); err != nil {
		t.Fatal(err)
	}
	_, err = other.GetSandbox(ctx, row.Id)
	var failure *client.Error
	if !errors.As(err, &failure) || failure.StatusCode != 404 || failure.Code != "sandbox_not_found" || failure.RequestID == "" {
		t.Fatal("ownership error", err)
	}
	out, err := c.Exec(ctx, row.Id, client.ExecRequest{Cmd: []string{"echo", "hello"}})
	if err != nil || out.Stdout != "output" || out.Stderr != "diagnostic" || out.ExitCode != 0 {
		t.Fatal(out, err)
	}
	name := "/workspace/name ? & + # literal"
	if err = c.WriteFile(ctx, row.Id, name, strings.NewReader("input")); err != nil {
		t.Fatal(err)
	}
	body, err := c.ReadFile(ctx, row.Id, name)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "\x00\xffx" {
		t.Fatal(data, err)
	}
	if items, err := c.ListFiles(ctx, row.Id, "/workspace", 1); err != nil || len(items.Items) != 0 {
		t.Fatal(items, err)
	}
	if err = c.UploadArchive(ctx, row.Id, "/workspace", strings.NewReader("opaque")); err != nil {
		t.Fatal(err)
	}
	body, err = c.DownloadArchive(ctx, row.Id, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(data) != "opaque-gzip" {
		t.Fatal(string(data), err)
	}
	if err = c.DeleteFile(ctx, row.Id, name, true); err != nil {
		t.Fatal(err)
	}
	key, err := admin.CreateKey(ctx, client.CreateKeyRequest{Name: "new"})
	if err != nil || key.Secret == "" {
		t.Fatal(err)
	}
	if list, err := admin.ListKeys(ctx, client.ListOptions{Limit: 100}); err != nil || len(list.Items) != 4 {
		t.Fatal(list, err)
	}
	if err = admin.RevokeKey(ctx, key.Key.Id); err != nil {
		t.Fatal(err)
	}
	if err = c.DestroySandbox(ctx, row.Id); err != nil {
		t.Fatal(err)
	}
	if err = c.DestroySandbox(ctx, row.Id); err != nil {
		t.Fatal(err)
	}
}

func TestExecStream_RealAPIReadersAndExit(t *testing.T) {
	_, c, other := fixture(t)
	ctx := context.Background()
	row, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := c.ExecStream(ctx, row.Id, client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"echo", "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	stderr := make(chan string, 1)
	go func() { data, _ := io.ReadAll(stream.Stderr); stderr <- string(data) }()
	data, err := io.ReadAll(stream.Stdout)
	if err != nil || string(data) != "output" {
		t.Fatal(string(data), err)
	}
	if got := <-stderr; got != "diagnostic" {
		t.Fatal(got)
	}
	result, err := stream.Wait()
	if err != nil || result.ExitCode != 0 || stream.ID == "" {
		t.Fatal(result, err)
	}
	_, err = other.ExecStream(ctx, row.Id, client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"true"}}})
	var failure *client.Error
	if !errors.As(err, &failure) || failure.StatusCode != 404 {
		t.Fatal(err)
	}
}

func TestClient_BoundsCancellationAndRedirects(t *testing.T) {
	for _, serverURL := range []string{"ftp://host", "http://user:pass@host", "http://host?x=1", "relative"} {
		if _, err := client.New(serverURL, "", client.Options{}); err == nil {
			t.Fatal(serverURL)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = io.WriteString(w, strings.Repeat("x", 128))
		case "/v1/sandboxes/x/files":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "0123456789")
		default:
			w.Header().Set("Location", "http://example.invalid")
			w.WriteHeader(307)
		}
	}))
	defer server.Close()
	c, err := client.New(server.URL, "secret", client.Options{MaxResponseBytes: 8, MaxTransferBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Health(context.Background()); !errors.Is(err, client.ErrLimit) {
		t.Fatal(err)
	}
	if _, err = c.ReadFile(context.Background(), "x", "/workspace/a"); !errors.Is(err, client.ErrLimit) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Health(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = c.GetSandbox(context.Background(), "../keys"); err == nil {
		t.Fatal("path injection accepted")
	}
	_, err = c.GetSandbox(context.Background(), "x")
	var failure *client.Error
	if !errors.As(err, &failure) || failure.StatusCode != 307 {
		t.Fatal("redirect followed", err)
	}
}

func TestClient_ChunkedTransferAndUploadCaps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "12345")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	}))
	defer server.Close()
	c, err := client.New(server.URL, "", client.Options{MaxTransferBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	body, err := c.ReadFile(context.Background(), "x", "/workspace/x")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if !errors.Is(err, client.ErrLimit) || string(data) != "1234" {
		t.Fatal(string(data), err)
	}
	if err = c.WriteFile(context.Background(), "x", "/workspace/x", strings.NewReader("12345")); !errors.Is(err, client.ErrLimit) {
		t.Fatal("oversized upload accepted", err)
	}
}

// ExampleClient_CreateSandbox demonstrates context ownership and argv exec.
func ExampleClient_CreateSandbox() {
	c, err := client.New("http://127.0.0.1:7070", "operator-api-key", client.Options{})
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sandbox, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		return
	}
	defer func() { _ = c.DestroySandbox(ctx, sandbox.Id) }()
	_, _ = c.Exec(ctx, sandbox.Id, client.ExecRequest{Cmd: []string{"python3", "--version"}})
}

func TestListTemplates_ConfiguredDefaults(t *testing.T) {
	c, _, _ := fixture(t)
	list, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "base" || list.Items[0].Workdir != "/workspace" {
		t.Fatalf("%+v", list)
	}
}

func TestListAudit_FilterEncoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audit" || r.URL.Query().Get("action") != "file.written" || r.URL.Query().Get("cursor") != "cursor+value" || r.URL.Query().Get("limit") != "5" {
			t.Error(r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer server.Close()
	c, err := client.New(server.URL, "key", client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListAudit(context.Background(), client.AuditOptions{Action: "file.written", Cursor: "cursor+value", Limit: 5}); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitedError_ExposesRetryWithoutAutomaticRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "2")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limited","message":"creation rate exceeded","request_id":"request"}}`)
	}))
	defer server.Close()
	c, err := client.New(server.URL, "key", client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateSandbox(context.Background(), client.CreateSandboxRequest{Template: "base"})
	var failure *client.Error
	if !errors.As(err, &failure) || failure.Code != "rate_limited" || failure.RetryAfterSeconds != 2 || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestClient_SnapshotForkAndRestoreAgainstRealAPI(t *testing.T) {
	_, owner, other := fixture(t)
	ctx := context.Background()
	source, err := owner.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := owner.CreateSnapshot(ctx, source.Id)
	if err != nil || snap.SourceSandboxId != source.Id || snap.SizeBytes != int64(len("opaque-gzip")) {
		t.Fatalf("%+v %v", snap, err)
	}
	if got, err := owner.GetSnapshot(ctx, snap.Id); err != nil || got.Sha256 != snap.Sha256 {
		t.Fatal(got, err)
	}
	var failure *client.Error
	if _, err = other.GetSnapshot(ctx, snap.Id); !errors.As(err, &failure) || failure.Code != "snapshot_not_found" {
		t.Fatal("foreign snapshot visible", err)
	}
	if _, err = owner.GetSnapshot(ctx, "../x"); err == nil {
		t.Fatal("unsafe snapshot ID accepted")
	}
	page, err := owner.ListSnapshots(ctx, client.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	from := snap.Id
	restored, err := owner.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base", FromSnapshot: &from})
	if err != nil || restored.Source != "snapshot" {
		t.Fatal(restored, err)
	}
	forked, err := owner.Fork(ctx, snap.Id, client.ForkRequest{Count: 2})
	if err != nil || len(forked.Items) != 2 || len(forked.Failures) != 0 {
		t.Fatalf("%+v %v", forked, err)
	}
	if _, err = owner.Fork(ctx, snap.Id, client.ForkRequest{Count: 99}); !errors.As(err, &failure) || failure.Code != "invalid_request" {
		t.Fatal("oversized fork accepted", err)
	}
	if err = owner.DeleteSnapshot(ctx, snap.Id); err != nil {
		t.Fatal(err)
	}
	if _, err = owner.GetSnapshot(ctx, snap.Id); !errors.As(err, &failure) || failure.StatusCode != 404 {
		t.Fatal(err)
	}
}

func nextEvent(t *testing.T, stream *client.EventStream, want string) client.Event {
	t.Helper()
	for i := 0; i < 20; i++ {
		event, err := stream.Next()
		if err != nil {
			t.Fatalf("waiting for %s: %v", want, err)
		}
		if event.Type == want {
			return event
		}
	}
	t.Fatalf("never saw %s", want)
	return client.Event{}
}

func TestClient_EventsResumeResetAndOutputTail(t *testing.T) {
	_, owner, other := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stream, err := owner.Events(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	row, err := owner.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	created := nextEvent(t, stream, "sandbox.created")
	if created.SandboxID != row.Id || created.ID == "" || created.At.IsZero() {
		t.Fatalf("%+v", created)
	}
	result, err := owner.Exec(ctx, row.Id, client.ExecRequest{Cmd: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	ended := nextEvent(t, stream, "exec.ended")
	if !strings.Contains(string(ended.Data), result.ExecId) || strings.Contains(string(ended.Data), "output") && strings.Contains(string(ended.Data), "diagnostic") {
		t.Fatalf("%s", ended.Data)
	}

	// Resuming from the creation event replays what followed it, with no gap.
	resumed, err := owner.Events(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	if got := nextEvent(t, resumed, "exec.started"); got.SandboxID != row.Id {
		t.Fatalf("%+v", got)
	}
	reset, err := owner.Events(ctx, "999999")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reset.Close() }()
	if _, err = reset.Next(); !errors.Is(err, client.ErrReset) {
		t.Fatalf("a stale Last-Event-ID must reset: %v", err)
	}
	if _, err = owner.Events(ctx, "not-a-number"); err == nil {
		t.Fatal("bad Last-Event-ID accepted")
	}
	// Another key neither sees these events nor the output.
	foreign, err := other.Events(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreign.Close() }()
	if _, err = other.ExecOutput(ctx, row.Id, result.ExecId); err == nil {
		t.Fatal("foreign key tailed another's exec")
	}

	tail, err := owner.ExecOutput(ctx, row.Id, result.ExecId)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tail.Close() }()
	var out, errText string
	var end *client.OutputEnd
	for end == nil {
		item, nextErr := tail.Next()
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		switch item.Kind {
		case "output":
			if item.Stream == "stderr" {
				errText += string(item.Data)
			} else {
				out += string(item.Data)
			}
		case "end":
			end = item.End
		}
	}
	if out != "output" || errText != "diagnostic" || end.ExitCode == nil || *end.ExitCode != 0 || end.Expired {
		t.Fatalf("%q %q %+v", out, errText, end)
	}
	if _, err = tail.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("stream must end after the end item: %v", err)
	}
	if _, err = owner.ExecOutput(ctx, "../x", "exe_x"); err == nil {
		t.Fatal("unsafe ID accepted")
	}

	execs, err := owner.ListExecs(ctx, row.Id, client.ListOptions{Limit: 10})
	if err != nil || len(execs.Items) != 1 || execs.Items[0].Id != result.ExecId || execs.Items[0].Cmd == nil {
		t.Fatalf("%+v %v", execs, err)
	}
	overview, err := owner.GetOverview(ctx)
	if err != nil || overview.Version != "v-test" || len(overview.SandboxesByTemplate) != 1 {
		t.Fatalf("%+v %v", overview, err)
	}
}
