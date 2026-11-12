package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPCommand_ServesToolsAndDestroysSessionSandboxesOnExit(t *testing.T) {
	var mu sync.Mutex
	var created, deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/sandboxes":
			created = append(created, string(body))
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"sbx_mcp","state":"running","template":"base","source":"cold","expires_at":"2026-11-12T00:00:00Z","runtime":"r","resources":{},"network":{"mode":"none"},"labels":{},"created_at":"2026-11-12T00:00:00Z"}`)
		case r.Method == "DELETE" && r.URL.Path == "/v1/sandboxes/sbx_mcp":
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	root := newRootCommand()
	var stderr strings.Builder
	root.SetIn(inR)
	root.SetOut(outW)
	root.SetErr(&stderr)
	root.SetArgs([]string{"--server", server.URL, "--api-key", "test-key", "mcp"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, &sdk.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 8 {
		t.Fatal(tools, err)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "create_sandbox", Arguments: map[string]any{"template": "base"}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || !strings.Contains(created[0], `"mcp_session"`) || len(deleted) != 1 {
		t.Fatalf("created=%v deleted=%v", created, deleted)
	}
	if strings.Contains(stderr.String(), "test-key") {
		t.Fatal("API key logged")
	}
}
