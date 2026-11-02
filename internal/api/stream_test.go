package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/apierr"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

func TestStreamEndpoint_AuthenticatesOwnershipAndEnvelopesUpgradeErrors(t *testing.T) {
	handler, _, owner, other := lifecycleFixture(t)
	created := request(handler, "POST", "/v1/sandboxes", owner, `{"template":"base"}`)
	var row gen.Sandbox
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil || created.Code != 201 {
		t.Fatal("creating fixture", err)
	}
	endpoint := "/v1/sandboxes/" + row.Id + "/exec/stream"
	for _, test := range []struct {
		key    string
		status int
	}{{"", 401}, {other, 404}, {owner, 400}} {
		got := request(handler, "GET", endpoint, test.key, "")
		if got.Code != test.status {
			t.Fatal("invalid ownership/upgrade status", got.Code, got.Body.String())
		}
		var envelope apierr.Envelope
		if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil || envelope.Error.RequestID == "" {
			t.Fatal("handshake error escaped API envelope", err)
		}
	}
	r := httptest.NewRequest("GET", endpoint, nil)
	r.Header.Set("Authorization", "Bearer "+owner)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	r.Header.Set("Origin", "https://untrusted.example")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, r)
	if got.Code != 403 {
		t.Fatal("cross-origin upgrade allowed", got.Code)
	}
	var envelope apierr.Envelope
	if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.Forbidden {
		t.Fatal("missing origin error envelope", err)
	}
}

type shutdownRuntime struct {
	*fake.Driver
	mu   sync.Mutex
	gate chan struct{}
	once sync.Once
}

func (d *shutdownRuntime) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := fake.Script{ExitGate: d.gate, Exit: runtime.ExitInfo{ExitCode: 124}}
	switch request.Cmd[1] {
	case "kill":
		d.once.Do(func() { close(d.gate) })
		script = fake.Script{}
	case "status":
		data, err := json.Marshal(protocol.Status{ID: request.Cmd[3], ExitCode: 137, Cancelled: true, CleanupComplete: true})
		if err != nil {
			return nil, err
		}
		script = fake.Script{Stdout: data}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	return d.Driver.Exec(ctx, id, request)
}

func TestServe_ShutdownWaitsForWebSocketExecCleanup(t *testing.T) {
	database, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keys := auth.New(database, auth.SystemClock{})
	owner, secret, err := keys.Create(context.Background(), "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	driver := &shutdownRuntime{Driver: fake.New(), gate: make(chan struct{})}
	cfg := config.Default()
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	row, err := service.Create(context.Background(), auth.Principal{ID: owner.ID}, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		done <- Serve(ctx, listener, New(keys, logger, cfg.Limits.Files, service), 3*time.Second, logger)
	}()
	t.Cleanup(func() { stop(); _ = listener.Close(); <-closed })
	requestCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+secret)
	connection, _, err := websocket.Dial(requestCtx, "ws://"+listener.Addr().String()+"/v1/sandboxes/"+row.ID+"/exec/stream", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	if err = connection.Write(requestCtx, websocket.MessageText, []byte(`{"type":"start","cmd":["waiting"]}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := connection.Read(requestCtx)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Type string `json:"type"`
		ID   string `json:"exec_id"`
	}
	if err = json.Unmarshal(data, &started); err != nil || started.Type != "started" || !strings.HasPrefix(started.ID, "exe_") {
		t.Fatal("missing started", err)
	}
	stop()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-requestCtx.Done():
		t.Fatal("shutdown did not drain WebSocket")
	}
	record, err := database.Queries.GetExec(context.Background(), started.ID)
	if err != nil || !record.EndedAt.Valid || record.Cancelled.Int64 != 1 || !record.ExitCode.Valid {
		t.Fatal("shutdown returned before cleanup was recorded", err)
	}
}

func TestStreamEndpoint_AdmissionPreservesOwnershipAndReleasesFailedUpgrades(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keys := auth.New(database, auth.SystemClock{})
	owner, secret, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keys.Create(ctx, "other", false)
	if err != nil {
		t.Fatal(err)
	}
	service := orchestrator.New(database, fake.New(), config.Default(), orchestrator.SystemClock{})
	row, err := service.Create(ctx, auth.Principal{ID: owner.ID}, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{keys: keys, logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), lifecycle: service, streams: make(chan struct{}, 1)}
	handler := apierr.RequestIDs(keys.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.ExecStream(w, r, row.ID) })))
	server.streams <- struct{}{}
	for _, test := range []struct {
		key    string
		status int
	}{{secret, 409}, {other, 404}, {"", 401}} {
		if got := request(handler, "GET", "/stream", test.key, ""); got.Code != test.status {
			t.Fatal("admission/ownership failure", got.Code)
		}
	}
	<-server.streams
	for range 2 {
		if got := request(handler, "GET", "/stream", secret, ""); got.Code != 400 {
			t.Fatal("failed upgrade consumed admission slot", got.Code)
		}
		if len(server.streams) != 0 {
			t.Fatal("admission slot leaked")
		}
	}
}
