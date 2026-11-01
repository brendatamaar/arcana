package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestErrors_StatusAndEnvelope(t *testing.T) {
	cases := []struct {
		newError func(string) *Error
		code     string
		status   int
	}{
		{NewInvalidRequest, "invalid_request", 400},
		{NewInvalidPath, "invalid_path", 400},
		{NewInvalidTemplate, "invalid_template", 400},
		{NewUnauthorized, "unauthorized", 401},
		{NewForbidden, "forbidden", 403},
		{NewSandboxNotFound, "sandbox_not_found", 404},
		{NewSnapshotNotFound, "snapshot_not_found", 404},
		{NewFileNotFound, "file_not_found", 404},
		{NewKeyNotFound, "key_not_found", 404},
		{NewSandboxNotRunning, "sandbox_not_running", 409},
		{NewExecLimitReached, "exec_limit_reached", 409},
		{NewPayloadTooLarge, "payload_too_large", 413},
		{NewQuotaExceeded, "quota_exceeded", 429},
		{NewRateLimited, "rate_limited", 429},
		{NewInternal, "internal", 500},
		{NewRuntimeUnavailable, "runtime_unavailable", 503},
		{NewShuttingDown, "shutting_down", 503},
	}
	for _, tt := range cases {
		t.Run(tt.code, func(t *testing.T) {
			e := tt.newError("safe message")
			r := httptest.NewRequest("GET", "/", nil)
			r = r.WithContext(WithRequestID(r.Context(), "req_test"))
			w := httptest.NewRecorder()
			if err := Write(w, r, e); err != nil {
				t.Fatal(err)
			}
			if w.Code != tt.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("response: %v", w.Result())
			}
			var body struct {
				Error struct {
					Code, Message string
					RequestID     string `json:"request_id"`
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tt.code || body.Error.Message != "safe message" || body.Error.RequestID != "req_test" {
				t.Fatalf("body: %s", w.Body.String())
			}
			if e.RequestID != "" || e.Error() != tt.code+": safe message" {
				t.Fatalf("error mutated: %+v", e)
			}
		})
	}
	if (&Error{Code: "unknown"}).Status() != 500 {
		t.Fatal("unknown error did not fail closed")
	}
}

func TestRequestIDs_IgnoreUntrustedHeader(t *testing.T) {
	pattern := regexp.MustCompile(`^req_[a-z2-7]{20}$`)
	handler := RequestIDs(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := RequestID(r.Context()); !pattern.MatchString(id) || id != w.Header().Get("X-Request-ID") {
			t.Fatalf("invalid request ID: %q", id)
		}
		if err := Write(w, r, NewUnauthorized("unauthorized")); err != nil {
			t.Fatal(err)
		}
	}))
	var previous string
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Request-ID", "untrusted")
		handler.ServeHTTP(w, r)
		if w.Header().Get("X-Request-ID") == previous {
			t.Fatal("reused ID")
		}
		previous = w.Header().Get("X-Request-ID")
	}
}
