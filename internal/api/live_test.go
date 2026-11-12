package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

// outputDriver answers helper protocol calls and gives user commands scripted output.
type outputDriver struct {
	*fake.Driver
	mu     sync.Mutex
	stdout string
	gate   chan struct{} // when set, `run` output and exit wait for it
}

func (d *outputDriver) Exec(ctx context.Context, id string, r runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := fake.Script{}
	switch r.Cmd[1] {
	case "status":
		script.Stdout, _ = json.Marshal(protocol.Status{ID: r.Cmd[3], CleanupComplete: true})
	case "run":
		script.Stdout = []byte(d.stdout)
		script.Stderr = []byte("warn")
		if d.gate != nil {
			script.OutputGate, script.ExitGate = d.gate, d.gate
		}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	return d.Driver.Exec(ctx, id, r)
}

// liveSubscriberCap is the event bus subscriber limit used by the live test environment.
const liveSubscriberCap = 6

type liveEnv struct {
	server              *httptest.Server
	admin, owner, other string
	driver              *outputDriver
	bus                 *events.Bus
	service             *orchestrator.Service
	keys                *auth.Service
}

func newLiveEnv(t *testing.T, sessions bool, mutate func(*config.Config)) *liveEnv {
	t.Helper()
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keys := auth.New(database, auth.SystemClock{})
	if sessions {
		keys.WithSessions(auth.SessionPolicy{TTL: time.Hour, AllowedOrigins: []string{"https://dash.example"}})
	}
	secrets := make([]string, 3)
	for i := range secrets {
		_, secrets[i], err = keys.Create(context.Background(), "k", i == 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	if mutate != nil {
		mutate(&cfg)
	}
	driver := &outputDriver{Driver: fake.New(), stdout: "hello"}
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	bus := events.NewBus(orchestrator.SystemClock{}, cfg.UI.EventBuffer, liveSubscriberCap)
	service.WithEvents(bus)
	handler := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service, Options{Version: "v-test", HeartbeatInterval: 40 * time.Millisecond, MaxFork: 3})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &liveEnv{server: server, admin: secrets[0], owner: secrets[1], other: secrets[2], driver: driver, bus: bus, service: service, keys: keys}
}

func (e *liveEnv) do(t *testing.T, method, path, key, body string, headers map[string]string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (e *liveEnv) login(t *testing.T, key string) *http.Cookie {
	t.Helper()
	resp := e.do(t, "POST", "/v1/sessions", key, "", nil)
	if resp.StatusCode != 201 {
		t.Fatalf("login: %d %s", resp.StatusCode, readBody(t, resp))
	}
	_ = readBody(t, resp)
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookie {
			return &http.Cookie{Name: c.Name, Value: c.Value} // #nosec G124 -- request cookie in a test
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func (e *liveEnv) createSandbox(t *testing.T, key string) string {
	t.Helper()
	resp := e.do(t, "POST", "/v1/sandboxes", key, `{"template":"base"}`, nil)
	var row gen.Sandbox
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, readBody(t, resp))
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &row); err != nil {
		t.Fatal(err)
	}
	return row.Id
}

type sseFrame struct{ id, event, data, comment string }

// frames reads SSE frames from a response in the background.
func frames(t *testing.T, resp *http.Response) <-chan sseFrame {
	t.Helper()
	// Registered after the server's own cleanup, so it runs first: httptest.Server.Close blocks
	// until every open stream has ended.
	t.Cleanup(func() { _ = resp.Body.Close() })
	out := make(chan sseFrame, 256)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		var f sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if f != (sseFrame{}) {
					out <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				f.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.data = line[6:]
			case strings.HasPrefix(line, ": "):
				f.comment = line[2:]
			}
		}
	}()
	return out
}

func next(t *testing.T, ch <-chan sseFrame, want string) sseFrame {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("stream ended while waiting for %q", want)
			}
			if f.comment != "" {
				if want == "keep-alive" {
					return f
				}
				continue
			}
			if want == "any" || f.event == want {
				return f
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestSessions_CookieAttributesLoginLogoutAndForbiddenMutations(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	resp := env.do(t, "POST", "/v1/sessions", env.owner, "", nil)
	if resp.StatusCode != 201 {
		t.Fatal(resp.StatusCode)
	}
	var body gen.Session
	if err := json.Unmarshal([]byte(readBody(t, resp)), &body); err != nil || body.Id == "" {
		t.Fatal(err)
	}
	cookie := resp.Cookies()[0]
	if cookie.Name != auth.SessionCookie || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 3600 || cookie.Secure {
		t.Fatalf("plain HTTP to loopback: %+v", cookie)
	}
	if strings.Contains(resp.Header.Get("Set-Cookie"), env.owner) || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("the API key must never reach the cookie")
	}
	secure := env.do(t, "POST", "/v1/sessions", env.owner, "", map[string]string{"X-Forwarded-Proto": "https"})
	if c := secure.Cookies()[0]; !c.Secure {
		t.Fatal("behind TLS the cookie must be Secure")
	}
	_ = readBody(t, secure)
	if got := (&http.Request{Host: "arcana.example:7070"}); secureCookie(got) != true {
		t.Fatal("a non-loopback host must get Secure cookies")
	}
	for host, want := range map[string]bool{"localhost:7070": false, "127.0.0.1:7070": false, "[::1]:7070": false, "10.0.0.5:7070": true} {
		if secureCookie(&http.Request{Host: host}) != want {
			t.Errorf("%s: want secure=%t", host, want)
		}
	}
	// Login needs an API key: a cookie cannot mint sessions, and no credentials are refused.
	same := map[string]string{"Origin": env.server.URL}
	if got := env.do(t, "POST", "/v1/sessions", "", "", same, &http.Cookie{Name: auth.SessionCookie, Value: cookie.Value}); got.StatusCode != 403 { // #nosec G124 -- request cookie in a test
		t.Fatalf("cookie created a session: %d", got.StatusCode)
	}
	if got := env.do(t, "POST", "/v1/sessions", "", "", nil); got.StatusCode != 401 {
		t.Fatal(got.StatusCode)
	}
	// Reading works with the cookie; logout needs Origin and kills the session.
	read := map[string]string{"Sec-Fetch-Site": "same-origin"}
	if got := env.do(t, "GET", "/v1/sandboxes", "", "", read, cookie); got.StatusCode != 200 {
		t.Fatal(got.StatusCode)
	}
	if got := env.do(t, "DELETE", "/v1/sessions/current", "", "", nil, cookie); got.StatusCode != 403 {
		t.Fatalf("logout without Origin: %d", got.StatusCode)
	}
	out := env.do(t, "DELETE", "/v1/sessions/current", "", "", same, cookie)
	if out.StatusCode != 204 || out.Cookies()[0].MaxAge >= 0 || out.Cookies()[0].Value != "" {
		t.Fatalf("logout: %d %+v", out.StatusCode, out.Cookies())
	}
	if got := env.do(t, "GET", "/v1/sandboxes", "", "", read, cookie); got.StatusCode != 401 {
		t.Fatal("a logged-out session still works")
	}
	// Disabled sessions refuse login.
	off := newLiveEnv(t, false, nil)
	if got := off.do(t, "POST", "/v1/sessions", off.owner, "", nil); got.StatusCode != 403 {
		t.Fatal(got.StatusCode)
	}
}

func TestESC22_CookieOnlyRequestsCannotMutate(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	id := env.createSandbox(t, env.owner)
	cookie := env.login(t, env.owner)
	headers := map[string]string{"Origin": env.server.URL, "Sec-Fetch-Site": "same-origin"}
	for _, call := range [][3]string{
		{"POST", "/v1/sandboxes", `{"template":"base"}`},
		{"POST", "/v1/sandboxes/" + id + "/exec", `{"cmd":["ls"]}`},
		{"POST", "/v1/sandboxes/" + id + "/snapshots", ""},
		{"POST", "/v1/sandboxes/" + id + "/keepalive", ""},
		{"PUT", "/v1/sandboxes/" + id + "/files?path=/workspace/a", "x"},
		{"DELETE", "/v1/sandboxes/" + id, ""},
		{"POST", "/v1/keys", `{"name":"x"}`},
		{"DELETE", "/v1/keys/key_x", ""},
	} {
		if got := env.do(t, call[0], call[1], "", call[2], headers, cookie); got.StatusCode != 403 {
			t.Errorf("%s %s with a cookie: %d", call[0], call[1], got.StatusCode)
		}
	}
	if rows, err := env.service.List(context.Background(), auth.Principal{ID: "x", Admin: true}, orchestrator.ListRequest{Limit: 100}); err != nil || len(rows) != 1 || rows[0].State != "running" {
		t.Fatalf("a refused request still changed state: %+v %v", rows, err)
	}
}

func TestESC23_CrossOriginStreamsAreRejected(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	id := env.createSandbox(t, env.owner)
	if got := env.do(t, "POST", "/v1/sandboxes/"+id+"/exec", env.owner, `{"cmd":["ls"]}`, nil); got.StatusCode != 200 {
		t.Fatal(got.StatusCode)
	}
	rows, _ := env.service.ListExecs(context.Background(), auth.Principal{ID: "x", Admin: true}, id, 10, "")
	cookie := env.login(t, env.owner)
	for _, path := range []string{"/v1/events", "/v1/sandboxes/" + id + "/execs/" + rows[0].ID + "/output"} {
		for name, headers := range map[string]map[string]string{
			"foreign Origin":   {"Origin": "https://evil.example"},
			"cross-site fetch": {"Sec-Fetch-Site": "cross-site"},
			"same-site fetch":  {"Sec-Fetch-Site": "same-site"},
			"no provenance":    nil,
			"null Origin":      {"Origin": "null"},
		} {
			if got := env.do(t, "GET", path, "", "", headers, cookie); got.StatusCode != 403 {
				t.Errorf("%s with %s: %d", path, name, got.StatusCode)
			}
		}
		ok := env.do(t, "GET", path, "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie)
		if ok.StatusCode != 200 {
			t.Errorf("%s same-origin: %d", path, ok.StatusCode)
		}
		_ = ok.Body.Close()
	}
	// An allowed extra origin works; the same Origin is useless without a valid session.
	if got := env.do(t, "GET", "/v1/overview", "", "", map[string]string{"Origin": "https://dash.example"}, cookie); got.StatusCode != 200 {
		t.Fatal(got.StatusCode)
	}
}

func TestEvents_StreamFiltersResumesAndResets(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	owner := env.do(t, "GET", "/v1/events", env.owner, "", nil)
	if owner.StatusCode != 200 || owner.Header.Get("Content-Type") != "text/event-stream" || owner.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("%d %v", owner.StatusCode, owner.Header)
	}
	ownerFrames := frames(t, owner)
	other := env.do(t, "GET", "/v1/events", env.other, "", nil)
	otherFrames := frames(t, other)
	admin := env.do(t, "GET", "/v1/events", env.admin, "", nil)
	adminFrames := frames(t, admin)
	id := env.createSandbox(t, env.owner)
	created := next(t, ownerFrames, "sandbox.created")
	var payload struct {
		Type      string         `json:"type"`
		SandboxID string         `json:"sandbox_id"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(created.data), &payload); err != nil || payload.SandboxID != id || payload.Data["template"] != "base" || created.id == "" {
		t.Fatalf("%+v %v", payload, err)
	}
	if strings.Contains(created.data, "owner") || strings.Contains(created.data, env.owner) {
		t.Fatalf("event leaked owner identity: %s", created.data)
	}
	next(t, adminFrames, "sandbox.created")
	// The other key sees nothing of the owner's sandbox, only keep-alives.
	next(t, otherFrames, "keep-alive")
	select {
	case f := <-otherFrames:
		if f.event != "" {
			t.Fatalf("another key received %+v", f)
		}
	default:
	}
	// Heartbeat comments keep idle streams alive.
	next(t, ownerFrames, "keep-alive")
	// Resume: events after Last-Event-ID are replayed, none duplicated.
	if got := env.do(t, "DELETE", "/v1/sandboxes/"+id, env.owner, "", nil); got.StatusCode != 204 {
		t.Fatal(got.StatusCode)
	}
	resumed := frames(t, env.do(t, "GET", "/v1/events", env.owner, "", map[string]string{"Last-Event-ID": created.id}))
	var replayed []string
	for len(replayed) < 3 {
		f := next(t, resumed, "any")
		replayed = append(replayed, f.event)
	}
	if strings.Join(replayed, ",") != "sandbox.state,sandbox.state,sandbox.destroyed" {
		t.Fatalf("replay: %v", replayed)
	}
	// An unknown (future) ID means a restarted server: the client must refetch.
	stale := frames(t, env.do(t, "GET", "/v1/events", env.owner, "", map[string]string{"Last-Event-ID": "999999"}))
	next(t, stale, "reset")
	if got := env.do(t, "GET", "/v1/events", env.owner, "", map[string]string{"Last-Event-ID": "abc"}); got.StatusCode != 400 {
		t.Fatalf("bad Last-Event-ID: %d", got.StatusCode)
	}
	if got := env.do(t, "GET", "/v1/events", "", "", nil); got.StatusCode != 401 {
		t.Fatal(got.StatusCode)
	}
}

func TestEvents_SubscriberCapIsEnforcedAndReleased(t *testing.T) {
	env := newLiveEnv(t, false, nil)
	var open []*http.Response
	for i := 0; i < liveSubscriberCap; i++ {
		resp := env.do(t, "GET", "/v1/events", env.owner, "", nil)
		if resp.StatusCode != 200 {
			t.Fatalf("stream %d: %d", i, resp.StatusCode)
		}
		open = append(open, resp)
	}
	if got := env.do(t, "GET", "/v1/events", env.owner, "", nil); got.StatusCode != 429 {
		t.Fatalf("cap not enforced: %d", got.StatusCode)
	}
	for _, resp := range open {
		_ = resp.Body.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for env.bus.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if env.bus.Subscribers() != 0 {
		t.Fatalf("closed streams still counted: %d", env.bus.Subscribers())
	}
}

func TestExecOutput_ReplaysThenEndsAndEnforcesOwnership(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	id := env.createSandbox(t, env.owner)
	resp := env.do(t, "POST", "/v1/sandboxes/"+id+"/exec", env.owner, `{"shell":"echo hello"}`, nil)
	var result gen.ExecResult
	if err := json.Unmarshal([]byte(readBody(t, resp)), &result); err != nil || result.ExitCode != 0 {
		t.Fatal(err)
	}
	path := "/v1/sandboxes/" + id + "/execs/" + result.ExecId + "/output"
	stream := frames(t, env.do(t, "GET", path, env.owner, "", nil))
	var out, errText string
	for {
		f := next(t, stream, "any")
		if f.event == "output" {
			var o struct {
				Stream string `json:"stream"`
				Data   string `json:"data"`
			}
			_ = json.Unmarshal([]byte(f.data), &o)
			raw, _ := base64.StdEncoding.DecodeString(o.Data)
			if o.Stream == "stderr" {
				errText += string(raw)
			} else {
				out += string(raw)
			}
			continue
		}
		if f.event != "end" {
			t.Fatalf("unexpected %+v", f)
		}
		var end endPayload
		_ = json.Unmarshal([]byte(f.data), &end)
		if end.ExitCode == nil || *end.ExitCode != 0 || end.Expired {
			t.Fatalf("%+v", end)
		}
		break
	}
	if out != "hello" || errText != "warn" {
		t.Fatalf("%q %q", out, errText)
	}
	if got := env.do(t, "GET", path, env.other, "", nil); got.StatusCode != 404 {
		t.Fatalf("foreign key: %d", got.StatusCode)
	}
	for _, p := range []string{"/v1/sandboxes/" + id + "/execs/exe_nope/output", "/v1/sandboxes/sbx_nope/execs/" + result.ExecId + "/output"} {
		if got := env.do(t, "GET", p, env.owner, "", nil); got.StatusCode != 404 {
			t.Fatalf("%s: %d", p, got.StatusCode)
		}
	}
	if got := env.do(t, "GET", path, "", "", nil); got.StatusCode != 401 {
		t.Fatal(got.StatusCode)
	}
}

func TestExecOutput_LiveFollowReaderCapAndExpiry(t *testing.T) {
	env := newLiveEnv(t, false, func(c *config.Config) {
		c.Limits.Exec.MaxTailSubscribers = 1
		c.Limits.Exec.TailRetentionSeconds = 1
	})
	id := env.createSandbox(t, env.owner)
	gate := make(chan struct{})
	env.driver.mu.Lock()
	env.driver.gate = gate
	env.driver.mu.Unlock()
	done := make(chan *http.Response, 1)
	go func() { done <- env.do(t, "POST", "/v1/sandboxes/"+id+"/exec", env.owner, `{"shell":"slow"}`, nil) }()
	var execID string
	deadline := time.Now().Add(5 * time.Second)
	for execID == "" && time.Now().Before(deadline) {
		if rows, err := env.service.ListExecs(context.Background(), auth.Principal{ID: "x", Admin: true}, id, 5, ""); err == nil && len(rows) > 0 {
			execID = rows[0].ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	if execID == "" {
		t.Fatal("exec did not start")
	}
	path := "/v1/sandboxes/" + id + "/execs/" + execID + "/output"
	first := env.do(t, "GET", path, env.owner, "", nil)
	if first.StatusCode != 200 {
		t.Fatal(first.StatusCode)
	}
	live := frames(t, first)
	next(t, live, "keep-alive") // nothing yet: the follower waits, with heartbeats
	if got := env.do(t, "GET", path, env.owner, "", nil); got.StatusCode != 429 {
		t.Fatalf("second reader beyond max_tail_subscribers: %d", got.StatusCode)
	}
	close(gate) // let the command finish
	if resp := <-done; resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	next(t, live, "output")
	next(t, live, "output")
	end := next(t, live, "end")
	if !strings.Contains(end.data, `"exit_code":0`) {
		t.Fatal(end.data)
	}
	// After the retention period the ring is gone, but the stream still ends cleanly with the record.
	time.Sleep(1200 * time.Millisecond)
	expired := frames(t, env.do(t, "GET", path, env.owner, "", nil))
	gone := next(t, expired, "end")
	var payload endPayload
	_ = json.Unmarshal([]byte(gone.data), &payload)
	if !payload.Expired || payload.ExitCode == nil || *payload.ExitCode != 0 {
		t.Fatalf("%+v", payload)
	}
}

func TestExecHistoryAndOverview_OwnershipPaginationAndCookieReads(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	id := env.createSandbox(t, env.owner)
	for i := 0; i < 3; i++ {
		time.Sleep(5 * time.Millisecond)
		if got := env.do(t, "POST", "/v1/sandboxes/"+id+"/exec", env.owner, `{"cmd":["echo","a b"]}`, nil); got.StatusCode != 200 {
			t.Fatal(got.StatusCode)
		}
	}
	env.do(t, "POST", "/v1/sandboxes/"+id+"/exec", env.owner, `{"shell":"echo hi"}`, nil)
	var page gen.ExecList
	resp := env.do(t, "GET", "/v1/sandboxes/"+id+"/execs?limit=3", env.owner, "", nil)
	if err := json.Unmarshal([]byte(readBody(t, resp)), &page); err != nil || len(page.Items) != 3 || page.NextCursor == nil {
		t.Fatalf("%+v %v", page, err)
	}
	if page.Items[0].Shell == nil || *page.Items[0].Shell != "echo hi" || page.Items[0].Cmd != nil {
		t.Fatalf("shell exec: %+v", page.Items[0])
	}
	if page.Items[1].Cmd == nil || (*page.Items[1].Cmd)[1] != "a b" || page.Items[1].ExitCode == nil || *page.Items[1].ExitCode != 0 {
		t.Fatalf("argv exec: %+v", page.Items[1])
	}
	var rest gen.ExecList
	resp = env.do(t, "GET", "/v1/sandboxes/"+id+"/execs?cursor="+*page.NextCursor, env.owner, "", nil)
	if err := json.Unmarshal([]byte(readBody(t, resp)), &rest); err != nil || len(rest.Items) != 1 {
		t.Fatalf("%+v %v", rest, err)
	}
	if got := env.do(t, "GET", "/v1/sandboxes/"+id+"/execs", env.other, "", nil); got.StatusCode != 404 {
		t.Fatal(got.StatusCode)
	}
	if got := env.do(t, "GET", "/v1/sandboxes/"+id+"/execs?limit=0", env.owner, "", nil); got.StatusCode != 400 {
		t.Fatal(got.StatusCode)
	}
	cookie := env.login(t, env.owner)
	var overview gen.Overview
	resp = env.do(t, "GET", "/v1/overview", "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie)
	if err := json.Unmarshal([]byte(readBody(t, resp)), &overview); err != nil || overview.Version != "v-test" || overview.Runtime == "" || len(overview.SandboxesByTemplate) != 1 || overview.SandboxesByTemplate[0].Count != 1 || len(overview.Pool) != 0 {
		t.Fatalf("%+v %v", overview, err)
	}
	if got := env.do(t, "GET", "/v1/overview", "", "", nil); got.StatusCode != 401 {
		t.Fatal(got.StatusCode)
	}
}

func TestSandboxHistory_OrderedReasonsAndOwnership(t *testing.T) {
	env := newLiveEnv(t, true, nil)
	id := env.createSandbox(t, env.owner)
	if got := env.do(t, "DELETE", "/v1/sandboxes/"+id, env.owner, "", nil); got.StatusCode != 204 {
		t.Fatal(got.StatusCode)
	}
	var history gen.StateHistory
	resp := env.do(t, "GET", "/v1/sandboxes/"+id+"/history", env.owner, "", nil)
	if err := json.Unmarshal([]byte(readBody(t, resp)), &history); err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, item := range history.Items {
		states = append(states, string(item.State))
	}
	if strings.Join(states, ",") != "creating,running,destroying,destroyed" {
		t.Fatalf("%v", states)
	}
	last := history.Items[len(history.Items)-1]
	if last.Reason == nil || *last.Reason != "requested" {
		t.Fatalf("end reason missing: %+v", last)
	}
	if got := env.do(t, "GET", "/v1/sandboxes/"+id+"/history", env.other, "", nil); got.StatusCode != 404 {
		t.Fatalf("foreign key: %d", got.StatusCode)
	}
	cookie := env.login(t, env.owner)
	if got := env.do(t, "GET", "/v1/sandboxes/"+id+"/history", "", "", map[string]string{"Sec-Fetch-Site": "same-origin"}, cookie); got.StatusCode != 200 {
		t.Fatalf("cookie read: %d", got.StatusCode)
	}
	if got := env.do(t, "GET", "/v1/sandboxes/"+id+"/history", "", "", nil); got.StatusCode != 401 {
		t.Fatal(got.StatusCode)
	}
}

func TestUIMount_ServedOutsideAuthWhileTheAPIStaysProtected(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keys := auth.New(database, auth.SystemClock{})
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "dashboard") })
	handler := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Default().Limits.Files, nil, Options{UI: ui})
	for target, want := range map[string]int{"/ui": 200, "/ui/": 200, "/ui/sandboxes/sbx_a": 200, "/v1/sandboxes": 401, "/v1/health": 200, "/uix": 401} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != want {
			t.Errorf("%s: %d want %d", target, w.Code, want)
		}
	}
	// Disabled: /ui is just another unauthenticated path and gets no special handling.
	plain := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), config.Default().Limits.Files, nil)
	w := httptest.NewRecorder()
	plain.ServeHTTP(w, httptest.NewRequest("GET", "/ui/", nil))
	if w.Code != 401 {
		t.Fatalf("a disabled dashboard must not be served: %d", w.Code)
	}
}
