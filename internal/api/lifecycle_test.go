package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

func lifecycleFixture(t *testing.T, drivers ...runtime.Driver) (http.Handler, string, string, string) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(database, auth.SystemClock{})
	secrets := make([]string, 3)
	for i := range secrets {
		_, secrets[i], err = keys.Create(context.Background(), "test", i == 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.Limits.MaxSandboxesPerKey = 2
	var driver runtime.Driver = fake.New()
	if len(drivers) > 0 {
		driver = drivers[0]
	}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	return New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service), secrets[0], secrets[1], secrets[2]
}

func TestLifecycleEndpoints_AuthOwnershipAndState(t *testing.T) {
	handler, admin, owner, other := lifecycleFixture(t)
	if got := request(handler, "POST", "/v1/sandboxes", "", `{"template":"base"}`); got.Code != 401 {
		t.Fatal(got.Code)
	}
	created := request(handler, "POST", "/v1/sandboxes", owner, `{"template":"base","env":{"SECRET":"hidden-value"},"labels":{"project":"demo"},"resources":{"cpus":999,"memory_mb":999999},"ttl_seconds":999999}`)
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body.String())
	}
	var row gen.Sandbox
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.State != "running" || *row.Resources.Cpus != 4 || *row.Resources.MemoryMb != 8192 || strings.Contains(created.Body.String(), "hidden-value") || strings.Contains(created.Body.String(), "container_id") {
		t.Fatal("unsafe or incorrect metadata")
	}
	base := "/v1/sandboxes/" + row.Id
	for _, route := range [][2]string{{"GET", base}, {"DELETE", base}, {"POST", base + "/keepalive"}} {
		if got := request(handler, route[0], route[1], "", ""); got.Code != 401 {
			t.Fatal("unauthorized", got.Code)
		}
		wrong := request(handler, route[0], route[1], other, "")
		if wrong.Code != 404 || !strings.Contains(wrong.Body.String(), "sandbox_not_found") {
			t.Fatal("wrong owner leaked", wrong.Code, wrong.Body.String())
		}
	}
	if got := request(handler, "GET", base, admin, ""); got.Code != 200 {
		t.Fatal("admin", got.Code)
	}
	if got := request(handler, "POST", base+"/keepalive", owner, ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, key := range []string{owner, admin} {
		got := request(handler, "GET", "/v1/sandboxes?state=running&label=project:demo&limit=1", key, "")
		if got.Code != 200 || !strings.Contains(got.Body.String(), row.Id) {
			t.Fatal(got.Body.String())
		}
	}
	if got := request(handler, "GET", "/v1/sandboxes", other, ""); got.Code != 200 || strings.Contains(got.Body.String(), row.Id) {
		t.Fatal("list leaked")
	}
	for range 2 {
		if got := request(handler, "DELETE", base, owner, ""); got.Code != 204 {
			t.Fatal("idempotent destroy", got.Code, got.Body.String())
		}
	}
	if got := request(handler, "POST", base+"/keepalive", owner, ""); got.Code != 409 {
		t.Fatal("ended activity", got.Code)
	}
	if got := request(handler, "GET", base, owner, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"state":"destroyed"`) {
		t.Fatal(got.Body.String())
	}
}

func TestLifecycleEndpoints_ValidationAndQuota(t *testing.T) {
	handler, _, owner, _ := lifecycleFixture(t)
	for _, body := range []string{`null`, `{}`, `{"template":"absent"}`, `{"template":"base","resources":{"cpus":0}}`, `{"template":"base","ttl_seconds":0}`, `{"template":"base","idle_timeout_seconds":-1}`, `{"template":"base","network":{"mode":"open"}}`, `{"template":"base","network":{}}`, `{"template":"base","labels":{"arcana.managed":"true"}}`, `{"template":"base","extra":true}`} {
		if got := request(handler, "POST", "/v1/sandboxes", owner, body); got.Code != 400 {
			t.Fatal("invalid accepted", body, got.Code, got.Body.String())
		}
	}
	for _, query := range []string{"limit=0", "limit=1001", "state=invalid", "label=invalid"} {
		if got := request(handler, "GET", "/v1/sandboxes?"+query, owner, ""); got.Code != 400 {
			t.Fatal("invalid list", query, got.Code)
		}
	}
	for range 2 {
		if got := request(handler, "POST", "/v1/sandboxes", owner, `{"template":"base"}`); got.Code != 201 {
			t.Fatal(got.Body.String())
		}
	}
	if got := request(handler, "POST", "/v1/sandboxes", owner, `{"template":"base"}`); got.Code != 429 || !strings.Contains(got.Body.String(), "quota_exceeded") {
		t.Fatal("quota", got.Code, got.Body.String())
	}
}
