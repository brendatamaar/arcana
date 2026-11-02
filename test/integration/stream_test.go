//go:build linux && integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/coder/websocket"
)

type discardStream struct{}

func (discardStream) Started(string, orchestrator.StreamControls) {}
func (discardStream) Output(bool, []byte) bool                    { return true }
func assertStreamEngine(t *testing.T, ctx context.Context, service *orchestrator.Service, p auth.Principal, id string) {
	t.Helper()
	args := []string{"/usr/bin/true"}
	result, err := service.StreamExec(ctx, p, id, orchestrator.ExecRequest{Cmd: &args}, orchestrator.StreamOptions{Cols: 80, Rows: 24, Stdin: io.NopCloser(strings.NewReader("")), Observer: discardStream{}})
	if err != nil || result.ExitCode != 0 {
		t.Fatal("direct stream engine", err, result.ExitCode)
	}
}

func assertLiveStream(t *testing.T, ctx context.Context, handler http.Handler, id, key string) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+key)
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/sandboxes/"+id+"/exec/stream", &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal("stream upgrade", err)
	}
	defer func() { _ = connection.CloseNow() }()
	send := func(value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = connection.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
	}
	type frame struct {
		Type      string `json:"type"`
		ID        string `json:"exec_id"`
		Data      []byte `json:"data"`
		ExitCode  int    `json:"exit_code"`
		TimedOut  bool   `json:"timed_out"`
		Truncated bool   `json:"truncated"`
		Cancelled bool   `json:"cancelled"`
		Code      string `json:"code"`
		Message   string `json:"message"`
	}
	receive := func() frame {
		kind, data, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.MessageText {
			t.Fatal("binary server frame")
		}
		var message frame
		if err = json.Unmarshal(data, &message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	// stdin gates the second write, proving output arrives while the command runs.
	send(map[string]any{"type": "start", "shell": "printf first; read -r token; printf second; exit 7"})
	if message := receive(); message.Type != "started" || !strings.HasPrefix(message.ID, "exe_") {
		t.Fatal("missing started", message.Type, message.Code, message.Message)
	}
	var output []byte
	for len(output) < 5 {
		message := receive()
		if message.Type != "stdout" {
			t.Fatal("no live output before stdin", message.Type)
		}
		output = append(output, message.Data...)
	}
	if string(output) != "first" {
		t.Fatal("first output changed")
	}
	send(map[string]any{"type": "stdin", "data": []byte("continue\n")})
	send(map[string]string{"type": "stdin_close"})
	for {
		message := receive()
		if message.Type == "exit" {
			if message.ExitCode != 7 || message.TimedOut || message.Truncated || message.Cancelled {
				t.Fatal("stream lost real exit status")
			}
			break
		}
		if message.Type != "stdout" {
			t.Fatal("unexpected live stream frame", message.Type)
		}
		output = append(output, message.Data...)
	}
	if string(output) != "firstsecond" {
		t.Fatal("stream output lost")
	}
	if _, _, err = connection.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatal("stream did not close after exit", err)
	}
}
