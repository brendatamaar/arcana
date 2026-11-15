package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

// cookieVerdict is the outcome of the cookie-authentication policy for one request.
type cookieVerdict uint8

const (
	cookieAllowed cookieVerdict = iota
	// cookieRouteForbidden: a session cookie may not reach this endpoint at all. Code execution
	// and file transfer stay with API keys (PRD FR-WD-5).
	cookieRouteForbidden
	// cookieOriginForbidden: the request did not provably come from this server's own pages.
	cookieOriginForbidden
)

// routeClass says what a session cookie may do with an endpoint and which proofs it must add.
type routeClass uint8

const (
	// routeDenied endpoints never accept a cookie.
	routeDenied routeClass = iota
	// routeRead are the dashboard's read endpoints (GET and HEAD).
	routeRead
	// routeWrite mutates sandboxes or snapshots and needs the CSRF token.
	routeWrite
	// routeKeyAdmin mutates API keys and needs the CSRF token and the admin key re-entered.
	routeKeyAdmin
	// routeLogout ends the session; it needs only a same-origin Origin.
	routeLogout
)

// logoutPath is the one endpoint that ends the cookie's own session.
const logoutPath = "/v1/sessions/current"

// Request headers that carry the extra proofs for cookie-authenticated mutations.
const (
	// CSRFHeader carries the session's synchronizer token.
	CSRFHeader = "X-CSRF-Token"
	// ReauthHeader carries the admin API key, sent once per key-management action and never stored.
	ReauthHeader = "X-Arcana-Reauth"
)

// classifyRoute decides what a cookie may do on a route. It is an allowlist: anything not named
// here, including every exec, file, archive and exec-stream route, is routeDenied. The exec
// stream is a GET that runs code, so "safe method" is not a safe test.
func classifyRoute(method, path string) routeClass {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok || rest == "" {
		return routeDenied
	}
	segments := strings.Split(rest, "/")
	for _, s := range segments {
		if s == "" || len(s) > 256 {
			return routeDenied
		}
	}
	n := len(segments)
	switch method {
	case http.MethodGet, http.MethodHead:
		switch {
		case n == 1:
			switch segments[0] {
			case "overview", "events", "templates", "sandboxes", "snapshots", "audit", "keys":
				return routeRead
			}
		case n == 2 && segments[0] == "sandboxes", n == 2 && segments[0] == "snapshots":
			return routeRead
		case n == 2 && segments[0] == "sessions" && segments[1] == "current":
			return routeRead
		case n == 3 && segments[0] == "sandboxes" && (segments[2] == "execs" || segments[2] == "history"):
			return routeRead
		case n == 5 && segments[0] == "sandboxes" && segments[2] == "execs" && segments[4] == "output":
			return routeRead
		}
	case http.MethodPost:
		switch {
		case n == 1 && segments[0] == "sandboxes":
			return routeWrite
		case n == 1 && segments[0] == "keys":
			return routeKeyAdmin
		case n == 3 && segments[0] == "sandboxes" && (segments[2] == "keepalive" || segments[2] == "snapshots"):
			return routeWrite
		case n == 3 && segments[0] == "snapshots" && segments[2] == "fork":
			return routeWrite
		}
	case http.MethodDelete:
		switch {
		case n == 2 && (segments[0] == "sandboxes" || segments[0] == "snapshots"):
			return routeWrite
		case n == 2 && segments[0] == "keys":
			return routeKeyAdmin
		case n == 2 && segments[0] == "sessions" && segments[1] == "current":
			return routeLogout
		}
	}
	return routeDenied
}

// decideCookie applies FR-WD-5 as a pure function of the request's method, path and headers.
//
//   - Only the routes classifyRoute names are reachable with a cookie.
//   - Reads must prove same-origin: an Origin header equal to the request's own origin or listed
//     in allowed, or, without Origin, Sec-Fetch-Site: same-origin. An Origin that does not match is
//     rejected even when Sec-Fetch-Site claims same-origin.
//   - Every other request (writes, key management, logout) needs a matching Origin; Sec-Fetch-Site
//     is not accepted instead, because browsers always send Origin on those. curl-style requests
//     with neither header are rejected, as a cookie is only ever the browser's.
//
// The CSRF token and key re-entry that writes additionally need are checked after the session is
// verified, because they depend on it.
func decideCookie(method, path, origin, fetchSite, host string, allowed []string) (cookieVerdict, routeClass) {
	class := classifyRoute(method, path)
	if class == routeDenied {
		return cookieRouteForbidden, class
	}
	if origin != "" {
		if originMatches(origin, host, allowed) {
			return cookieAllowed, class
		}
		return cookieOriginForbidden, class
	}
	if class == routeRead && fetchSite == "same-origin" {
		return cookieAllowed, class
	}
	return cookieOriginForbidden, class
}

// csrfToken derives the session's synchronizer token from the stored token hash: unguessable
// without the session token's hash, stable for the life of the session so a reloaded page can
// fetch it again, and never stored separately.
func csrfToken(tokenHash []byte) string {
	mac := hmac.New(sha256.New, tokenHash)
	mac.Write([]byte("arcana-csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// csrfValid compares a presented token with the expected one in constant time.
func csrfValid(presented, expected string) bool {
	if len(presented) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// originMatches reports whether an Origin header names this server (by Host) or an allowed origin.
func originMatches(origin, host string, allowed []string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil || strings.EqualFold(origin, "null") {
		return false
	}
	if host != "" && strings.EqualFold(u.Host, host) {
		return true
	}
	canonical := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, a := range allowed {
		if strings.ToLower(a) == canonical {
			return true
		}
	}
	return false
}
