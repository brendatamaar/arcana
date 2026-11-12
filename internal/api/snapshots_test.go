package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

func snapshotAPI(t *testing.T, perKey int, rate int) (http.Handler, string, string, string) {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keys := auth.New(database, auth.SystemClock{})
	secrets := make([]string, 3)
	for i := range secrets {
		_, secrets[i], err = keys.Create(context.Background(), "test", i == 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Limits.MaxSandboxesPerKey = perKey
	cfg.Snapshots.MaxFork = 3
	service := orchestrator.New(database, &filesDriver{Driver: fake.New()}, cfg, orchestrator.SystemClock{})
	handler := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service, Options{MaxFork: cfg.Snapshots.MaxFork, CreatePerMinute: rate})
	return handler, secrets[0], secrets[1], secrets[2]
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	return string(decodeBody[gen.Error](t, w).Error.Code)
}

func TestSnapshotEndpoints_LifecycleOwnershipAndErrors(t *testing.T) {
	h, admin, owner, other := snapshotAPI(t, 5, 1000)
	created := request(h, "POST", "/v1/sandboxes", owner, `{"template":"base","env":{"TOKEN":"never-stored"}}`)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	source := decodeBody[gen.Sandbox](t, created)
	for _, route := range [][2]string{{"POST", "/v1/sandboxes/" + source.Id + "/snapshots"}, {"GET", "/v1/snapshots"}, {"GET", "/v1/snapshots/snp_x"}, {"DELETE", "/v1/snapshots/snp_x"}, {"POST", "/v1/snapshots/snp_x/fork"}} {
		if got := request(h, route[0], route[1], "", `{"count":1}`); got.Code != 401 {
			t.Fatalf("%v unauthenticated: %d", route, got.Code)
		}
	}
	if got := request(h, "POST", "/v1/sandboxes/"+source.Id+"/snapshots", other, ""); got.Code != 404 || errorCode(t, got) != "sandbox_not_found" {
		t.Fatalf("foreign sandbox: %d %s", got.Code, got.Body.String())
	}
	made := request(h, "POST", "/v1/sandboxes/"+source.Id+"/snapshots", owner, "")
	if made.Code != 201 {
		t.Fatal(made.Code, made.Body.String())
	}
	if strings.Contains(made.Body.String(), "never-stored") || strings.Contains(made.Body.String(), `"path"`) {
		t.Fatal("snapshot response leaked env values or host paths")
	}
	snap := decodeBody[gen.Snapshot](t, made)
	if snap.SourceSandboxId != source.Id || snap.Template != "base" || snap.SizeBytes != int64(len("opaque-gzip")) || len(snap.Sha256) != 64 || !contains(snap.EnvKeys, "TOKEN") {
		t.Fatalf("%+v", snap)
	}
	for _, key := range []string{other} {
		for _, call := range [][2]string{{"GET", "/v1/snapshots/" + snap.Id}, {"DELETE", "/v1/snapshots/" + snap.Id}, {"POST", "/v1/snapshots/" + snap.Id + "/fork"}} {
			got := request(h, call[0], call[1], key, `{"count":1}`)
			if got.Code != 404 || errorCode(t, got) != "snapshot_not_found" {
				t.Fatalf("%v by other key: %d %s", call, got.Code, got.Body.String())
			}
		}
		if page := decodeBody[gen.SnapshotList](t, request(h, "GET", "/v1/snapshots", key, "")); len(page.Items) != 0 {
			t.Fatal("listing leaked a foreign snapshot")
		}
	}
	if got := request(h, "GET", "/v1/snapshots/"+snap.Id, owner, ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	if page := decodeBody[gen.SnapshotList](t, request(h, "GET", "/v1/snapshots", admin, "")); len(page.Items) != 1 {
		t.Fatal("admin must see every snapshot")
	}
	if got := request(h, "GET", "/v1/snapshots?limit=0", owner, ""); got.Code != 400 {
		t.Fatal("bad limit", got.Code)
	}
	if got := request(h, "DELETE", "/v1/snapshots/"+snap.Id, owner, ""); got.Code != 204 {
		t.Fatal(got.Code)
	}
	if got := request(h, "GET", "/v1/snapshots/"+snap.Id, owner, ""); got.Code != 404 {
		t.Fatal("deleted snapshot still readable")
	}
	if got := request(h, "DELETE", "/v1/sandboxes/"+source.Id, owner, ""); got.Code != 204 {
		t.Fatal(got.Code)
	}
	if got := request(h, "POST", "/v1/sandboxes/"+source.Id+"/snapshots", owner, ""); got.Code != 409 {
		t.Fatalf("snapshot of an ended sandbox: %d", got.Code)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func TestCreateFromSnapshotAndFork_ValidationQuotaAndPartialFailure(t *testing.T) {
	h, _, owner, other := snapshotAPI(t, 3, 1000)
	source := decodeBody[gen.Sandbox](t, request(h, "POST", "/v1/sandboxes", owner, `{"template":"base"}`))
	snap := decodeBody[gen.Snapshot](t, request(h, "POST", "/v1/sandboxes/"+source.Id+"/snapshots", owner, ""))
	body := func(extra string) string { return `{"template":"base","from_snapshot":"` + snap.Id + `"` + extra + `}` }
	restored := request(h, "POST", "/v1/sandboxes", owner, body(""))
	if restored.Code != 201 || decodeBody[gen.Sandbox](t, restored).Source != gen.SandboxSourceSnapshot {
		t.Fatal(restored.Code, restored.Body.String())
	}
	if got := request(h, "POST", "/v1/sandboxes", owner, strings.Replace(body(""), `"base"`, `"nope"`, 1)); got.Code != 400 {
		t.Fatalf("template mismatch: %d %s", got.Code, got.Body.String())
	}
	if got := request(h, "POST", "/v1/sandboxes", other, body("")); got.Code != 404 || errorCode(t, got) != "snapshot_not_found" {
		t.Fatalf("foreign snapshot: %d", got.Code)
	}
	if got := request(h, "POST", "/v1/sandboxes", owner, `{"template":"base","from_snapshot":"snp_aaaaaaaaaaaaaaaaaaaa"}`); got.Code != 404 {
		t.Fatalf("unknown snapshot: %d", got.Code)
	}
	// The key now holds 2 of 3 slots, so a fork of 3 creates one and reports two quota failures.
	forked := request(h, "POST", "/v1/snapshots/"+snap.Id+"/fork", owner, `{"count":3,"labels":{"attempt":"fork"}}`)
	if forked.Code != 201 {
		t.Fatal(forked.Code, forked.Body.String())
	}
	result := decodeBody[gen.ForkResult](t, forked)
	if len(result.Items) != 1 || len(result.Failures) != 2 || result.Failures[0].Code != "quota_exceeded" || result.Items[0].Labels["attempt"] != "fork" {
		t.Fatalf("%+v", result)
	}
	// With no capacity at all the response is the first failure's error, not an empty 201.
	if got := request(h, "POST", "/v1/snapshots/"+snap.Id+"/fork", owner, `{"count":2}`); got.Code != 429 || errorCode(t, got) != "quota_exceeded" {
		t.Fatalf("no capacity: %d %s", got.Code, got.Body.String())
	}
	for _, bad := range []string{`{"count":0}`, `{"count":4}`, `{}`, `{"count":1,"template":"x"}`, `not json`} {
		if got := request(h, "POST", "/v1/snapshots/"+snap.Id+"/fork", owner, bad); got.Code != 400 {
			t.Fatalf("%s: %d", bad, got.Code)
		}
	}
}

func TestFork_ChargesTheCreationRateLimitPerMember(t *testing.T) {
	h, _, owner, _ := snapshotAPI(t, 10, 4)
	source := decodeBody[gen.Sandbox](t, request(h, "POST", "/v1/sandboxes", owner, `{"template":"base"}`)) // 1 token
	snap := decodeBody[gen.Snapshot](t, request(h, "POST", "/v1/sandboxes/"+source.Id+"/snapshots", owner, ""))
	got := request(h, "POST", "/v1/snapshots/"+snap.Id+"/fork", owner, `{"count":3}`) // 3 more tokens
	if got.Code != 201 {
		t.Fatal(got.Code, got.Body.String())
	}
	denied := request(h, "POST", "/v1/snapshots/"+snap.Id+"/fork", owner, `{"count":2}`)
	if denied.Code != 429 || errorCode(t, denied) != "rate_limited" || denied.Header().Get("Retry-After") == "" {
		t.Fatalf("batch was not rate limited: %d %s", denied.Code, denied.Body.String())
	}
}
