package metrics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestMetrics_OutcomesGaugesAndBoundedRouteLabels(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	cfg := config.Default()
	m := New(db, cfg, func() map[string]int { return map[string]int{"base": 2} }, func() audit.Stats { return audit.Stats{Dropped: 3, Errors: 1} })
	keys := auth.New(db, auth.SystemClock{})
	key, _, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	service := orchestrator.New(db, fake.New(), cfg, orchestrator.SystemClock{})
	if _, err = service.Create(ctx, auth.Principal{ID: key.ID}, orchestrator.CreateRequest{Template: "base"}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		action  string
		details map[string]any
	}{
		{"sandbox.created", map[string]any{"source": "cold", "duration_seconds": 0.1}},
		{"sandbox.destroyed", map[string]any{"reason": "api"}},
		{"exec.ended", map[string]any{"result": "ok", "template": "base", "duration_seconds": 0.2}},
		{"egress.decision", map[string]any{"allowed": true}},
		{"reconcile.orphan", map[string]any{"kind": "volume"}},
	} {
		data, _ := json.Marshal(event.details)
		m.ObserveAudit(audit.Event{Action: event.action, Details: data})
	}
	handler := m.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/sandboxes/secret-resource-id", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/attacker-controlled-path", nil))
	response := httptest.NewRecorder()
	m.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	for _, part := range []string{
		"arcana_sandboxes_active", "arcana_sandbox_create_seconds_count", "arcana_sandboxes_destroyed_total", "arcana_exec_duration_seconds_count", "arcana_execs_total", "arcana_pool_available", "arcana_egress_requests_total", "arcana_api_requests_total", "arcana_api_request_seconds_count", "arcana_reconcile_orphans_total", "arcana_ui_sessions_active", "arcana_event_subscribers", "arcana_audit_events_dropped_total 3",
	} {
		if !strings.Contains(body, part) {
			t.Error("missing metric", part)
		}
	}
	if response.Code != 200 || strings.Contains(body, "secret-resource-id") || strings.Contains(body, "attacker-controlled-path") {
		t.Fatal("unsafe scrape", response.Code)
	}
	if !strings.Contains(body, `route="/v1/sandboxes/{id}"`) {
		t.Fatal("route was not canonicalized")
	}
}

func TestMetrics_HTTPPreservesWebSocketHijacking(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	m := New(db, config.Default(), nil, nil)
	server := httptest.NewServer(m.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("missing hijacker")
			return
		}
		conn, buffer, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		_ = buffer.Flush()
	})))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "ok" {
		t.Fatal(string(body), err)
	}
}
