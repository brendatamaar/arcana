package api

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestAudit_AdminOnlyFiltersAndLimits(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	}()
	recorder, err := audit.New(audit.SQLBackend{Store: database}, audit.SystemClock{}, 30, 32, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.New(database, auth.SystemClock{}, recorder)
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
	recorder.Record("file.written", "owner", "sandbox", map[string]any{"path": "/workspace/probe", "size_bytes": 10})
	if err = recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	handler := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, orchestrator.New(database, fake.New(), cfg, orchestrator.SystemClock{}), Options{Audit: recorder})
	for _, test := range []struct {
		key    string
		status int
	}{{"", 401}, {owner, 403}, {other, 403}, {admin, 200}} {
		response := request(handler, "GET", "/v1/audit", test.key, "")
		if response.Code != test.status {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	response := request(handler, "GET", "/v1/audit?action=file.written&sandbox_id=sandbox&key_id=owner&limit=1", admin, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "/workspace/probe") || strings.Contains(response.Body.String(), admin) || strings.Contains(response.Body.String(), owner) {
		t.Fatal(response.Body.String())
	}
	for _, query := range []string{"limit=0", "limit=1001", "cursor=invalid!", "since=invalid", "since=2026-11-08T00:00:00Z&until=2026-11-07T00:00:00Z"} {
		if response := request(handler, "GET", "/v1/audit?"+query, admin, ""); response.Code != 400 {
			t.Fatal(query, response.Code)
		}
	}
}
