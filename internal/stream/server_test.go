package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/internal/store/db"
)

type testDriver struct {
	*fake.Driver
	mu        sync.Mutex
	script    fake.Script
	code      int
	cancelled bool
	gate      chan struct{}
	finish    sync.Once
	sessions  chan *testSession
}
type (
	testSession struct {
		runtime.ExecSession
		base      *fake.Session
		inputDone chan struct{}
		once      sync.Once
		resized   chan resize
	}
	testStdin struct {
		io.WriteCloser
		session *testSession
	}
)

func (w testStdin) Close() error {
	err := w.WriteCloser.Close()
	w.session.once.Do(func() { close(w.session.inputDone) })
	return err
}
func (s *testSession) Stdin() io.WriteCloser { return testStdin{s.ExecSession.Stdin(), s} }
func (s *testSession) Resize(cols, rows uint16) error {
	err := s.ExecSession.Resize(cols, rows)
	select {
	case s.resized <- resize{cols, rows}:
	default:
	}
	return err
}

func (d *testDriver) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := d.script
	switch request.Cmd[1] {
	case "status":
		data, err := json.Marshal(protocol.Status{ID: request.Cmd[3], ExitCode: d.code, Cancelled: d.cancelled, CleanupComplete: true})
		if err != nil {
			return nil, err
		}
		script = fake.Script{Stdout: data}
	case "kill":
		d.cancelled = true
		d.finish.Do(func() {
			if d.gate != nil {
				close(d.gate)
			}
		})
		script = fake.Script{}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	session, err := d.Driver.Exec(ctx, id, request)
	if err != nil {
		return nil, err
	}
	if request.Cmd[1] != "run" {
		return session, nil
	}
	wrapped := &testSession{ExecSession: session, base: session.(*fake.Session), inputDone: make(chan struct{}), resized: make(chan resize, 16)}
	d.sessions <- wrapped
	return wrapped, nil
}

type testRunner struct {
	service *orchestrator.Service
	p       auth.Principal
	id      string
	done    chan execution
}

func (r testRunner) Run(ctx context.Context, request orchestrator.ExecRequest, options orchestrator.StreamOptions) (orchestrator.ExecResult, error) {
	result, err := r.service.StreamExec(ctx, r.p, r.id, request, options)
	r.done <- execution{result, err}
	return result, err
}

type (
	gateState struct {
		active          atomic.Bool
		release, closed chan struct{}
		once            sync.Once
	}
	gateConn struct {
		net.Conn
		state *gateState
	}
)

func (c *gateConn) Write(data []byte) (int, error) {
	if c.state.active.Load() {
		select {
		case <-c.state.release:
		case <-c.state.closed:
			return 0, net.ErrClosed
		}
	}
	return c.Conn.Write(data)
}

func (c *gateConn) Close() error {
	c.state.once.Do(func() { close(c.state.closed) })
	return c.Conn.Close()
}

type gateListener struct {
	net.Listener
	state *gateState
}

func (l gateListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &gateConn{connection, l.state}, nil
}

type fixture struct {
	ctx           context.Context
	conn          *websocket.Conn
	driver        *testDriver
	service       *orchestrator.Service
	database      *store.Store
	principal     auth.Principal
	primary, peer db.Sandbox
	done          chan execution
	gate          *gateState
}

func fixtureStream(t *testing.T, script fake.Script, code int, finish chan struct{}, clock Clock, gate bool) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	keys := auth.New(database, auth.SystemClock{})
	owner, _, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	driver := &testDriver{Driver: fake.New(), script: script, code: code, gate: finish, sessions: make(chan *testSession, 1)}
	service := orchestrator.New(database, driver, config.Default(), orchestrator.SystemClock{})
	principal := auth.Principal{ID: owner.ID}
	primary, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := service.Create(ctx, principal, orchestrator.CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = driver.Destroy(cleanup, primary.ContainerID.String)
		_ = driver.Destroy(cleanup, peer.ContainerID.String)
	})
	done := make(chan execution, 1)
	runner := testRunner{service, principal, primary.ID, done}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		Serve(ctx, connection, runner, clock, func(err error) (string, string) {
			if errors.Is(err, ErrInvalid) || errors.Is(err, ErrInputLimit) || errors.Is(err, orchestrator.ErrInvalid) {
				return "invalid_request", "invalid stream message"
			}
			return "runtime_unavailable", "exec failed"
		})
	}))
	state := &gateState{release: make(chan struct{}), closed: make(chan struct{})}
	if gate {
		server.Listener = gateListener{server.Listener, state}
	}
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(cancel)
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return &fixture{ctx, connection, driver, service, database, principal, primary, peer, done, state}
}

type frame struct {
	Type      string `json:"type"`
	ID        string `json:"exec_id"`
	Data      []byte `json:"data"`
	ExitCode  int    `json:"exit_code"`
	Cancelled bool   `json:"cancelled"`
	Truncated bool   `json:"truncated"`
	Code      string `json:"code"`
}

func send(t *testing.T, f *fixture, value any) {
	t.Helper()
	if err := f.conn.Write(f.ctx, websocket.MessageText, encode(value)); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, f *fixture) frame {
	t.Helper()
	kind, data, err := f.conn.Read(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.MessageText {
		t.Fatal("server sent binary frame")
	}
	var result frame
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func startFrame(tty bool) any {
	return struct {
		Type string   `json:"type"`
		Cmd  []string `json:"cmd"`
		TTY  bool     `json:"tty"`
		Cols int      `json:"cols"`
		Rows int      `json:"rows"`
	}{"start", []string{"program"}, tty, 80, 24}
}

func waitDone(t *testing.T, f *fixture) execution {
	t.Helper()
	select {
	case result := <-f.done:
		return result
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
		return execution{}
	}
}

func assertHealthy(t *testing.T, f *fixture) {
	t.Helper()
	for _, row := range []db.Sandbox{f.primary, f.peer} {
		current, err := f.service.Get(f.ctx, f.principal, row.ID)
		if err != nil || current.State != "running" {
			t.Fatal("sandbox lost", err)
		}
	}
}

func TestServer_StartedBinarySafeOutputAndExitWithoutStdinClose(t *testing.T) {
	f := fixtureStream(t, fake.Script{Stdout: []byte{0xff, 0, 'X'}, Stderr: []byte("error")}, 0, nil, orchestrator.SystemClock{}, false)
	send(t, f, startFrame(false))
	started := receive(t, f)
	if started.Type != "started" || started.ID == "" {
		t.Fatal("started was not first")
	}
	var stdout, stderr []byte
	for {
		message := receive(t, f)
		if message.Type == "exit" {
			if message.ExitCode != 0 || message.Cancelled {
				t.Fatal("incorrect exit")
			}
			break
		}
		switch message.Type {
		case "stdout":
			stdout = append(stdout, message.Data...)
		case "stderr":
			stderr = append(stderr, message.Data...)
		default:
			t.Fatal(message.Type)
		}
	}
	if !bytes.Equal(stdout, []byte{0xff, 0, 'X'}) || string(stderr) != "error" {
		t.Fatal("binary data was changed")
	}
	if result := waitDone(t, f); result.err != nil {
		t.Fatal(result.err)
	}
	session := <-f.driver.sessions
	if request := session.base.Request(); request.TTY || request.Cols != 0 || request.Rows != 0 {
		t.Fatal("non-TTY exec received console dimensions")
	}
	assertHealthy(t, f)
}

func TestServer_StdinCloseTTYResizeAndMergedOutput(t *testing.T) {
	finish := make(chan struct{})
	f := fixtureStream(t, fake.Script{Stdout: []byte("out"), Stderr: []byte("err"), ExitGate: finish}, 0, finish, orchestrator.SystemClock{}, false)
	send(t, f, startFrame(true))
	if receive(t, f).Type != "started" {
		t.Fatal("missing started")
	}
	var session *testSession
	select {
	case session = <-f.driver.sessions:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	send(t, f, struct {
		Type string `json:"type"`
		Data []byte `json:"data"`
	}{"stdin", []byte{'a', 0, 0xff}})
	send(t, f, map[string]string{"type": "stdin_close"})
	send(t, f, map[string]any{"type": "resize", "cols": 100, "rows": 40})
	select {
	case <-session.inputDone:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	select {
	case size := <-session.resized:
		if size != (resize{100, 40}) {
			t.Fatal(size)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if !bytes.Equal(session.base.Input(), []byte{'a', 0, 0xff}) {
		t.Fatal("stdin changed")
	}
	if !session.base.Request().TTY || session.base.Request().Cmd[8] != "--tty" {
		t.Fatal("TTY helper foreground flag missing")
	}
	f.driver.finish.Do(func() { close(finish) })
	var output []byte
	for {
		message := receive(t, f)
		if message.Type == "exit" {
			break
		}
		if message.Type != "stdout" {
			t.Fatal("TTY emitted stderr")
		}
		output = append(output, message.Data...)
	}
	if string(output) != "outerr" {
		t.Fatal("TTY output lost")
	}
	if result := waitDone(t, f); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestServer_CancelAndDisconnectConfirmCleanup(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "disconnect"}[disconnect], func(t *testing.T) {
			finish := make(chan struct{})
			f := fixtureStream(t, fake.Script{ExitGate: finish, Exit: runtime.ExitInfo{ExitCode: 124}}, 137, finish, orchestrator.SystemClock{}, false)
			send(t, f, startFrame(false))
			started := receive(t, f)
			if started.Type != "started" {
				t.Fatal("missing started")
			}
			if disconnect {
				if err := f.conn.CloseNow(); err != nil {
					t.Fatal(err)
				}
			} else {
				send(t, f, map[string]string{"type": "cancel"})
				message := receive(t, f)
				if message.Type != "exit" || !message.Cancelled || message.ExitCode != 137 {
					t.Fatal("cancel lost real status")
				}
			}
			result := waitDone(t, f)
			if !errors.Is(result.err, context.Canceled) || !result.result.Cancelled {
				t.Fatal("did not cancel exec", result.err)
			}
			record, err := f.database.Queries.GetExec(f.ctx, started.ID)
			if err != nil || !record.EndedAt.Valid || record.Cancelled.Int64 != 1 {
				t.Fatal("cleanup not recorded", err)
			}
			assertHealthy(t, f)
		})
	}
}

func TestServer_RejectsMissingStartAndProtocolViolations(t *testing.T) {
	for _, body := range []string{`{"type":"stdin","data":"YQ=="}`, `{"type":"resize","cols":1,"rows":1}`, `{"type":"cancel"}`, `{"type":"invalid"}`} {
		t.Run(body, func(t *testing.T) {
			f := fixtureStream(t, fake.Script{}, 0, nil, orchestrator.SystemClock{}, false)
			if err := f.conn.Write(f.ctx, websocket.MessageText, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if message := receive(t, f); message.Type != "error" || message.Code != "invalid_request" {
				t.Fatal("missing start accepted")
			}
			select {
			case <-f.driver.sessions:
				t.Fatal("executed before start")
			default:
			}
		})
	}
	for _, violation := range []string{`{"type":"start","cmd":["again"]}`, `{"type":"resize","cols":100,"rows":40}`, `{"type":"stdin","data":"!"}`} {
		t.Run(violation, func(t *testing.T) {
			finish := make(chan struct{})
			f := fixtureStream(t, fake.Script{ExitGate: finish, Exit: runtime.ExitInfo{ExitCode: 124}}, 137, finish, orchestrator.SystemClock{}, false)
			send(t, f, startFrame(false))
			if receive(t, f).Type != "started" {
				t.Fatal("missing started")
			}
			if err := f.conn.Write(f.ctx, websocket.MessageText, []byte(violation)); err != nil {
				t.Fatal(err)
			}
			if message := receive(t, f); message.Type != "error" || message.Code != "invalid_request" {
				t.Fatal("invalid protocol accepted", message.Type, message.Code)
			}
			_ = waitDone(t, f)
			assertHealthy(t, f)
		})
	}
}

func TestServer_SlowReaderDropsOutputWithinOneMiB(t *testing.T) {
	output := make(chan struct{})
	f := fixtureStream(t, fake.Script{Stdout: bytes.Repeat([]byte("x"), 8*1048576), OutputGate: output}, 0, nil, orchestrator.SystemClock{}, true)
	send(t, f, startFrame(false))
	if receive(t, f).Type != "started" {
		t.Fatal("missing started")
	}
	f.gate.active.Store(true)
	close(output)
	result := waitDone(t, f)
	if result.err != nil || !result.result.Truncated {
		t.Fatal("backpressure did not truncate", result.err)
	}
	close(f.gate.release)
	for {
		message := receive(t, f)
		if message.Type == "exit" {
			if !message.Truncated {
				t.Fatal("drop not reported")
			}
			break
		}
		if message.Type != "stdout" {
			t.Fatal(message.Type)
		}
	}
	record, err := f.database.Queries.GetExec(f.ctx, result.result.ID)
	if err != nil || record.Truncated.Int64 != 1 || record.StdoutBytes.Int64 != 8*1048576 {
		t.Fatal("drop/drain not persisted", err)
	}
}

func TestServer_PipelinedCancellationPreservesStartedBeforeExit(t *testing.T) {
	finish := make(chan struct{})
	f := fixtureStream(t, fake.Script{ExitGate: finish, Exit: runtime.ExitInfo{ExitCode: 124}}, 137, finish, orchestrator.SystemClock{}, true)
	f.gate.active.Store(true)
	send(t, f, startFrame(false))
	select {
	case <-f.driver.sessions:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	send(t, f, map[string]string{"type": "cancel"})
	result := waitDone(t, f)
	if !errors.Is(result.err, context.Canceled) || result.result.ExitCode != 137 {
		t.Fatal("pipelined cancel lost cleanup", result.err)
	}
	close(f.gate.release)
	if receive(t, f).Type != "started" {
		t.Fatal("exit overtook started")
	}
	if message := receive(t, f); message.Type != "exit" || !message.Cancelled {
		t.Fatal("missing cancellation result")
	}
	assertHealthy(t, f)
}
