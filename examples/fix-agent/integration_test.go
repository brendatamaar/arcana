//go:build linux && integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

// liveArcana starts the real orchestrator under runsc behind a real HTTP API.
func liveArcana(t *testing.T) *client.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: database.InstallID, HelperPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Server.DestroyOnShutdown = true
	cfg.Templates = nil
	for _, name := range []string{"python", "node", "go"} {
		cfg.Templates = append(cfg.Templates, config.Template{Name: name, Image: "arcana-" + name + ":local"})
	}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 90*time.Second)
		defer done()
		if err := service.Shutdown(cleanup); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(database, auth.SystemClock{})
	_, secret, err := keys.Create(ctx, "fix-agent-test", false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service))
	t.Cleanup(server.Close)
	c, err := client.New(server.URL, secret, client.Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVerifyFixtures_EveryFixtureIsARealFixableBug(t *testing.T) {
	arcana := liveArcana(t)
	fixtures, err := LoadFixtures("../fixtures")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, fx := range fixtures {
		result := Deps{Arcana: arcana, Log: io.Discard}.VerifyFixture(ctx, fx)
		if !result.OK() {
			t.Errorf("%s: %+v", fx.Name, result)
		}
	}
}

// scriptedModel is an OpenAI-compatible endpoint that "knows" the answer: it lists the files, runs
// the visible tests, then writes the reference solution. It validates the whole stack (agent, HTTP
// client, Arcana API, sandboxes, hidden-test judge, snapshot and fork). It says nothing about how
// well a real model performs.
func scriptedModel(t *testing.T, fx Fixture) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	steps := map[string]int{} // keyed by the first user message plus attempt temperature
	solution := map[string]string{}
	root := filepath.Join(fx.Dir, "solution")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		rel, _ := filepath.Rel(root, path)
		solution["/workspace/"+strings.TrimSuffix(filepath.ToSlash(rel), stripSuffix)] = string(data)
		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages    []Message `json:"messages"`
			Temperature float64   `json:"temperature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		mu.Lock()
		key := strings.Repeat("x", int(request.Temperature*100))
		steps[key]++
		step := steps[key]
		mu.Unlock()
		message := map[string]any{"role": "assistant"}
		switch step {
		case 1:
			message["tool_calls"] = []ToolCall{call("1", "list_files", map[string]any{"path": "/workspace"})}
		case 2:
			message["tool_calls"] = []ToolCall{call("2", "run_command", map[string]any{"command": fx.VisibleTest})}
		case 3:
			var calls []ToolCall
			for path, content := range solution {
				calls = append(calls, call("w"+path, "write_file", map[string]any{"path": path, "content": content}))
			}
			message["tool_calls"] = calls
		default:
			message["content"] = "fixed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message}}, "usage": map[string]any{"total_tokens": 100}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunTask_ScriptedModelFixesFixturesEndToEnd(t *testing.T) {
	arcana := liveArcana(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	for _, name := range []string{"py-off-by-one", "js-numeric-sort", "go-nil-map"} {
		fx, err := loadFixture("../fixtures/" + name)
		if err != nil {
			t.Fatal(err)
		}
		model := scriptedModel(t, fx)
		llm, err := NewOpenAIChat(model.URL, "scripted", "")
		if err != nil {
			t.Fatal(err)
		}
		result, err := RunTask(ctx, Deps{Arcana: arcana, LLM: llm, Log: io.Discard}, fx, TaskOptions{Parallel: 1, Budget: Budget{MaxSteps: 6, MaxTokens: 10000}, Temperature: 0.2})
		if err != nil || !result.Solved || result.Steps != 3 {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
	}
	// Parallel mode: snapshot after setup, fork two attempts, keep the first to pass.
	fx, err := loadFixture("../fixtures/py-mutable-default")
	if err != nil {
		t.Fatal(err)
	}
	llm, err := NewOpenAIChat(scriptedModel(t, fx).URL, "scripted", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunTask(ctx, Deps{Arcana: arcana, LLM: llm, Log: io.Discard}, fx, TaskOptions{Parallel: 2, Budget: Budget{MaxSteps: 6, MaxTokens: 10000}, Temperature: 0.2})
	if err != nil || !result.Solved || len(result.Attempts) != 2 {
		t.Fatalf("parallel: %+v %v", result, err)
	}
	left, err := arcana.ListSandboxes(ctx, client.ListOptions{State: "running"})
	if err != nil || len(left.Items) != 0 {
		t.Fatalf("sandboxes left running: %v %v", left.Items, err)
	}
	if snaps, err := arcana.ListSnapshots(ctx, client.ListOptions{}); err != nil || len(snaps.Items) != 0 {
		t.Fatalf("snapshots left: %v %v", snaps.Items, err)
	}
}
