package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

type clockTimer struct {
	after  time.Duration
	events chan time.Time
}

func (t *clockTimer) Chan() <-chan time.Time { return t.events }
func (*clockTimer) Stop()                    {}

type testClock struct{ timers chan *clockTimer }

func (c testClock) NewTimer(after time.Duration) orchestrator.Timer {
	timer := &clockTimer{after, make(chan time.Time, 1)}
	c.timers <- timer
	return timer
}

func timerFor(t *testing.T, f *fixture, c testClock, after time.Duration) *clockTimer {
	t.Helper()
	for {
		select {
		case timer := <-c.timers:
			if timer.after == after {
				return timer
			}
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
			return nil
		}
	}
}

func TestServer_StartDeadlineUsesInjectedClock(t *testing.T) {
	clock := testClock{make(chan *clockTimer, 16)}
	f := fixtureStream(t, fake.Script{}, 0, nil, clock, false)
	timerFor(t, f, clock, 10*time.Second).events <- time.Time{}
	if message := receive(t, f); message.Type != "error" || message.Code != "invalid_request" {
		t.Fatal("missing-start deadline did not reject")
	}
	select {
	case <-f.driver.sessions:
		t.Fatal("executed without start")
	default:
	}
}

func TestServer_PingPongKeepsReadingWhileExecutionRuns(t *testing.T) {
	clock := testClock{make(chan *clockTimer, 16)}
	finish := make(chan struct{})
	f := fixtureStream(t, fake.Script{ExitGate: finish}, 0, finish, clock, false)
	messages := make(chan frame, 8)
	readErrors := make(chan error, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			_, data, err := f.conn.Read(f.ctx)
			if err != nil {
				readErrors <- err
				return
			}
			var message frame
			if err = json.Unmarshal(data, &message); err != nil {
				readErrors <- err
				return
			}
			select {
			case messages <- message:
			case <-f.ctx.Done():
				return
			}
		}
	}()
	defer func() { _ = f.conn.CloseNow(); <-readDone }()
	next := func() frame {
		select {
		case message := <-messages:
			return message
		case err := <-readErrors:
			t.Fatal(err)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		}
		return frame{}
	}
	send(t, f, startFrame(false))
	if next().Type != "started" {
		t.Fatal("missing started")
	}
	timerFor(t, f, clock, 15*time.Second).events <- time.Time{}
	// A second ping timer is scheduled only after a pong has been read successfully.
	_ = timerFor(t, f, clock, 15*time.Second)
	f.driver.finish.Do(func() { close(finish) })
	if message := next(); message.Type != "exit" || message.ExitCode != 0 {
		t.Fatal("keepalive broke execution")
	}
	_ = waitDone(t, f)
}

func TestServer_BinaryAndOversizedFramesCancelAcceptedExecs(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(map[bool]string{false: "binary", true: "oversized"}[oversized], func(t *testing.T) {
			finish := make(chan struct{})
			f := fixtureStream(t, fake.Script{ExitGate: finish, Exit: runtime.ExitInfo{ExitCode: 124}}, 137, finish, orchestrator.SystemClock{}, false)
			send(t, f, startFrame(false))
			if receive(t, f).Type != "started" {
				t.Fatal("missing started")
			}
			if oversized {
				_ = f.conn.Write(f.ctx, websocket.MessageText, make([]byte, 65537))
			} else {
				if err := f.conn.Write(f.ctx, websocket.MessageBinary, []byte(`{"type":"cancel"}`)); err != nil {
					t.Fatal(err)
				}
				if message := receive(t, f); message.Type != "error" || message.Code != "invalid_request" {
					t.Fatal("binary frame accepted")
				}
			}
			result := waitDone(t, f)
			if !errors.Is(result.err, context.Canceled) {
				t.Fatal("accepted command survived invalid transport", result.err)
			}
			assertHealthy(t, f)
		})
	}
}

func TestServer_ClosedInputAndResizeFailureCancelTheExec(t *testing.T) {
	for _, resizeFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "stdin_after_close", true: "resize_failure"}[resizeFailure], func(t *testing.T) {
			finish := make(chan struct{})
			script := fake.Script{ExitGate: finish, Exit: runtime.ExitInfo{ExitCode: 124}}
			if resizeFailure {
				script.ResizeError = io.ErrClosedPipe
			}
			f := fixtureStream(t, script, 137, finish, orchestrator.SystemClock{}, false)
			send(t, f, startFrame(resizeFailure))
			if receive(t, f).Type != "started" {
				t.Fatal("missing started")
			}
			expected := "invalid_request"
			if resizeFailure {
				send(t, f, map[string]any{"type": "resize", "cols": 120, "rows": 40})
				expected = "runtime_unavailable"
			} else {
				send(t, f, map[string]string{"type": "stdin_close"})
				send(t, f, map[string]string{"type": "stdin", "data": "YQ=="})
			}
			if message := receive(t, f); message.Type != "error" || message.Code != expected {
				t.Fatal("invalid input/control result", message.Type, message.Code)
			}
			_ = waitDone(t, f)
			assertHealthy(t, f)
		})
	}
}
