//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	arcanamcp "github.com/brendatamaar/arcana/internal/mcp"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

func TestMCP_ToolsDriveRealSandboxesAndCleanUpOnSessionEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: database.InstallID, HelperPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Server.DestroyOnShutdown = true
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(database, auth.SystemClock{})
	_, secret, err := keys.Create(ctx, "mcp-test", false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service))
	defer httpServer.Close()
	arcana, err := client.New(httpServer.URL, secret, client.Options{HTTPClient: httpServer.Client()})
	if err != nil {
		t.Fatal(err)
	}

	srv, protocol, err := arcanamcp.New(arcana, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	serverSide, clientSide := sdk.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, protocol, serverSide) }()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "it", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if callErr != nil || result.IsError {
			t.Fatalf("%s: %v %+v", name, callErr, result)
		}
		data, _ := json.Marshal(result.StructuredContent)
		var out map[string]any
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	sandbox := call("create_sandbox", map[string]any{"template": "base"})["id"].(string)
	row, err := arcana.GetSandbox(ctx, sandbox)
	if err != nil || row.Labels[arcanamcp.SessionLabel] != srv.Session() {
		t.Fatalf("session label missing: %+v %v", row.Labels, err)
	}
	ran := call("run_command", map[string]any{"sandbox_id": sandbox, "shell": "echo hello && exit 3"})
	if ran["exit_code"].(float64) != 3 || strings.TrimSpace(ran["stdout"].(string)) != "hello" {
		t.Fatalf("%v", ran)
	}
	call("write_file", map[string]any{"sandbox_id": sandbox, "path": "/workspace/dir/note.txt", "content": "héllo from mcp"})
	if got := call("read_file", map[string]any{"sandbox_id": sandbox, "path": "/workspace/dir/note.txt"}); got["content"] != "héllo from mcp" || got["encoding"] != "utf8" {
		t.Fatalf("%v", got)
	}
	if listed := call("list_files", map[string]any{"sandbox_id": sandbox, "depth": 2}); len(listed["items"].([]any)) < 2 {
		t.Fatalf("%v", listed)
	}
	snap := call("snapshot_sandbox", map[string]any{"sandbox_id": sandbox})["id"].(string)
	forked := call("fork_sandbox", map[string]any{"snapshot_id": snap, "count": 2})
	members := forked["sandboxes"].([]any)
	if len(members) != 2 || len(forked["failures"].([]any)) != 0 {
		t.Fatalf("%v", forked)
	}
	fork := members[0].(map[string]any)["id"].(string)
	if got := call("read_file", map[string]any{"sandbox_id": fork, "path": "/workspace/dir/note.txt"}); got["content"] != "héllo from mcp" {
		t.Fatal("fork lost the snapshot content", got)
	}
	call("destroy_sandbox", map[string]any{"sandbox_id": sandbox})

	// Ending the session destroys the remaining forks but not the snapshot.
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		id := member.(map[string]any)["id"].(string)
		ended, getErr := arcana.GetSandbox(ctx, id)
		if getErr != nil || ended.State != "destroyed" {
			t.Fatalf("%s survived the session: %v %v", id, ended.State, getErr)
		}
	}
	if _, err = arcana.GetSnapshot(ctx, snap); err != nil {
		t.Fatal("snapshot must outlive the session", err)
	}
}
