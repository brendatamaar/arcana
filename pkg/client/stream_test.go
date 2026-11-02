package client_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/pkg/client"
)

func TestStream_StdinResizeCancelAndRealNonzeroExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	received := make(chan []map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		var packets []map[string]any
		for i := 0; i < 5; i++ {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var packet map[string]any
			if json.Unmarshal(data, &packet) != nil {
				return
			}
			packets = append(packets, packet)
			if i == 0 {
				if err = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"started","exec_id":"exe_test"}`)); err != nil {
					return
				}
			}
		}
		received <- packets
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","exit_code":7,"timed_out":false,"truncated":true,"cancelled":true,"duration_ms":10}`))
	}))
	defer server.Close()
	c, err := client.New(server.URL, "key", client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := c.ExecStream(ctx, "sbx_test", client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"cat"}}, TTY: true, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if n, err := stream.Write([]byte{0, 255, 1}); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if err = stream.Resize(ctx, 100, 40); err != nil {
		t.Fatal(err)
	}
	if err = stream.CloseStdin(); err != nil {
		t.Fatal(err)
	}
	if err = stream.CloseStdin(); err != nil {
		t.Fatal("EOF not idempotent", err)
	}
	if _, err = stream.Write([]byte("late")); err != io.ErrClosedPipe {
		t.Fatal(err)
	}
	if err = stream.Cancel(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := stream.Wait()
	if err != nil || result.ExitCode != 7 || !result.Cancelled || !result.Truncated {
		t.Fatal(result, err)
	}
	packets := <-received
	for i, want := range []string{"start", "stdin", "resize", "stdin_close", "cancel"} {
		if packets[i]["type"] != want {
			t.Fatal(packets)
		}
	}
	if packets[0]["tty"] != true || packets[1]["data"] != "AP8B" || packets[2]["cols"] != float64(100) {
		t.Fatal(packets)
	}
}

func TestStream_CloseUnblocksUnreadOutputAndMalformedExitFails(t *testing.T) {
	for _, mode := range []string{"blocked", "malformed", "disconnect", "before-start"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sent := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				if _, _, err = conn.Read(ctx); err != nil {
					return
				}
				if mode == "before-start" {
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"stdout","data":"eA=="}`))
					return
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"started","exec_id":"exe_test"}`))
				switch mode {
				case "blocked":
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"stdout","data":"eA=="}`))
					close(sent)
					_, _, _ = conn.Read(ctx)
				case "malformed":
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"exit"}`))
				case "disconnect":
					return
				}
			}))
			defer server.Close()
			c, err := client.New(server.URL, "", client.Options{})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := c.ExecStream(ctx, "x", client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"true"}}})
			if mode == "before-start" {
				if err == nil {
					_ = stream.Close()
					t.Fatal("output before acknowledgement accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			if mode == "blocked" {
				<-sent
				if err = stream.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, err = stream.Wait()
			if err == nil {
				t.Fatal("missing terminal proof accepted")
			}
		})
	}
}

func ExampleClient_ExecStream() {
	c, err := client.New("http://127.0.0.1:7070", "operator-api-key", client.Options{})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := c.ExecStream(ctx, "sandbox-id", client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: strings.Fields("python3 --version")}})
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	stderrDone := make(chan struct{})
	go func() { defer close(stderrDone); _, _ = io.Copy(io.Discard, stream.Stderr) }()
	_, _ = io.Copy(io.Discard, stream.Stdout)
	<-stderrDone
	_, _ = stream.Wait()
}
