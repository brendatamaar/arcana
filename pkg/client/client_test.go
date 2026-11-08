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

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
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
	driver := &helperDriver{Driver: fake.New()}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service))
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
