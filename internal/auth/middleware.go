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
