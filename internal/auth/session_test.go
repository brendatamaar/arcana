package auth

import (
	"bytes"
	"context"
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
	}{
		{"same-origin GET via Sec-Fetch-Site", "GET", "/v1/events", "", "same-origin", cookieAllowed},
		{"HEAD same-origin", "HEAD", "/v1/overview", "", "same-origin", cookieAllowed},
		{"GET with matching Origin", "GET", "/v1/events", "http://arcana.example:7070", "", cookieAllowed},
		{"GET with allowed Origin", "GET", "/v1/events", "https://dash.example", "cross-site", cookieAllowed},
		{"allowed origin is case-insensitive", "GET", "/v1/events", "HTTPS://DASH.EXAMPLE", "", cookieAllowed},
		{"logout with Origin", "DELETE", logoutPath, "http://arcana.example:7070", "", cookieAllowed},
		{"logout without Origin is refused", "DELETE", logoutPath, "", "same-origin", cookieOriginForbidden},
		{"POST create sandbox", "POST", "/v1/sandboxes", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"PUT file", "PUT", "/v1/sandboxes/sbx_a/files", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"DELETE a sandbox", "DELETE", "/v1/sandboxes/sbx_a", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"DELETE keys", "DELETE", "/v1/keys/key_a", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"POST sessions", "POST", "/v1/sessions", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"PATCH", "PATCH", "/v1/events", "http://arcana.example:7070", "same-origin", cookieMethodForbidden},
		{"cross-site GET", "GET", "/v1/events", "", "cross-site", cookieOriginForbidden},
		{"same-site GET is not same-origin", "GET", "/v1/events", "", "same-site", cookieOriginForbidden},
		{"navigation GET", "GET", "/v1/events", "", "none", cookieOriginForbidden},
		{"no headers at all", "GET", "/v1/events", "", "", cookieOriginForbidden},
		{"foreign Origin", "GET", "/v1/events", "https://evil.example", "", cookieOriginForbidden},
		{"foreign Origin beats same-origin claim", "GET", "/v1/events", "https://evil.example", "same-origin", cookieOriginForbidden},
		{"null Origin", "GET", "/v1/events", "null", "same-origin", cookieOriginForbidden},
		{"origin with a path", "GET", "/v1/events", "http://arcana.example:7070/x", "", cookieOriginForbidden},
		{"look-alike host", "GET", "/v1/events", "http://arcana.example:7070.evil.example", "", cookieOriginForbidden},
		{"wrong scheme", "GET", "/v1/events", "ftp://arcana.example:7070", "", cookieOriginForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideCookie(tc.method, tc.path, tc.origin, tc.fetch, host, allowed); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
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

func TestMiddleware_CookieSessionsAreReadOnlyAndSameOriginOnly(t *testing.T) {
	service, clock, p, raw := sessionFixture(t)
	token, _, err := service.CreateSession(context.Background(), p)
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
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		cookies []string
		want    int
	}{
		{"GET same-origin", "GET", "/v1/sandboxes", same, []string{token}, 204},
		{"logout with Origin", "DELETE", logoutPath, origin, []string{token}, 204},
		{"POST create sandbox is forbidden (ESC-22)", "POST", "/v1/sandboxes", origin, []string{token}, 403},
		{"POST exec is forbidden (ESC-22)", "POST", "/v1/sandboxes/sbx_a/exec", origin, []string{token}, 403},
		{"DELETE sandbox is forbidden", "DELETE", "/v1/sandboxes/sbx_a", origin, []string{token}, 403},
		{"PUT file is forbidden", "PUT", "/v1/sandboxes/sbx_a/files", origin, []string{token}, 403},
		{"POST fork is forbidden", "POST", "/v1/snapshots/snp_a/fork", origin, []string{token}, 403},
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
	// and a bad bearer never falls back to the cookie.
	before := len(seen)
	if got := do("POST", "/v1/sandboxes", map[string]string{"Authorization": "Bearer " + raw}, token); got != 204 || seen[before] != "POST "+p.ID+" bearer" {
		t.Fatalf("bearer with a cookie present: %d %v", got, seen)
	}
	if got := do("GET", "/v1/sandboxes", map[string]string{"Authorization": "Bearer nope", "Sec-Fetch-Site": "same-origin"}, token); got != 401 {
		t.Fatalf("invalid bearer fell back to the cookie: %d", got)
	}
	// Revoking the key invalidates the cookie immediately; expiry does too.
	_ = service.Revoke(context.Background(), p.ID)
	if got := do("GET", "/v1/sandboxes", same, token); got != 401 {
		t.Fatalf("revoked key's session still works: %d", got)
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
