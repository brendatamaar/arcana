package apierr

import (
	"net/http"

	"github.com/brendatamaar/arcana/internal/ids"
)

// RequestIDs generates an ID for every request. Untrusted inbound IDs are ignored.
func RequestIDs(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := ids.New("req")
		if err != nil {
			_ = Write(w, r, NewInternal("request identifier unavailable"))
			return
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
