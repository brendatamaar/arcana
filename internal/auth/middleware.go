package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/brendatamaar/arcana/internal/apierr"
)

type principalKey struct{}

// FromContext returns the identity established by authentication middleware.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Middleware authenticates every request except the public health endpoint.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		headers := r.Header.Values("Authorization")
		if len(headers) == 0 && s.sessions != nil {
			// An explicit Authorization header always wins, so a cross-site page (which cannot set
			// one) can only ever act through the cookie, and the cookie is restricted below.
			if cookies := r.CookiesNamed(SessionCookie); len(cookies) > 0 {
				s.serveCookie(w, r, next, cookies)
				return
			}
		}
		if len(headers) != 1 || len(headers[0]) > 48 {
			_ = apierr.Write(w, r, apierr.NewUnauthorized("valid bearer key required"))
			return
		}
		scheme, raw, ok := strings.Cut(headers[0], " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") {
			_ = apierr.Write(w, r, apierr.NewUnauthorized("valid bearer key required"))
			return
		}
		principal, err := s.Verify(r.Context(), raw)
		if err != nil {
			var invalid InvalidCredentials
			if errors.As(err, &invalid) {
				_ = apierr.Write(w, r, apierr.NewUnauthorized("valid bearer key required"))
			} else {
				_ = apierr.Write(w, r, apierr.NewInternal("authentication unavailable"))
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	})
}

// RequireAdmin restricts a handler after authentication middleware.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok {
			_ = apierr.Write(w, r, apierr.NewUnauthorized("authentication required"))
			return
		}
		if !p.Admin {
			_ = apierr.Write(w, r, apierr.NewForbidden("admin key required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveCookie authenticates a dashboard session cookie under FR-WD-5: only the dashboard's own
// routes, only for requests that provably originate from this server's own pages, and, for writes,
// only with the session's CSRF token (and, for key management, the admin key re-entered).
func (s *Service) serveCookie(w http.ResponseWriter, r *http.Request, next http.Handler, cookies []*http.Cookie) {
	// Several cookies with one name is cookie tossing or a bug, never a legitimate browser state.
	if len(cookies) != 1 {
		_ = apierr.Write(w, r, apierr.NewUnauthorized("valid session required"))
		return
	}
	verdict, class := decideCookie(r.Method, r.URL.Path, r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Site"), r.Host, s.sessions.AllowedOrigins)
	switch verdict {
	case cookieRouteForbidden:
		_ = apierr.Write(w, r, apierr.NewForbidden("session cookies cannot authorize this request; use an API key"))
		return
	case cookieOriginForbidden:
		_ = apierr.Write(w, r, apierr.NewForbidden("cross-origin or origin-less request rejected for cookie authentication"))
		return
	case cookieAllowed:
	}
	principal, session, err := s.verifySession(r.Context(), cookies[0].Value)
	if err != nil {
		var invalid InvalidCredentials
		if errors.As(err, &invalid) {
			_ = apierr.Write(w, r, apierr.NewUnauthorized("valid session required"))
		} else {
			_ = apierr.Write(w, r, apierr.NewInternal("authentication unavailable"))
		}
		return
	}
	if class == routeWrite || class == routeKeyAdmin {
		if !csrfValid(r.Header.Get(CSRFHeader), session.CSRFToken) {
			_ = apierr.Write(w, r, apierr.NewForbidden("missing or invalid CSRF token"))
			return
		}
	}
	if class == routeKeyAdmin && !s.reauthenticated(r, principal) {
		_ = apierr.Write(w, r, apierr.NewForbidden("re-enter your admin API key to manage keys"))
		return
	}
	if class == routeWrite || class == routeKeyAdmin {
		s.audit.Record("session.write", principal.ID, "", map[string]any{"session_id": session.ID, "method": r.Method, "path": r.URL.Path})
	}
	ctx := context.WithValue(r.Context(), principalKey{}, principal)
	ctx = context.WithValue(ctx, sessionContextKey{}, session)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// reauthenticated reports whether the request re-entered the admin key that owns the session.
// Re-entry stops a hijacked session from minting or revoking keys on its own.
func (s *Service) reauthenticated(r *http.Request, principal Principal) bool {
	raw := r.Header.Get(ReauthHeader)
	if raw == "" || len(raw) > 64 || !principal.Admin {
		return false
	}
	again, err := s.Verify(r.Context(), raw)
	return err == nil && again.ID == principal.ID && again.Admin
}
