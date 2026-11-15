//go:build linux && (integration || escape)

// Command envdprobe is a test-only gRPC client that runs inside a sandbox next to envd
// and checks the daemon's behaviour from the sandbox's own point of view.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
)

type outcome struct {
	stdout, stderr bytes.Buffer
	exit           *envdpb.ExecExit
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	conn, err := grpc.NewClient("passthrough:///envd", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", os.Args[1])
		}))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	client := envdpb.NewEnvdClient(conn)
	for _, check := range []struct {
		name string
		run  func(context.Context, envdpb.EnvdClient) error
	}{
		{"info", info},
		{"exit code and streams", streams},
		{"runs as the sandbox user", identity},
		{"timeout reaps detached descendants", timeout},
		{"cancel and signal", cancellation},
		{"tty size", tty},
		{"files and symlinks stay in the sandbox", files},
	} {
		if err = check.run(ctx, client); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
		fmt.Println("PASS", check.name)
	}
	return nil
}

func start(id string, timeout uint32, argv ...string) *envdpb.ExecClientMessage {
	return &envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Start{Start: &envdpb.ExecStart{ExecId: id, Argv: argv, TimeoutSeconds: timeout}}}
}

func exec(ctx context.Context, client envdpb.EnvdClient, message *envdpb.ExecClientMessage, during func(envdpb.Envd_ExecClient) error) (*outcome, error) {
	stream, err := client.Exec(ctx)
	if err != nil {
		return nil, err
	}
	if err = stream.Send(message); err != nil {
		return nil, err
	}
	if during != nil {
		if err = during(stream); err != nil {
			return nil, err
		}
	}
	return drain(stream)
}

func drain(stream envdpb.Envd_ExecClient) (*outcome, error) {
	result := &outcome{}
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return nil, err
		}
		switch m := message.Message.(type) {
		case *envdpb.ExecServerMessage_Output:
			if m.Output.GetStream() == envdpb.OutputStream_OUTPUT_STREAM_STDERR {
				result.stderr.Write(m.Output.GetData())
			} else {
				result.stdout.Write(m.Output.GetData())
			}
		case *envdpb.ExecServerMessage_Exit:
			result.exit = m.Exit
		}
	}
}

func info(ctx context.Context, client envdpb.EnvdClient) error {
	response, err := client.Info(ctx, &envdpb.InfoRequest{})
	if err != nil {
		return err
	}
	if len(response.AllowedRoots) != 2 {
		return fmt.Errorf("unexpected roots %v", response.AllowedRoots)
	}
	return nil
}

func streams(ctx context.Context, client envdpb.EnvdClient) error {
	result, err := exec(ctx, client, start("exe_streams", 20, "sh", "-c", "echo out; echo err >&2; exit 7"), nil)
	if err != nil {
		return err
	}
	if result.exit == nil || result.exit.ExitCode != 7 || result.stdout.String() != "out\n" || result.stderr.String() != "err\n" {
		return fmt.Errorf("unexpected result %+v %q %q", result.exit, result.stdout.String(), result.stderr.String())
	}
	return nil
}

func identity(ctx context.Context, client envdpb.EnvdClient) error {
	result, err := exec(ctx, client, start("exe_id", 20, "id", "-u"), nil)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.stdout.String()) != strconv.Itoa(os.Getuid()) || os.Getuid() == 0 {
		return fmt.Errorf("commands ran as %q, probe is %d", result.stdout.String(), os.Getuid())
	}
	return nil
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func timeout(ctx context.Context, client envdpb.EnvdClient) error {
	script := `trap '' TERM; sleep 300 & echo $!; setsid sleep 300 & echo $!; wait`
	result, err := exec(ctx, client, start("exe_timeout", 2, "sh", "-c", script), nil)
	if err != nil {
		return err
	}
	if result.exit == nil || !result.exit.TimedOut {
		return fmt.Errorf("not timed out: %+v", result.exit)
	}
	pids := strings.Fields(result.stdout.String())
	if len(pids) != 2 {
		return fmt.Errorf("expected two descendant PIDs, got %q", result.stdout.String())
	}
	for _, field := range pids {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			return convErr
		}
		if alive(pid) {
			return fmt.Errorf("descendant %d survived the timeout", pid)
		}
	}
	return nil
}

func cancellation(ctx context.Context, client envdpb.EnvdClient) error {
	stream, err := client.Exec(ctx)
	if err != nil {
		return err
	}
	if err = stream.Send(start("exe_cancel", 60, "sh", "-c", `trap '' TERM; setsid sleep 300 & echo $!; wait`)); err != nil {
		return err
	}
	var pid int
	for pid == 0 {
		message, recvErr := stream.Recv()
		if recvErr != nil {
			return recvErr
		}
		if data := message.GetOutput().GetData(); len(data) > 0 {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(data))); err != nil {
				return err
			}
		}
	}
	if _, err = client.Signal(ctx, &envdpb.SignalRequest{ExecId: "exe_cancel", Signal: envdpb.Signal_SIGNAL_TERM, GraceMillis: 100}); err != nil {
		return err
	}
	result, err := drain(stream)
	if err != nil {
		return err
	}
	if result.exit == nil || !result.exit.Cancelled || alive(pid) {
		return fmt.Errorf("cancel failed: %+v alive=%v", result.exit, alive(pid))
	}
	return nil
}

func tty(ctx context.Context, client envdpb.EnvdClient) error {
	message := start("exe_tty", 20, "stty", "size")
	message.GetStart().Tty, message.GetStart().Cols, message.GetStart().Rows = true, 100, 30
	result, err := exec(ctx, client, message, nil)
	if err != nil {
		return err
	}
	if result.exit == nil || result.exit.ExitCode != 0 || !strings.Contains(result.stdout.String(), "30 100") {
		return fmt.Errorf("unexpected terminal result %+v %q", result.exit, result.stdout.String())
	}
	return nil
}

func files(ctx context.Context, client envdpb.EnvdClient) error {
	payload := make([]byte, 200000)
	for i := range payload {
		payload[i] = byte(i * 13)
	}
	write, err := client.WriteFile(ctx)
	if err != nil {
		return err
	}
	if err = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Header{Header: &envdpb.WriteFileHeader{Path: "/workspace/d/blob"}}}); err != nil {
		return err
	}
	for rest := payload; len(rest) > 0; {
		n := min(len(rest), 60000)
		if err = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: rest[:n]}}); err != nil {
			return err
		}
		rest = rest[n:]
	}
	if _, err = write.CloseAndRecv(); err != nil {
		return err
	}
	got, err := readFile(ctx, client, "/workspace/d/blob")
	if err != nil || !bytes.Equal(got, payload) {
		return fmt.Errorf("round trip failed: %v (%d bytes)", err, len(got))
	}
	if err = os.Symlink("/etc/passwd", "/workspace/link"); err != nil {
		return err
	}
	linked, err := readFile(ctx, client, "/workspace/link")
	local, localErr := os.ReadFile("/etc/passwd")
	if err != nil || localErr != nil || !bytes.Equal(linked, local) {
		return fmt.Errorf("symlink did not resolve to the sandbox's own file: %v %v", err, localErr)
	}
	for _, path := range []string{"/etc/passwd", "/workspace/../etc/passwd", "relative"} {
		if _, err = readFile(ctx, client, path); status.Code(err) != codes.InvalidArgument {
			return fmt.Errorf("%q was not rejected: %v", path, err)
		}
	}
	return nil
}

func readFile(ctx context.Context, client envdpb.EnvdClient, path string) ([]byte, error) {
	stream, err := client.ReadFile(ctx, &envdpb.ReadFileRequest{Path: path})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
		out.Write(message.GetChunk())
	}
}
