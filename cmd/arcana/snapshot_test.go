package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const snapshotJSON = `{"id":"snp_test","source_sandbox_id":"sbx_test","template":"python","resources":{},"network":{"mode":"none"},"env_keys":[],"size_bytes":42,"sha256":"` + "00000000000000000000000000000000000000000000000000000000000000ab" + `","created_at":"2026-11-12T00:00:00Z"}`

func snapshotServer(t *testing.T, failures string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/sandboxes/sbx_test/snapshots":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, snapshotJSON)
		case r.Method == "GET" && r.URL.Path == "/v1/snapshots":
			_, _ = io.WriteString(w, `{"items":[`+snapshotJSON+`],"next_cursor":"snp_test"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/snapshots/snp_test":
			_, _ = io.WriteString(w, snapshotJSON)
		case r.Method == "DELETE" && r.URL.Path == "/v1/snapshots/snp_test":
			w.WriteHeader(204)
		case r.Method == "POST" && r.URL.Path == "/v1/snapshots/snp_test/fork":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"items":[{"id":"sbx_fork","state":"running","template":"python"}],"failures":[`+failures+`]}`)
		case r.Method == "POST" && r.URL.Path == "/v1/sandboxes":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"sbx_new","state":"running","template":"python"}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"code":"snapshot_not_found","message":"snapshot not found","request_id":"req_test"}}`)
		}
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func TestSnapshotCLI_CreateListRemoveAndJSON(t *testing.T) {
	server, seen := snapshotServer(t, "")
	for _, args := range [][]string{{"snapshot", "create", "sbx_test"}, {"snapshot", "list"}, {"snapshot", "rm", "snp_test"}} {
		out, _, err := runCLI(server.URL, args...)
		if err != nil || !strings.Contains(out, "snp_test") {
			t.Fatal(args, out, err)
		}
	}
	out, _, err := runCLI(server.URL, "snapshot", "list")
	if err != nil || !strings.Contains(out, "SIZE_BYTES") || !strings.Contains(out, "python") {
		t.Fatal(out, err)
	}
	_, stderr, err := runCLI(server.URL, "snapshot", "list")
	if err != nil || !strings.Contains(stderr, "next cursor: snp_test") {
		t.Fatal(stderr, err)
	}
	out, _, err = runCLI(server.URL, "--json", "snapshot", "create", "sbx_test")
	var row struct {
		ID     string `json:"id"`
		Sha256 string `json:"sha256"`
	}
	if err != nil || json.Unmarshal([]byte(out), &row) != nil || row.ID != "snp_test" || len(row.Sha256) != 64 {
		t.Fatal(out, err)
	}
	if _, _, err = runCLI(server.URL, "snapshot", "rm", "snp_missing"); err == nil {
		t.Fatal("API error was swallowed")
	}
	if len(*seen) == 0 {
		t.Fatal("no requests")
	}
}

func TestForkCLI_CountOverridesAndPartialFailureExitsNonZero(t *testing.T) {
	server, seen := snapshotServer(t, "")
	out, _, err := runCLI(server.URL, "fork", "snp_test", "--count", "3", "--ttl", "60")
	if err != nil || !strings.Contains(out, "sbx_fork") {
		t.Fatal(out, err)
	}
	last := (*seen)[len(*seen)-1]
	if !strings.Contains(last, `"count":3`) || !strings.Contains(last, `"ttl_seconds":60`) || strings.Contains(last, "idle_timeout") {
		t.Fatalf("request: %s", last)
	}
	failing, _ := snapshotServer(t, `{"index":1,"code":"quota_exceeded","message":"sandbox quota exceeded"}`)
	out, stderr, err := runCLI(failing.URL, "fork", "snp_test", "--count", "2")
	if err == nil || !strings.Contains(err.Error(), "1 of 2") || !strings.Contains(out, "sbx_fork") || !strings.Contains(stderr, "fork 1 failed: quota_exceeded") {
		t.Fatalf("partial failure must print what was created and fail: %q %q %v", out, stderr, err)
	}
}

func TestSandboxCreateCLI_FromSnapshotDefaultsTemplateToTheSnapshots(t *testing.T) {
	server, seen := snapshotServer(t, "")
	if _, _, err := runCLI(server.URL, "sandbox", "create", "--from-snapshot", "snp_test"); err != nil {
		t.Fatal(err)
	}
	last := (*seen)[len(*seen)-1]
	if !strings.Contains(last, `"template":"python"`) || !strings.Contains(last, `"from_snapshot":"snp_test"`) {
		t.Fatalf("request: %s", last)
	}
	if _, _, err := runCLI(server.URL, "sandbox", "create", "--from-snapshot", "snp_test", "--template", "base"); err != nil {
		t.Fatal(err)
	}
	last = (*seen)[len(*seen)-1]
	if !strings.Contains(last, `"template":"base"`) {
		t.Fatalf("explicit template overridden: %s", last)
	}
}
