// Package apierr defines the public API error contract.
package apierr

import (
	"context"
	"encoding/json"
	"net/http"
)

// Code is a stable machine-readable API error code.
type Code string

// Error is an API failure. Messages must be safe for the caller to see.
type Error struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Envelope is the JSON error response.
type Envelope struct {
	Error Error `json:"error"`
}

type requestIDKey struct{}

// WithRequestID attaches a server-generated request identifier.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request identifier, or an empty string outside HTTP handling.
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }

// Write writes an error envelope using its status mapping and request context.
func Write(w http.ResponseWriter, r *http.Request, e *Error) error {
	response := *e
	response.RequestID = RequestID(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Status())
	return json.NewEncoder(w).Encode(Envelope{Error: response})
}
