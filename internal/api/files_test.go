package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

type deadlineResponse struct {
	*httptest.ResponseRecorder
	calls int
}

func (w *deadlineResponse) SetReadDeadline(time.Time) error { w.calls++; return nil }

func TestFileBody_ClosePreservesCompletedRequestAndInterruptsPendingRead(t *testing.T) {
	for _, consume := range []bool{false, true} {
		writer := &deadlineResponse{ResponseRecorder: httptest.NewRecorder()}
		body := &fileBody{ReadCloser: io.NopCloser(strings.NewReader("input")), controller: http.NewResponseController(writer), now: time.Now}
		if consume {
			if _, err := io.Copy(io.Discard, body); err != nil {
				t.Fatal(err)
			}
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
		want := 1
		if consume {
			want = 0
		}
		if writer.calls != want {
			t.Fatalf("consume=%t: deadline calls=%d", consume, writer.calls)
		}
	}
}

type filesDriver struct {
	*fake.Driver
	mu sync.Mutex
}

func (d *filesDriver) Exec(ctx context.Context, id string, r runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := fake.Script{}
	if r.Cmd[1] == "status" {
		script.Stdout, _ = json.Marshal(protocol.Status{ID: r.Cmd[3], CleanupComplete: true})
	} else if r.Cmd[1] == "run" {
		for i, arg := range r.Cmd {
			if arg == "--" && i+2 < len(r.Cmd) {
				switch r.Cmd[i+2] {
				case "read":
					script.Stdout = []byte("{\"size_bytes\":3}\n\x00\xffx")
				case "ls":
					script.Stdout = []byte(`{"items":[]}`)
				case "-czf":
					script.Stdout = []byte("opaque-gzip")
				}
				break
			}
		}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	return d.Driver.Exec(ctx, id, r)
}

func TestFilesEndpoints_AuthOwnershipPathsAndOpaqueResponses(t *testing.T) {
	h, _, owner, other := lifecycleFixture(t, &filesDriver{Driver: fake.New()})
	created := request(h, "POST", "/v1/sandboxes", owner, `{"template":"base"}`)
	var row gen.Sandbox
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	base := "/v1/sandboxes/" + row.Id
	call := func(method, target, key, media string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(""))
		r.Header.Set("Content-Type", media)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, route := range []struct {
		method, path, media string
		status              int
	}{
		{"PUT", "/files", "application/octet-stream", 204},
		{"GET", "/files", "", 200},
		{"DELETE", "/files", "", 204},
		{"GET", "/files/list", "", 200},
		{"PUT", "/files/archive", "application/gzip", 204},
		{"GET", "/files/archive", "", 200},
	} {
		target := base + route.path + "?path=/workspace/data"
		if w := call(route.method, target, "", route.media); w.Code != 401 {
			t.Fatal(route, w.Code)
		}
		if w := call(route.method, target, other, route.media); w.Code != 404 {
			t.Fatal("foreign resource", route, w.Code, w.Body.String())
		}
		for _, path := range []string{"relative", "%2Fworkspace%2F..%2Fetc%2Fpasswd", "%2Fetc%2Fpasswd", "%2Fworkspace%2Fa%00b"} {
			w := call(route.method, base+route.path+"?path="+path, owner, route.media)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_path") {
				t.Fatal(path, w.Code, w.Body.String())
			}
		}
		w := call(route.method, target, owner, route.media)
		if w.Code != route.status {
			t.Fatal(route, w.Code, w.Body.String())
		}
		if route.method == "GET" && route.path == "/files" && (w.Body.String() != "\x00\xffx" || w.Header().Get("Content-Length") != "3") {
			t.Fatal("changed binary response")
		}
	}
	if w := call("PUT", base+"/files?path=/workspace/a", owner, "text/plain"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := call("GET", base+"/files/list?path=/workspace&depth=101", owner, ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestFiles_LateStreamFailureAbortsHTTPResponse(t *testing.T) {
	s := &Server{}
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("partial binary response was not aborted")
		}
	}()
	s.fileError(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil), &fileResponse{started: true}, context.Canceled)
}
