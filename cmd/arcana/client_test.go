package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func cliServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthorized","message":"unauthorized","request_id":"req_test"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/exec/stream") {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close(websocket.StatusNormalClosure, "complete") }()
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var start struct {
				Type string   `json:"type"`
				Cmd  []string `json:"cmd"`
			}
			if json.Unmarshal(data, &start) != nil || start.Type != "start" || len(start.Cmd) != 2 || start.Cmd[1] != "literal ; argument" {
				t.Error("argv changed")
				return
			}
			for _, packet := range []string{`{"type":"started","exec_id":"exe_test"}`, `{"type":"stdout","data":"b3V0"}`, `{"type":"stderr","data":"ZXJy"}`, `{"type":"exit","exit_code":7,"timed_out":false,"truncated":false,"cancelled":false,"duration_ms":1}`} {
				if err = conn.Write(ctx, websocket.MessageText, []byte(packet)); err != nil {
					return
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/sandboxes" && r.Method == "GET":
			_, _ = io.WriteString(w, `{"items":[{"id":"sbx_test","state":"running","template":"base"}]}`)
		case strings.HasSuffix(r.URL.Path, "/files/list"):
			_, _ = io.WriteString(w, `{"items":[{"path":"/workspace/a","name":"a","size_bytes":3,"type":"file"}]}`)
		case strings.HasSuffix(r.URL.Path, "/files"):
			if r.URL.Query().Get("path") != "/workspace/a ? &" {
				t.Error("file query changed")
			}
			if r.Method == "PUT" {
				data, _ := io.ReadAll(io.LimitReader(r.Body, 100))
				if string(data) != "upload" {
					t.Error("upload changed")
				}
				w.WriteHeader(204)
			} else {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte{0, 255, 'x'})
			}
		case r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			_, _ = io.WriteString(w, `{"id":"sbx_test","state":"running","template":"base"}`)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func runCLI(server string, args ...string) (string, string, error) {
	root := newRootCommand()
	var out, stderr bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader("upload"))
	root.SetArgs(append([]string{"--server", server, "--api-key", "test-key"}, args...))
	err := root.Execute()
	return out.String(), stderr.String(), err
}

func TestClientCLI_LifecycleTablesJSONFilesAndEnvironment(t *testing.T) {
	server := cliServer(t)
	for _, args := range [][]string{{"sandbox", "create"}, {"sandbox", "list"}, {"sandbox", "get", "sbx_test"}, {"sandbox", "rm", "sbx_test"}, {"files", "ls", "sbx_test", "/workspace"}} {
		out, _, err := runCLI(server.URL, args...)
		if err != nil || out == "" {
			t.Fatal(args, out, err)
		}
	}
	out, _, err := runCLI(server.URL, "--json", "sandbox", "list")
	var page struct {
		Items []struct {
			ID string `json:"id"`
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &page) != nil || len(page.Items) != 1 {
		t.Fatal(out, err)
	}
	if _, _, err = runCLI(server.URL, "files", "put", "sbx_test", "-", "/workspace/a ? &"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(server.URL, "files", "get", "sbx_test", "/workspace/a ? &")
	if err != nil || out != "\x00\xffx" {
		t.Fatal(out, err)
	}
	filename := t.TempDir() + "/download"
	if _, _, err = runCLI(server.URL, "files", "get", "sbx_test", "/workspace/a ? &", filename); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runCLI(server.URL, "files", "get", "sbx_test", "/workspace/a ? &", filename); err == nil {
		t.Fatal("existing local file overwritten")
	}
	t.Setenv("ARCANA_URL", server.URL)
	t.Setenv("ARCANA_API_KEY", "test-key")
	root := newRootCommand()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"sandbox", "list"})
	if err = root.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestExecCLI_PreservesArgvStreamsAndRemoteExit(t *testing.T) {
	server := cliServer(t)
	out, stderr, err := runCLI(server.URL, "exec", "sbx_test", "--", "echo", "literal ; argument")
	if exitCode(err) != 7 || out != "out" || stderr != "err" {
		t.Fatal(out, stderr, err)
	}
	out, stderr, err = runCLI(server.URL, "--json", "exec", "sbx_test", "--", "echo", "literal ; argument")
	if exitCode(err) != 7 || !strings.Contains(out, `"exit_code":7`) || stderr != "" {
		t.Fatal(out, stderr, err)
	}
	for _, args := range [][]string{{"exec", "sbx_test", "echo"}, {"exec", "--tty", "sbx_test", "--", "echo"}, {"files", "get", "sbx_test"}} {
		if _, _, err = runCLI(server.URL, args...); err == nil {
			t.Fatal("invalid args accepted", args)
		}
	}
}

func TestExecCLI_ProcessExitCode(t *testing.T) {
	server := cliServer(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCLIProcessHelper$") // #nosec G204 -- executable is this test binary from os.Executable, not caller input.
	cmd.Env = append(os.Environ(), "ARCANA_CLI_TEST_SERVER="+server.URL)
	err = cmd.Run()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 7 {
		t.Fatal("process exit code", err)
	}
}

func TestCLIProcessHelper(_ *testing.T) {
	server := os.Getenv("ARCANA_CLI_TEST_SERVER")
	if server == "" {
		return
	}
	os.Args = []string{"arcana", "--server", server, "--api-key", "test-key", "exec", "sbx_test", "--", "echo", "literal ; argument"}
	main()
}
