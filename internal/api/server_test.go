package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/store"
)

func apiFixture(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(db, auth.SystemClock{})
	_, admin, err := keys.Create(context.Background(), "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	_, user, err := keys.Create(context.Background(), "user", false)
	if err != nil {
		t.Fatal(err)
	}
	return New(keys, slog.New(slog.NewJSONHandler(io.Discard, nil)), config.Default().Limits.Files, nil), admin, user
}

func request(handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestContract_Validates(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err = document.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if document.Paths.Len() != 11 {
		t.Fatalf("unexpected endpoint path count: %d", document.Paths.Len())
	}
}

func TestServer_PublicHealthAndProtectedStubs(t *testing.T) {
	handler, admin, _ := apiFixture(t)
	if got := request(handler, "GET", "/v1/health", "", ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	routes := [][2]string{{"POST", "/v1/sandboxes"}, {"GET", "/v1/sandboxes"}, {"GET", "/v1/sandboxes/sbx_test"}, {"DELETE", "/v1/sandboxes/sbx_test"}, {"POST", "/v1/sandboxes/sbx_test/keepalive"}, {"POST", "/v1/sandboxes/sbx_test/exec"}, {"GET", "/v1/sandboxes/sbx_test/exec/stream"}, {"GET", "/v1/sandboxes/sbx_test/files?path=/workspace/a"}, {"PUT", "/v1/sandboxes/sbx_test/files?path=/workspace/a"}, {"DELETE", "/v1/sandboxes/sbx_test/files?path=/workspace/a"}, {"GET", "/v1/sandboxes/sbx_test/files/list?path=/workspace"}, {"GET", "/v1/sandboxes/sbx_test/files/archive?path=/workspace"}, {"PUT", "/v1/sandboxes/sbx_test/files/archive?path=/workspace"}}
	for _, route := range routes {
		t.Run(route[0]+route[1], func(t *testing.T) {
			if got := request(handler, route[0], route[1], "", ""); got.Code != 401 {
				t.Fatalf("unauthenticated: %d", got.Code)
			}
			got := request(handler, route[0], route[1], admin, "")
			if got.Code != 501 {
				t.Fatalf("stub: %d %s", got.Code, got.Body.String())
			}
			var envelope apierr.Envelope
			if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != apierr.Internal || envelope.Error.RequestID != got.Header().Get("X-Request-ID") || envelope.Error.RequestID == "" {
				t.Fatal("missing standard error envelope")
			}
		})
	}
	if got := request(handler, "GET", "/v1/sandboxes?limit=invalid", admin, ""); got.Code != 400 {
		t.Fatalf("bad query: %d", got.Code)
	}
}

func TestServer_KeyEndpointsRequireAdminAndNeverListSecrets(t *testing.T) {
	handler, admin, user := apiFixture(t)
	for _, route := range [][2]string{{"GET", "/v1/keys"}, {"POST", "/v1/keys"}, {"DELETE", "/v1/keys/key_other"}} {
		if got := request(handler, route[0], route[1], "", ""); got.Code != 401 {
			t.Fatalf("unauthorized: %d", got.Code)
		}
		if got := request(handler, route[0], route[1], user, ""); got.Code != 403 {
			t.Fatalf("non-admin: %d", got.Code)
		}
	}
	created := request(handler, "POST", "/v1/keys", admin, `{"name":"new user"}`)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var result struct {
		Secret string   `json:"secret"`
		Key    auth.Key `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Secret == "" || result.Key.Admin {
		t.Fatal("bad created key")
	}
	listed := request(handler, "GET", "/v1/keys?limit=1", admin, "")
	if listed.Code != 200 || strings.Contains(listed.Body.String(), result.Secret) || strings.Contains(listed.Body.String(), "hash") {
		t.Fatal("list leaked credential")
	}
	if got := request(handler, "DELETE", "/v1/keys/"+result.Key.ID, admin, ""); got.Code != 204 {
		t.Fatalf("revoke: %d", got.Code)
	}
	if got := request(handler, "GET", "/v1/sandboxes", result.Secret, ""); got.Code != 401 {
		t.Fatal("revoked key accepted")
	}
	for _, body := range []string{`null`, `{}`, `{"name":""}`, `{"name":"test","extra":true}`, `{"name":"one"}{"name":"two"}`} {
		if got := request(handler, "POST", "/v1/keys", admin, body); got.Code != 400 {
			t.Fatalf("accepted invalid JSON: %d", got.Code)
		}
	}
	if got := request(handler, "GET", "/v1/keys?limit=-1", admin, ""); got.Code != 400 {
		t.Fatal("accepted unbounded list")
	}
}

func TestServer_RequestCapsAndRecovery(t *testing.T) {
	handler, admin, _ := apiFixture(t)
	if got := request(handler, "POST", "/v1/keys", admin, strings.Repeat("x", 65537)); got.Code != 413 {
		t.Fatal("large body accepted")
	}
	// Chunked bodies must be capped while decoding, including trailing whitespace.
	r := httptest.NewRequest("POST", "/v1/keys", strings.NewReader(`{"name":"test"}`+strings.Repeat(" ", 65536)))
	r.ContentLength = -1
	r.Header.Set("Authorization", "Bearer "+admin)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("uncapped chunked body: %d %s", w.Code, w.Body.String())
	}
	var logs bytes.Buffer
	server := &Server{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	recovered := apierr.RequestIDs(server.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret panic content") })))
	w = request(recovered, "GET", "/panic", "", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "secret") || strings.Contains(logs.String(), "secret") {
		t.Fatal("panic data leaked or incorrect status")
	}
}

func TestServe_CancellationDrainsAndStopsListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	response := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); w.WriteHeader(204) })
	go func() {
		done <- Serve(ctx, listener, handler, time.Second, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	}()
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		r, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			err = r.Body.Close()
		}
		response <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server failed to shut down")
	}
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second); err == nil {
		_ = connection.Close()
		t.Fatal("listener still accepting")
	}
}
