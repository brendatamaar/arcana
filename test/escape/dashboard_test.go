//go:build linux && escape

package escape

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
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

// request sends one raw HTTP request; the attacks below need header control the client lacks.
func request(ctx context.Context, t *testing.T, method, url, body string, headers map[string]string, cookie *http.Cookie) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// A successful stream would block; the status line alone proves the request was admitted.
		return resp.StatusCode, ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(data)
}

// TestEscapeV3_DashboardSessions covers ESC-22 and ESC-23 against real runsc sandboxes through the
// public API: a session cookie can never change state, and cookie-authenticated streams refuse any
// request that does not provably come from the server's own pages.
func TestEscapeV3_DashboardSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireRunsc(ctx, t)
	docker(ctx, t, "build", "-t", "arcana-base:local", "../../templates/base")
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: escapeRuntime(), AllowInsecureRuntime: escapeRuntime() == "runc", InstallID: database.InstallID, HelperPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	cfg := config.Default()
	cfg.DataDir = dir
	cfg.Limits.Defaults.MemoryMB = 128
	cfg.Limits.Defaults.PIDs = 64
	keys := auth.New(database, auth.SystemClock{}).WithSessions(auth.SessionPolicy{TTL: time.Hour})
	ownerKey, owner, err := keys.Create(ctx, "dashboard-user", false)
	if err != nil {
		t.Fatal(err)
	}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{}, nil).WithEvents(events.NewBus(orchestrator.SystemClock{}, 100, 10))
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service))
	t.Cleanup(func() {
		server.Close()
		cleanup, finish := context.WithTimeout(context.Background(), 30*time.Second)
		defer finish()
		instances, listErr := driver.List(cleanup)
		if listErr != nil {
			t.Error(listErr)
		}
		for _, instance := range instances {
			if destroyErr := driver.Destroy(cleanup, instance.ID); destroyErr != nil {
				t.Error(destroyErr)
			}
		}
	})
	c, err := client.New(server.URL, owner, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	target, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	ran, err := c.Exec(ctx, target.Id, client.ExecRequest{Cmd: []string{"/bin/echo", "secret-output"}})
	if err != nil {
		t.Fatal(err)
	}

	// The victim logs in. The attacker page can make the browser attach this cookie to requests, so
	// the attacks below send the cookie with the best headers an attacker could hope to forge. The
	// attacker cannot read the CSRF token: only same-origin script can fetch it.
	login := func(secret string) (*http.Cookie, string) {
		loginReq, reqErr := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/sessions", nil)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		loginReq.Header.Set("Authorization", "Bearer "+secret)
		loginResp, doErr := http.DefaultClient.Do(loginReq)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer func() { _ = loginResp.Body.Close() }()
		if loginResp.StatusCode != 201 {
			t.Fatalf("login: %d", loginResp.StatusCode)
		}
		var session struct {
			CSRFToken string `json:"csrf_token"`
		}
		if decodeErr := json.NewDecoder(io.LimitReader(loginResp.Body, 1<<16)).Decode(&session); decodeErr != nil || session.CSRFToken == "" {
			t.Fatalf("login response lacks a CSRF token: %v", decodeErr)
		}
		for _, ck := range loginResp.Cookies() {
			if ck.Name == auth.SessionCookie {
				return &http.Cookie{Name: ck.Name, Value: ck.Value}, session.CSRFToken // #nosec G124 -- request cookie in a test
			}
		}
		t.Fatal("no session cookie issued")
		return nil, ""
	}
	cookie, csrf := login(owner)
	_, adminSecret, err := keys.Create(ctx, "dashboard-admin", true)
	if err != nil {
		t.Fatal(err)
	}
	sameOrigin := map[string]string{"Origin": server.URL, "Sec-Fetch-Site": "same-origin"}

	t.Run("ESC22_cookie_cannot_mutate", func(t *testing.T) {
		before, err := c.ListExecs(ctx, target.Id, client.ListOptions{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, attack := range []struct{ method, path, body string }{
			{"POST", "/v1/sandboxes", `{"template":"base"}`},
			{"POST", "/v1/sandboxes/" + target.Id + "/exec", `{"shell":"touch /workspace/pwned"}`},
			{"PUT", "/v1/sandboxes/" + target.Id + "/files?path=/workspace/pwned", "x"},
			{"POST", "/v1/sandboxes/" + target.Id + "/snapshots", ""},
			{"DELETE", "/v1/sandboxes/" + target.Id, ""},
			{"DELETE", "/v1/sandboxes/" + peer.Id + "/files?path=/workspace&recursive=true", ""},
			{"POST", "/v1/keys", `{"name":"backdoor","is_admin":true}`},
		} {
			if got, resp := request(ctx, t, attack.method, server.URL+attack.path, attack.body, sameOrigin, cookie); got != 403 {
				t.Errorf("%s %s with a session cookie: %d %s", attack.method, attack.path, got, resp)
			}
			if got, _ := request(ctx, t, attack.method, server.URL+attack.path, attack.body, nil, cookie); got != 403 {
				t.Errorf("%s %s with a bare cookie: %d", attack.method, attack.path, got)
			}
		}
		// Nothing changed. The exec history is compared first, because the checks below are themselves
		// recorded as execs.
		if after, execErr := c.ListExecs(ctx, target.Id, client.ListOptions{Limit: 100}); execErr != nil || len(after.Items) != len(before.Items) {
			t.Fatalf("an attack ran a command: %d -> %d %v", len(before.Items), len(after.Items), execErr)
		}
		for _, id := range []string{target.Id, peer.Id} {
			if row, getErr := c.GetSandbox(ctx, id); getErr != nil || row.State != "running" {
				t.Fatalf("sandbox %s after the attacks: %+v %v", id, row, getErr)
			}
		}
		if list, listErr := c.ListSandboxes(ctx, client.ListOptions{Limit: 100}); listErr != nil || len(list.Items) != 2 {
			t.Fatalf("sandbox count changed: %v %v", list.Items, listErr)
		}
		if _, readErr := c.ReadFile(ctx, target.Id, "/workspace/pwned"); readErr == nil {
			t.Fatal("a cookie-authenticated request wrote to the sandbox")
		}
	})

	t.Run("ESC23_cross_origin_streams_rejected", func(t *testing.T) {
		streams := []string{"/v1/events", "/v1/sandboxes/" + target.Id + "/execs/" + ran.ExecId + "/output", "/v1/overview"}
		for _, path := range streams {
			for name, headers := range map[string]map[string]string{
				"foreign Origin":   {"Origin": "https://evil.example"},
				"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
				"same-site fetch":  {"Sec-Fetch-Site": "same-site"},
				"no provenance":    nil,
				"null Origin":      {"Origin": "null"},
			} {
				got, resp := request(ctx, t, "GET", server.URL+path, "", headers, cookie)
				if got != 403 {
					t.Errorf("%s with %s: %d", path, name, got)
				}
				if strings.Contains(resp, target.Id) || strings.Contains(resp, "secret-output") {
					t.Errorf("%s with %s leaked data: %s", path, name, resp)
				}
			}
			// Not vacuous: the legitimate same-origin request is admitted.
			if got, _ := request(ctx, t, "GET", server.URL+path, "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie); got != 200 {
				t.Errorf("%s same-origin: %d", path, got)
			}
		}
	})

	t.Run("ESC25_cookie_writes_need_the_csrf_token", func(t *testing.T) {
		otherCookie, otherCSRF := login(owner)
		_ = otherCookie
		keepalive := server.URL + "/v1/sandboxes/" + target.Id + "/keepalive"
		for name, headers := range map[string]map[string]string{
			"no token":                           {"Origin": server.URL},
			"empty token":                        {"Origin": server.URL, auth.CSRFHeader: ""},
			"guessed token":                      {"Origin": server.URL, auth.CSRFHeader: "not-the-token"},
			"another session's token":            {"Origin": server.URL, auth.CSRFHeader: otherCSRF},
			"the real token from a foreign page": {"Origin": "https://evil.example", auth.CSRFHeader: csrf},
			"the real token, null origin":        {"Origin": "null", auth.CSRFHeader: csrf},
			"the real token, no Origin":          {auth.CSRFHeader: csrf, "Sec-Fetch-Site": "same-origin"},
			"the real token claiming same-site":  {"Sec-Fetch-Site": "same-site", auth.CSRFHeader: csrf},
		} {
			if got, resp := request(ctx, t, "POST", keepalive, "", headers, cookie); got != 403 {
				t.Errorf("keepalive with %s: %d %s", name, got, resp)
			}
		}
		// The token is read through the session endpoint, which a foreign page cannot reach.
		if got, resp := request(ctx, t, "GET", server.URL+"/v1/sessions/current", "", map[string]string{"Origin": "https://evil.example"}, cookie); got != 403 || strings.Contains(resp, csrf) {
			t.Errorf("a foreign page read the CSRF token: %d", got)
		}
		// Not vacuous: the page's own request, with both proofs, is admitted.
		if got, resp := request(ctx, t, "POST", keepalive, "", map[string]string{"Origin": server.URL, auth.CSRFHeader: csrf}, cookie); got != 200 {
			t.Errorf("keepalive with both proofs: %d %s", got, resp)
		}
	})

	t.Run("ESC26_cookies_never_reach_exec_or_files", func(t *testing.T) {
		before, err := c.ListExecs(ctx, target.Id, client.ListOptions{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		// Even a page that holds the cookie, the token and a matching Origin cannot run code or
		// move files: those need an API key. The exec stream is a GET, so it is listed explicitly.
		proofs := map[string]string{"Origin": server.URL, "Sec-Fetch-Site": "same-origin", auth.CSRFHeader: csrf}
		for _, attack := range []struct{ method, path, body string }{
			{"POST", "/v1/sandboxes/" + target.Id + "/exec", `{"shell":"touch /workspace/pwned26"}`},
			{"GET", "/v1/sandboxes/" + target.Id + "/exec/stream", ""},
			{"GET", "/v1/sandboxes/" + target.Id + "/files?path=/workspace/pwned", ""},
			{"GET", "/v1/sandboxes/" + target.Id + "/files/list?path=/workspace", ""},
			{"GET", "/v1/sandboxes/" + target.Id + "/files/archive?path=/workspace", ""},
			{"PUT", "/v1/sandboxes/" + target.Id + "/files?path=/workspace/pwned26", "x"},
			{"PUT", "/v1/sandboxes/" + target.Id + "/files/archive?path=/workspace", "x"},
			{"DELETE", "/v1/sandboxes/" + target.Id + "/files?path=/workspace&recursive=true", ""},
		} {
			if got, resp := request(ctx, t, attack.method, server.URL+attack.path, attack.body, proofs, cookie); got != 403 {
				t.Errorf("%s %s with a cookie and every proof: %d %s", attack.method, attack.path, got, resp)
			}
		}
		if after, execErr := c.ListExecs(ctx, target.Id, client.ListOptions{Limit: 100}); execErr != nil || len(after.Items) != len(before.Items) {
			t.Fatalf("an attack ran a command: %d -> %d %v", len(before.Items), len(after.Items), execErr)
		}
		if _, readErr := c.ReadFile(ctx, target.Id, "/workspace/pwned26"); readErr == nil {
			t.Fatal("a cookie-authenticated request wrote to the sandbox")
		}
	})

	t.Run("ESC27_key_management_needs_the_admin_key_again", func(t *testing.T) {
		adminCookie, adminCSRF := login(adminSecret)
		proofs := func(reauth string) map[string]string {
			h := map[string]string{"Origin": server.URL, auth.CSRFHeader: adminCSRF}
			if reauth != "" {
				h[auth.ReauthHeader] = reauth
			}
			return h
		}
		count := func() int {
			list, listErr := keys.List(ctx, "", 200)
			if listErr != nil {
				t.Fatal(listErr)
			}
			return len(list)
		}
		before := count()
		// A hijacked admin session (cookie, token, Origin) cannot mint a backdoor key without the
		// admin key, nor with the wrong or another key.
		for name, reauth := range map[string]string{"nothing": "", "a wrong key": "arc_live_" + strings.Repeat("z", 32), "a non-admin key": owner} {
			if got, resp := request(ctx, t, "POST", server.URL+"/v1/keys", `{"name":"backdoor","is_admin":true}`, proofs(reauth), adminCookie); got != 403 {
				t.Errorf("creating a key re-entering %s: %d %s", name, got, resp)
			}
		}
		// A non-admin session fails even when it presents an admin key.
		if got, _ := request(ctx, t, "POST", server.URL+"/v1/keys", `{"name":"backdoor"}`, map[string]string{"Origin": server.URL, auth.CSRFHeader: csrf, auth.ReauthHeader: adminSecret}, cookie); got != 403 {
			t.Errorf("a non-admin session created a key: %d", got)
		}
		if count() != before {
			t.Fatal("a refused request created a key")
		}
		// Not vacuous: with the admin key typed again, the page can create a key.
		if got, resp := request(ctx, t, "POST", server.URL+"/v1/keys", `{"name":"legit"}`, proofs(adminSecret), adminCookie); got != 201 {
			t.Errorf("creating a key with re-entry: %d %s", got, resp)
		}
	})

	// Arcana and the peer sandbox stay healthy after every attack.
	if _, err = c.Health(ctx); err != nil {
		t.Fatal("Arcana unhealthy", err)
	}
	if result, execErr := c.Exec(ctx, peer.Id, client.ExecRequest{Cmd: []string{"/bin/sleep", "0"}}); execErr != nil || result.ExitCode != 0 {
		t.Fatal("peer sandbox unhealthy", result, execErr)
	}
	docker(ctx, t, "info", "--format", "{{.ServerVersion}}")

	t.Run("ESC23_session_dies_with_its_key", func(t *testing.T) {
		if revokeErr := keys.Revoke(ctx, ownerKey.ID); revokeErr != nil {
			t.Fatal(revokeErr)
		}
		if got, _ := request(ctx, t, "GET", server.URL+"/v1/overview", "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie); got != 401 {
			t.Fatalf("a stolen session outlived its revoked key: %d", got)
		}
	})
}
