package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecideCookie_Table(t *testing.T) {
	const host = "arcana.example:7070"
	allowed := []string{"https://dash.example"}
	for _, tc := range []struct {
		name                        string
		method, path, origin, fetch string
		want                        cookieVerdict
		class                       routeClass
	}{
		{"same-origin GET via Sec-Fetch-Site", "GET", "/v1/events", "", "same-origin", cookieAllowed, routeRead},
		{"HEAD same-origin", "HEAD", "/v1/overview", "", "same-origin", cookieAllowed, routeRead},
		{"GET with matching Origin", "GET", "/v1/events", "http://arcana.example:7070", "", cookieAllowed, routeRead},
		{"GET with allowed Origin", "GET", "/v1/events", "https://dash.example", "cross-site", cookieAllowed, routeRead},
		{"allowed origin is case-insensitive", "GET", "/v1/events", "HTTPS://DASH.EXAMPLE", "", cookieAllowed, routeRead},
		{"logout with Origin", "DELETE", logoutPath, "http://arcana.example:7070", "", cookieAllowed, routeLogout},
		{"logout without Origin is refused", "DELETE", logoutPath, "", "same-origin", cookieOriginForbidden, routeLogout},
		{"create sandbox with Origin", "POST", "/v1/sandboxes", "http://arcana.example:7070", "same-origin", cookieAllowed, routeWrite},
		{"create sandbox needs Origin, not Sec-Fetch-Site", "POST", "/v1/sandboxes", "", "same-origin", cookieOriginForbidden, routeWrite},
		{"create sandbox from a foreign Origin", "POST", "/v1/sandboxes", "https://evil.example", "same-origin", cookieOriginForbidden, routeWrite},
		{"destroy a sandbox", "DELETE", "/v1/sandboxes/sbx_a", "http://arcana.example:7070", "", cookieAllowed, routeWrite},
		{"revoke a key is key management", "DELETE", "/v1/keys/key_a", "http://arcana.example:7070", "", cookieAllowed, routeKeyAdmin},
		{"PUT file is not a dashboard route", "PUT", "/v1/sandboxes/sbx_a/files", "http://arcana.example:7070", "same-origin", cookieRouteForbidden, routeDenied},
		{"exec is not a dashboard route", "POST", "/v1/sandboxes/sbx_a/exec", "http://arcana.example:7070", "same-origin", cookieRouteForbidden, routeDenied},
		{"POST sessions", "POST", "/v1/sessions", "http://arcana.example:7070", "same-origin", cookieRouteForbidden, routeDenied},
		{"PATCH", "PATCH", "/v1/events", "http://arcana.example:7070", "same-origin", cookieRouteForbidden, routeDenied},
		{"the exec stream is a GET that runs code", "GET", "/v1/sandboxes/sbx_a/exec/stream", "http://arcana.example:7070", "same-origin", cookieRouteForbidden, routeDenied},
		{"cross-site GET", "GET", "/v1/events", "", "cross-site", cookieOriginForbidden, routeRead},
		{"same-site GET is not same-origin", "GET", "/v1/events", "", "same-site", cookieOriginForbidden, routeRead},
		{"navigation GET", "GET", "/v1/events", "", "none", cookieOriginForbidden, routeRead},
		{"no headers at all", "GET", "/v1/events", "", "", cookieOriginForbidden, routeRead},
		{"foreign Origin", "GET", "/v1/events", "https://evil.example", "", cookieOriginForbidden, routeRead},
		{"foreign Origin beats same-origin claim", "GET", "/v1/events", "https://evil.example", "same-origin", cookieOriginForbidden, routeRead},
		{"null Origin", "GET", "/v1/events", "null", "same-origin", cookieOriginForbidden, routeRead},
		{"origin with a path", "GET", "/v1/events", "http://arcana.example:7070/x", "", cookieOriginForbidden, routeRead},
		{"look-alike host", "GET", "/v1/events", "http://arcana.example:7070.evil.example", "", cookieOriginForbidden, routeRead},
		{"wrong scheme", "GET", "/v1/events", "ftp://arcana.example:7070", "", cookieOriginForbidden, routeRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, class := decideCookie(tc.method, tc.path, tc.origin, tc.fetch, host, allowed)
			if got != tc.want || class != tc.class {
				t.Fatalf("got verdict %d class %d, want %d and %d", got, class, tc.want, tc.class)
			}
		})
	}
}

func TestClassifyRoute_IsAnAllowlistOfTheDashboardsRoutes(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         routeClass
	}{
		{"GET", "/v1/overview", routeRead},
		{"GET", "/v1/events", routeRead},
		{"GET", "/v1/templates", routeRead},
		{"GET", "/v1/sandboxes", routeRead},
		{"GET", "/v1/sandboxes/sbx_a", routeRead},
		{"GET", "/v1/sandboxes/sbx_a/execs", routeRead},
		{"GET", "/v1/sandboxes/sbx_a/history", routeRead},
		{"GET", "/v1/sandboxes/sbx_a/execs/exe_a/output", routeRead},
		{"GET", "/v1/snapshots", routeRead},
		{"GET", "/v1/snapshots/snp_a", routeRead},
		{"GET", "/v1/audit", routeRead},
		{"GET", "/v1/keys", routeRead},
		{"GET", "/v1/sessions/current", routeRead},
		{"HEAD", "/v1/sandboxes", routeRead},
		{"POST", "/v1/sandboxes", routeWrite},
		{"POST", "/v1/sandboxes/sbx_a/keepalive", routeWrite},
		{"POST", "/v1/sandboxes/sbx_a/snapshots", routeWrite},
		{"POST", "/v1/snapshots/snp_a/fork", routeWrite},
		{"DELETE", "/v1/sandboxes/sbx_a", routeWrite},
		{"DELETE", "/v1/snapshots/snp_a", routeWrite},
		{"POST", "/v1/keys", routeKeyAdmin},
		{"DELETE", "/v1/keys/key_a", routeKeyAdmin},
		{"DELETE", "/v1/sessions/current", routeLogout},
		// Everything that runs code or moves files stays with API keys, whatever the method.
		{"POST", "/v1/sandboxes/sbx_a/exec", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/exec/stream", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/files", routeDenied},
		{"PUT", "/v1/sandboxes/sbx_a/files", routeDenied},
		{"DELETE", "/v1/sandboxes/sbx_a/files", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/files/list", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/files/archive", routeDenied},
		{"PUT", "/v1/sandboxes/sbx_a/files/archive", routeDenied},
		{"POST", "/v1/sessions", routeDenied},
		{"GET", "/v1/health", routeDenied},
		{"GET", "/ui", routeDenied},
		{"PATCH", "/v1/sandboxes/sbx_a", routeDenied},
		{"PUT", "/v1/sandboxes", routeDenied},
		{"POST", "/v1/sandboxes/sbx_a", routeDenied},
		{"GET", "/v1/sandboxes/", routeDenied},
		{"GET", "/v1/sandboxes//execs", routeDenied},
		{"GET", "/v1/", routeDenied},
		{"GET", "/v1", routeDenied},
		{"DELETE", "/v1/sandboxes/sbx_a/keepalive", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/keepalive", routeDenied},
		{"POST", "/v1/keys/key_a", routeDenied},
		{"POST", "/v1/snapshots", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/execs/exe_a", routeDenied},
		{"GET", "/v1/sandboxes/sbx_a/execs/exe_a/output/x", routeDenied},
	} {
		if got := classifyRoute(tc.method, tc.path); got != tc.want {
			t.Errorf("%s %s: class %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestCSRFToken_IsDerivedPerSessionAndComparedExactly(t *testing.T) {
	a, b := sha256.Sum256([]byte("session a")), sha256.Sum256([]byte("session b"))
	ta, tb := csrfToken(a[:]), csrfToken(b[:])
	if ta == tb || len(ta) != 43 || csrfToken(a[:]) != ta {
		t.Fatalf("tokens must be stable per session and differ between sessions: %q %q", ta, tb)
	}
	if !csrfValid(ta, ta) || csrfValid(ta, tb) || csrfValid("", ta) || csrfValid(ta[:42], ta) || csrfValid(ta+"x", ta) || csrfValid(ta, "") {
		t.Fatal("csrfValid accepted a wrong token")
	}
}

func sessionFixture(t *testing.T) (*Service, *fakeClock, Principal, string) {
	t.Helper()
	service, _, clock, _ := fixture(t)
	service.WithSessions(SessionPolicy{TTL: time.Hour, AllowedOrigins: []string{"https://dash.example"}})
	key, raw, err := service.Create(context.Background(), "dash", false)
	if err != nil {
		t.Fatal(err)
	}
	return service, clock, Principal{ID: key.ID}, raw
}

func TestSessions_HashedTokenExpiryRevocationAndEnd(t *testing.T) {
	service, clock, p, _ := sessionFixture(t)
	ctx := context.Background()
	token, session, err := service.CreateSession(ctx, p)
	if err != nil || len(token) != sessionTokenLength || session.ID == "" || !session.ExpiresAt.Equal(clock.now.Add(time.Hour)) {
		t.Fatalf("%q %+v %v", token, session, err)
	}
	// Only the hash is stored: the token appears nowhere in the table.
	rows, err := service.store.SQL.QueryContext(ctx, "SELECT id, key_id, token_hash FROM sessions")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, key string
		var hash []byte
		if err = rows.Scan(&id, &key, &hash); err != nil || bytes.Contains(hash, []byte(token)) || len(hash) != 32 {
			t.Fatal("token stored in clear or hash malformed", err)
		}
	}
	_ = rows.Close()
	got, verified, err := service.verifySession(ctx, token)
	if err != nil || got.ID != p.ID || got.Admin || verified.ID != session.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if session.CSRFToken == "" || verified.CSRFToken != session.CSRFToken {
		t.Fatalf("the CSRF token must be recoverable for the life of the session: %q %q", session.CSRFToken, verified.CSRFToken)
	}
	if _, _, err = service.verifySession(ctx, strings.Repeat("x", sessionTokenLength)); err == nil {
		t.Fatal("unknown token accepted")
	}
	if _, _, err = service.verifySession(ctx, "short"); err == nil {
		t.Fatal("malformed token accepted")
	}
	// Revoking the key kills the session at once.
	if err = service.Revoke(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.verifySession(ctx, token); err == nil {
		t.Fatal("session survived key revocation")
	}
	// A fresh key's session expires with the TTL.
	key2, _, _ := service.Create(ctx, "second", true)
	token2, _, err := service.CreateSession(ctx, Principal{ID: key2.ID, Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err = service.verifySession(ctx, token2); err != nil || !got.Admin {
		t.Fatal("admin flag lost", err)
	}
	clock.now = clock.now.Add(time.Hour)
	if _, _, err = service.verifySession(ctx, token2); err == nil {
		t.Fatal("expired session accepted")
	}
	clock.now = clock.now.Add(-time.Hour)
	_, session3, _ := service.CreateSession(ctx, Principal{ID: key2.ID, Admin: true})
	if err = service.EndSession(ctx, session3.ID); err != nil || service.EndSession(ctx, session3.ID) != nil {
		t.Fatal("end must be idempotent", err)
	}
}

func TestSessions_PerKeyCapDropsTheOldestAndExpiredAreDeleted(t *testing.T) {
	service, clock, p, _ := sessionFixture(t)
	ctx := context.Background()
	var tokens []string
	for i := 0; i < maxSessionsPerKey+3; i++ {
		clock.now = clock.now.Add(time.Second)
		token, _, err := service.CreateSession(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	if n, _ := service.store.Queries.CountSessionsForKey(ctx, p.ID); n != maxSessionsPerKey {
		t.Fatalf("sessions: %d", n)
	}
	if _, _, err := service.verifySession(ctx, tokens[0]); err == nil {
		t.Fatal("oldest session kept past the cap")
	}
	if _, _, err := service.verifySession(ctx, tokens[len(tokens)-1]); err != nil {
		t.Fatal("newest session dropped", err)
	}
	clock.now = clock.now.Add(2 * time.Hour)
	if _, _, err := service.CreateSession(ctx, p); err != nil {
		t.Fatal(err)
	}
	if n, _ := service.store.Queries.CountSessionsForKey(ctx, p.ID); n != 1 {
		t.Fatalf("expired sessions not pruned: %d", n)
	}
	if n, err := service.ActiveSessions(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	plain, _, _, _ := fixture(t)
	if _, _, err := plain.CreateSession(ctx, p); err == nil {
		t.Fatal("sessions created while disabled")
	}
}

func TestMiddleware_CookiesReachOnlyDashboardRoutesAndWritesNeedProofs(t *testing.T) {
	service, clock, p, raw := sessionFixture(t)
	ctx := context.Background()
	token, session, err := service.CreateSession(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	_, otherSession, err := service.CreateSession(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, _ := FromContext(r.Context())
		_, viaSession := SessionFromContext(r.Context())
		seen = append(seen, r.Method+" "+who.ID+" "+map[bool]string{true: "cookie", false: "bearer"}[viaSession])
		w.WriteHeader(204)
	}))
	do := func(method, path string, headers map[string]string, cookies ...string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Host = "arcana.example:7070"
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		for _, c := range cookies {
			r.AddCookie(&http.Cookie{Name: SessionCookie, Value: c}) // #nosec G124 -- request cookie in a test
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	origin := map[string]string{"Origin": "http://arcana.example:7070"}
	proofs := map[string]string{"Origin": "http://arcana.example:7070", CSRFHeader: session.CSRFToken}
	foreign := map[string]string{"Origin": "https://evil.example", CSRFHeader: session.CSRFToken}
	noOrigin := map[string]string{CSRFHeader: session.CSRFToken, "Sec-Fetch-Site": "same-origin"}
	wrong := map[string]string{"Origin": "http://arcana.example:7070", CSRFHeader: otherSession.CSRFToken}
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		cookies []string
		want    int
	}{
		{"GET same-origin", "GET", "/v1/sandboxes", same, []string{token}, 204},
		{"logout with Origin only", "DELETE", logoutPath, origin, []string{token}, 204},
		{"create sandbox without the CSRF token (ESC-22)", "POST", "/v1/sandboxes", origin, []string{token}, 403},
		{"create sandbox with another session's token (ESC-25)", "POST", "/v1/sandboxes", wrong, []string{token}, 403},
		{"create sandbox with an empty token (ESC-25)", "POST", "/v1/sandboxes", map[string]string{"Origin": "http://arcana.example:7070", CSRFHeader: ""}, []string{token}, 403},
		{"create sandbox from a foreign Origin (ESC-25)", "POST", "/v1/sandboxes", foreign, []string{token}, 403},
		{"create sandbox without an Origin (ESC-25)", "POST", "/v1/sandboxes", noOrigin, []string{token}, 403},
		{"create sandbox with both proofs", "POST", "/v1/sandboxes", proofs, []string{token}, 204},
		{"destroy with both proofs", "DELETE", "/v1/sandboxes/sbx_a", proofs, []string{token}, 204},
		{"keepalive with both proofs", "POST", "/v1/sandboxes/sbx_a/keepalive", proofs, []string{token}, 204},
		{"snapshot with both proofs", "POST", "/v1/sandboxes/sbx_a/snapshots", proofs, []string{token}, 204},
		{"fork with both proofs", "POST", "/v1/snapshots/snp_a/fork", proofs, []string{token}, 204},
		{"delete snapshot without the token", "DELETE", "/v1/snapshots/snp_a", origin, []string{token}, 403},
		{"POST exec stays forbidden with both proofs (ESC-26)", "POST", "/v1/sandboxes/sbx_a/exec", proofs, []string{token}, 403},
		{"the exec stream stays forbidden (ESC-26)", "GET", "/v1/sandboxes/sbx_a/exec/stream", proofs, []string{token}, 403},
		{"file read stays forbidden (ESC-26)", "GET", "/v1/sandboxes/sbx_a/files", proofs, []string{token}, 403},
		{"file write stays forbidden (ESC-26)", "PUT", "/v1/sandboxes/sbx_a/files", proofs, []string{token}, 403},
		{"archive upload stays forbidden (ESC-26)", "PUT", "/v1/sandboxes/sbx_a/files/archive", proofs, []string{token}, 403},
		{"create session by cookie", "POST", "/v1/sessions", proofs, []string{token}, 403},
		{"cross-site GET (ESC-23)", "GET", "/v1/events", map[string]string{"Sec-Fetch-Site": "cross-site"}, []string{token}, 403},
		{"foreign Origin GET (ESC-23)", "GET", "/v1/events", map[string]string{"Origin": "https://evil.example"}, []string{token}, 403},
		{"headerless GET", "GET", "/v1/events", nil, []string{token}, 403},
		{"allowed origin GET", "GET", "/v1/events", map[string]string{"Origin": "https://dash.example"}, []string{token}, 204},
		{"unknown token", "GET", "/v1/sandboxes", same, []string{strings.Repeat("a", sessionTokenLength)}, 401},
		{"two cookies of the same name", "GET", "/v1/sandboxes", same, []string{token, token}, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(tc.method, tc.path, tc.headers, tc.cookies...); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
	// An Authorization header always takes precedence over the cookie, so API clients are unaffected
	// and a bad bearer never falls back to the cookie. Bearer requests need no CSRF token.
	before := len(seen)
	if got := do("POST", "/v1/sandboxes/sbx_a/exec", map[string]string{"Authorization": "Bearer " + raw}, token); got != 204 || seen[before] != "POST "+p.ID+" bearer" {
		t.Fatalf("bearer with a cookie present: %d %v", got, seen)
	}
	if got := do("GET", "/v1/sandboxes", map[string]string{"Authorization": "Bearer nope", "Sec-Fetch-Site": "same-origin"}, token); got != 401 {
		t.Fatalf("invalid bearer fell back to the cookie: %d", got)
	}
	// Revoking the key invalidates the cookie immediately, for writes too; expiry does too.
	_ = service.Revoke(ctx, p.ID)
	if got := do("GET", "/v1/sandboxes", same, token); got != 401 {
		t.Fatalf("revoked key's session still works: %d", got)
	}
	if got := do("POST", "/v1/sandboxes", proofs, token); got != 401 {
		t.Fatalf("revoked key's session can still write: %d", got)
	}
	// Cookies are ignored entirely when sessions are not enabled.
	plain := New(service.store, clock)
	h := plain.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest("GET", "/v1/sandboxes", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token}) // #nosec G124 -- request cookie in a test
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cookie honoured without sessions enabled", w.Code)
	}
}

func TestMiddleware_KeyManagementNeedsTheAdminKeyReEntered(t *testing.T) {
	service, _, plain, plainRaw := sessionFixture(t)
	ctx := context.Background()
	adminKey, adminRaw, err := service.Create(ctx, "admin", true)
	if err != nil {
		t.Fatal(err)
	}
	otherAdmin, otherRaw, err := service.Create(ctx, "other admin", true)
	if err != nil {
		t.Fatal(err)
	}
	admin := Principal{ID: adminKey.ID, Admin: true}
	adminToken, adminSession, err := service.CreateSession(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	plainToken, plainSession, err := service.CreateSession(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	handler := service.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	do := func(method, path, token, csrf, reauth string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Host = "arcana.example:7070"
		r.Header.Set("Origin", "http://arcana.example:7070")
		if csrf != "" {
			r.Header.Set(CSRFHeader, csrf)
		}
		if reauth != "" {
			r.Header.Set(ReauthHeader, reauth)
		}
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: token}) // #nosec G124 -- request cookie in a test
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for _, tc := range []struct {
		name                         string
		method, path, token, csrf, r string
		want                         int
	}{
		{"create key with both proofs and the admin key (ESC-27)", "POST", "/v1/keys", adminToken, adminSession.CSRFToken, adminRaw, 204},
		{"revoke key with both proofs and the admin key", "DELETE", "/v1/keys/key_a", adminToken, adminSession.CSRFToken, adminRaw, 204},
		{"create key without re-entry (ESC-27)", "POST", "/v1/keys", adminToken, adminSession.CSRFToken, "", 403},
		{"create key without the CSRF token (ESC-27)", "POST", "/v1/keys", adminToken, "", adminRaw, 403},
		{"create key re-entering a wrong key (ESC-27)", "POST", "/v1/keys", adminToken, adminSession.CSRFToken, "arc_live_" + strings.Repeat("x", 32), 403},
		{"create key re-entering another admin's key (ESC-27)", "POST", "/v1/keys", adminToken, adminSession.CSRFToken, otherRaw, 403},
		{"create key re-entering an oversized value", "POST", "/v1/keys", adminToken, adminSession.CSRFToken, strings.Repeat("a", 65), 403},
		{"non-admin session re-entering its own key (ESC-27)", "POST", "/v1/keys", plainToken, plainSession.CSRFToken, plainRaw, 403},
		{"non-admin session re-entering an admin key (ESC-27)", "POST", "/v1/keys", plainToken, plainSession.CSRFToken, adminRaw, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(tc.method, tc.path, tc.token, tc.csrf, tc.r); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
	// Re-entering a revoked admin key fails even with a valid session of another key.
	if err = service.Revoke(ctx, otherAdmin.ID); err != nil {
		t.Fatal(err)
	}
	if got := do("POST", "/v1/keys", adminToken, adminSession.CSRFToken, otherRaw); got != 403 {
		t.Fatalf("a revoked key was accepted for re-entry: %d", got)
	}
}
