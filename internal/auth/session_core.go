package auth

import (
	"net/http"
	"net/url"
	"strings"
)

// cookieVerdict is the outcome of the cookie-authentication policy for one request.
type cookieVerdict uint8

const (
	cookieAllowed cookieVerdict = iota
	// cookieMethodForbidden: a cookie alone must never authorize a state-changing request (FR-WD-5).
	cookieMethodForbidden
	// cookieOriginForbidden: the request did not provably come from this server's own pages.
	cookieOriginForbidden
)

// logoutPath is the only non-safe request a session cookie may authorize.
const logoutPath = "/v1/sessions/current"

// decideCookie applies FR-WD-5 as a pure function of the request's method, path and headers.
//
//   - Safe methods (GET, HEAD) and DELETE of the current session are the only methods allowed.
//   - Every cookie request must prove same-origin: an Origin header equal to the request's own
//     origin or listed in allowed, or, for GET/HEAD without Origin, Sec-Fetch-Site: same-origin.
//     An Origin that does not match is rejected even when Sec-Fetch-Site claims same-origin.
//     curl-style requests with neither header are rejected, as a cookie is only ever the browser's.
func decideCookie(method, path, origin, fetchSite, host string, allowed []string) cookieVerdict {
	safe := method == http.MethodGet || method == http.MethodHead
	if !safe && (method != http.MethodDelete || path != logoutPath) {
		return cookieMethodForbidden
	}
	if origin != "" {
		if originMatches(origin, host, allowed) {
			return cookieAllowed
		}
		return cookieOriginForbidden
	}
	if safe && fetchSite == "same-origin" {
		return cookieAllowed
	}
	return cookieOriginForbidden
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
