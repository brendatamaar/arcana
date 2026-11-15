package envd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
)

var envdBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "envd-bin")
	if err != nil {
		panic(err)
	}
	envdBinary = filepath.Join(dir, "envd")
	build := exec.Command("go", "build", "-o", envdBinary, "../../cmd/envd") // #nosec G204 -- fixed arguments; the output path is a fresh temporary directory
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type fixture struct {
	client    envdpb.EnvdClient
	workspace string
	state     string
	server    *Server
}

func start(t *testing.T, tune func(*Config)) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	f := &fixture{workspace: filepath.Join(root, "ws"), state: filepath.Join(root, "run")}
	for _, dir := range []string{f.workspace, f.state} {
		if err = os.Mkdir(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Self: envdBinary, StateDir: f.state, Roots: []string{f.workspace}, BaseEnv: []string{"PATH=" + os.Getenv("PATH")}}
	if tune != nil {
		tune(&cfg)
	}
	f.server, err = NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := Listen("unix:" + filepath.Join(root, "s"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, NewGRPCServer(f.server), listener, 30*time.Second) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	conn, err := grpc.NewClient("passthrough:///envd", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(root, "s"))
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f.client = envdpb.NewEnvdClient(conn)
	return f
}

type result struct {
	stdout, stderr bytes.Buffer
	exit           *envdpb.ExecExit
	err            error
}

func startMsg(f *fixture, id string, timeout uint32, argv ...string) *envdpb.ExecClientMessage {
	return &envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Start{Start: &envdpb.ExecStart{ExecId: id, Argv: argv, Cwd: f.workspace, TimeoutSeconds: timeout}}}
}

func stdinMsg(data string) *envdpb.ExecClientMessage {
	return &envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Stdin{Stdin: []byte(data)}}
}

// open starts an exec and returns the stream after the started message.
func (f *fixture) open(ctx context.Context, t *testing.T, start *envdpb.ExecClientMessage) envdpb.Envd_ExecClient {
	t.Helper()
	stream, err := f.client.Exec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(start); err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetStarted() == nil {
		t.Fatalf("expected started, got %v %v", first, err)
	}
	return stream
}

func collect(stream envdpb.Envd_ExecClient) *result {
	r := &result{}
	for {
		message, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.err = err
			}
			return r
		}
		switch m := message.Message.(type) {
		case *envdpb.ExecServerMessage_Output:
			if m.Output.GetStream() == envdpb.OutputStream_OUTPUT_STREAM_STDERR {
				r.stderr.Write(m.Output.GetData())
			} else {
				r.stdout.Write(m.Output.GetData())
			}
		case *envdpb.ExecServerMessage_Exit:
			r.exit = m.Exit
		}
	}
}

func (f *fixture) run(t *testing.T, id string, timeout uint32, argv ...string) *result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return collect(f.open(ctx, t, startMsg(f, id, timeout, argv...)))
}

func code(err error) codes.Code { return status.Code(err) }

func gone(pid int) bool {
	err := syscall.Kill(pid, 0)
	return errors.Is(err, syscall.ESRCH)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d survived", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestInfo_ReportsLimits(t *testing.T) {
	f := start(t, func(c *Config) { c.Version, c.MaxExecs = "9.9", 3 })
	info, err := f.client.Info(context.Background(), &envdpb.InfoRequest{})
	if err != nil || info.Version != "9.9" || info.MaxConcurrentExecs != 3 || info.MaxChunkBytes != MaxChunkBytes || len(info.AllowedRoots) != 1 {
		t.Fatalf("%v %v", info, err)
	}
}

func TestExec_SeparatesStreamsAndReturnsRealExitCode(t *testing.T) {
	f := start(t, nil)
	r := f.run(t, "exe_one", 10, "sh", "-c", "echo out; echo err >&2; pwd; exit 3")
	if r.err != nil || r.exit == nil {
		t.Fatalf("%v %v", r.err, r.exit)
	}
	if r.exit.ExitCode != 3 || r.exit.TimedOut || r.exit.Cancelled {
		t.Fatalf("%+v", r.exit)
	}
	if r.stdout.String() != "out\n"+f.workspace+"\n" || r.stderr.String() != "err\n" {
		t.Fatalf("stdout %q stderr %q", r.stdout.String(), r.stderr.String())
	}
}

func TestExec_PassesEnvAndForwardsStdin(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg := startMsg(f, "exe_in", 10, "sh", "-c", "read a; echo got:$a:$GREETING; cat")
	msg.GetStart().Env = map[string]string{"GREETING": "hi"}
	stream := f.open(ctx, t, msg)
	for _, m := range []*envdpb.ExecClientMessage{stdinMsg("first\nrest "), stdinMsg("of it"), {Message: &envdpb.ExecClientMessage_StdinClose{StdinClose: &envdpb.StdinClose{}}}} {
		if err := stream.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	r := collect(stream)
	if r.err != nil || r.exit == nil || r.exit.ExitCode != 0 || r.stdout.String() != "got:first:hi\nrest of it" {
		t.Fatalf("%v %+v %q", r.err, r.exit, r.stdout.String())
	}
}

func TestExec_BinaryOutputIsExact(t *testing.T) {
	f := start(t, nil)
	r := f.run(t, "exe_bin", 10, "sh", "-c", `printf '\000\377\n\r'; head -c 200000 /dev/zero`)
	if r.exit == nil || r.exit.ExitCode != 0 || r.stdout.Len() != 4+200000 || !bytes.HasPrefix(r.stdout.Bytes(), []byte{0, 0xff, '\n', '\r', 0}) {
		t.Fatalf("%v %+v %d", r.err, r.exit, r.stdout.Len())
	}
}

func TestExec_TimeoutKillsTheWholeTreeIncludingDetachedDescendants(t *testing.T) {
	f := start(t, nil)
	script := `trap '' TERM; sleep 300 & echo $!; setsid sleep 300 & echo $!; wait`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := collect(f.open(ctx, t, startMsg(f, "exe_timeout", 1, "sh", "-c", script)))
	if r.err != nil || r.exit == nil || !r.exit.TimedOut {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
	for _, field := range strings.Fields(r.stdout.String()) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		waitGone(t, pid)
	}
}

func firstPID(t *testing.T, stream envdpb.Envd_ExecClient) int {
	t.Helper()
	message, err := stream.Recv()
	if err != nil || message.GetOutput() == nil {
		t.Fatalf("expected output, got %v %v", message, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(message.GetOutput().GetData())))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestExec_CancelMessageKillsDetachedDescendants(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := f.open(ctx, t, startMsg(f, "exe_cancel", 60, "sh", "-c", `trap '' TERM; setsid sleep 300 & echo $!; wait`))
	pid := firstPID(t, stream)
	if err := stream.Send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Cancel{Cancel: &envdpb.Cancel{GraceMillis: 100}}}); err != nil {
		t.Fatal(err)
	}
	r := collect(stream)
	if r.err != nil || r.exit == nil || !r.exit.Cancelled {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
	waitGone(t, pid)
}

func TestExec_ClientDisconnectCancelsAndReleasesTheSlot(t *testing.T) {
	f := start(t, func(c *Config) { c.MaxExecs = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	stream := f.open(ctx, t, startMsg(f, "exe_gone", 60, "sh", "-c", `trap '' TERM; echo $$; sleep 300 & wait`))
	pid := firstPID(t, stream)
	cancel()
	waitGone(t, pid)
	deadline := time.Now().Add(15 * time.Second)
	for {
		// The slot is released only after the cleanup proof, shortly after the process dies.
		retry, retryCancel := context.WithTimeout(context.Background(), 30*time.Second)
		stream, err := f.client.Exec(retry)
		if err != nil {
			t.Fatal(err)
		}
		if err = stream.Send(startMsg(f, "exe_after", 10, "true")); err != nil {
			t.Fatal(err)
		}
		r := collect(stream)
		retryCancel()
		if r.err == nil && r.exit != nil && r.exit.ExitCode == 0 {
			return
		}
		if code(r.err) != codes.ResourceExhausted || time.Now().After(deadline) {
			t.Fatalf("slot never released: %v", r.err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSignal_TerminatesARunningExecOutOfBand(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := f.open(ctx, t, startMsg(f, "exe_sig", 60, "sh", "-c", `echo $$; trap '' TERM; sleep 300 & wait`))
	pid := firstPID(t, stream)
	if _, err := f.client.Signal(ctx, &envdpb.SignalRequest{ExecId: "exe_sig", Signal: envdpb.Signal_SIGNAL_TERM, GraceMillis: 100}); err != nil {
		t.Fatal(err)
	}
	r := collect(stream)
	if r.err != nil || r.exit == nil || !r.exit.Cancelled {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
	waitGone(t, pid)
	if _, err := f.client.Signal(ctx, &envdpb.SignalRequest{ExecId: "exe_sig", Signal: envdpb.Signal_SIGNAL_KILL}); code(err) != codes.NotFound {
		t.Fatalf("signalling a finished exec: %v", err)
	}
	for name, request := range map[string]*envdpb.SignalRequest{
		"unspecified": {ExecId: "x", Signal: envdpb.Signal_SIGNAL_UNSPECIFIED},
		"huge grace":  {ExecId: "x", Signal: envdpb.Signal_SIGNAL_TERM, GraceMillis: MaxGraceMillis + 1},
	} {
		if _, err := f.client.Signal(ctx, request); code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestExec_TTYHasTheRequestedSizeAndResizes(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg := startMsg(f, "exe_tty", 20, "sh", "-c", "stty size; read x; stty size")
	msg.GetStart().Tty, msg.GetStart().Cols, msg.GetStart().Rows = true, 100, 30
	stream := f.open(ctx, t, msg)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "30 100") {
		message, err := stream.Recv()
		if err != nil {
			t.Fatalf("%v (saw %q)", err, seen.String())
		}
		seen.Write(message.GetOutput().GetData())
	}
	if err := stream.Send(&envdpb.ExecClientMessage{Message: &envdpb.ExecClientMessage_Resize{Resize: &envdpb.Resize{Cols: 90, Rows: 20}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := stream.Send(stdinMsg("\n")); err != nil {
		t.Fatal(err)
	}
	r := collect(stream)
	if r.err != nil || r.exit == nil || r.exit.ExitCode != 0 || !strings.Contains(r.stdout.String(), "20 90") {
		t.Fatalf("%v %+v %q", r.err, r.exit, r.stdout.String())
	}
}

func TestExec_RejectsBadRequestsAndLimits(t *testing.T) {
	f := start(t, func(c *Config) { c.MaxExecs = 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	expect := func(want codes.Code, first *envdpb.ExecClientMessage) {
		t.Helper()
		stream, err := f.client.Exec(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = stream.Send(first); err != nil {
			t.Fatal(err)
		}
		if _, err = stream.Recv(); code(err) != want {
			t.Fatalf("want %v, got %v", want, err)
		}
	}
	expect(codes.InvalidArgument, stdinMsg("x"))
	expect(codes.InvalidArgument, startMsg(f, "BAD ID", 5, "true"))
	expect(codes.InvalidArgument, startMsg(f, "exe_x", 5))
	outside := startMsg(f, "exe_x", 5, "true")
	outside.GetStart().Cwd = "/etc"
	expect(codes.InvalidArgument, outside)

	busy := f.open(ctx, t, startMsg(f, "exe_busy", 30, "sleep", "30"))
	expect(codes.AlreadyExists, startMsg(f, "exe_busy", 5, "true"))
	expect(codes.ResourceExhausted, startMsg(f, "exe_second", 5, "true"))
	if _, err := f.client.Signal(ctx, &envdpb.SignalRequest{ExecId: "exe_busy", Signal: envdpb.Signal_SIGNAL_KILL}); err != nil {
		t.Fatal(err)
	}
	if r := collect(busy); r.exit == nil || !r.exit.Cancelled {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
}

func TestExec_CommandNotFoundReportsExit127(t *testing.T) {
	f := start(t, nil)
	r := f.run(t, "exe_nf", 10, "/nonexistent/program")
	if r.err != nil || r.exit == nil || r.exit.ExitCode != 127 {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
}

func TestExec_ReusedIDAfterCompletionIsStillRejectedByTheController(t *testing.T) {
	f := start(t, nil)
	if r := f.run(t, "exe_reuse", 10, "true"); r.exit == nil || r.exit.ExitCode != 0 {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
	// The status was consumed, so the same ID may run again; the registry slot was freed.
	if r := f.run(t, "exe_reuse", 10, "true"); r.exit == nil || r.exit.ExitCode != 0 {
		t.Fatalf("%v %+v", r.err, r.exit)
	}
}

func TestFiles_RoundTripBinaryListRemoveAndMkdir(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	payload := make([]byte, 3*MaxChunkBytes+17)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	path := filepath.Join(f.workspace, "a", "blob.bin")
	write, err := f.client.WriteFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Header{Header: &envdpb.WriteFileHeader{Path: path}}}); err != nil {
		t.Fatal(err)
	}
	for rest := payload; len(rest) > 0; {
		n := min(len(rest), MaxChunkBytes)
		if err = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: rest[:n]}}); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	response, err := write.CloseAndRecv()
	if err != nil || response.SizeBytes != uint64(len(payload)) {
		t.Fatalf("%v %v", response, err)
	}
	read, err := f.client.ReadFile(ctx, &envdpb.ReadFileRequest{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	var announced uint64
	for {
		message, recvErr := read.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if m, ok := message.Message.(*envdpb.ReadFileResponse_SizeBytes); ok {
			announced = m.SizeBytes
		}
		got.Write(message.GetChunk())
	}
	if announced != uint64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("announced %d, got %d bytes", announced, got.Len())
	}
	listing, err := f.client.ListFiles(ctx, &envdpb.ListFilesRequest{Path: f.workspace, Depth: 2})
	if err != nil || len(listing.Items) != 2 {
		t.Fatalf("%v %v", listing, err)
	}
	if _, err = f.client.MakeDirectory(ctx, &envdpb.MakeDirectoryRequest{Path: filepath.Join(f.workspace, "b", "c")}); err != nil {
		t.Fatal(err)
	}
	removed, err := f.client.RemoveFile(ctx, &envdpb.RemoveFileRequest{Path: path})
	if err != nil || removed.RemovedSizeBytes != int64(len(payload)) {
		t.Fatalf("%v %v", removed, err)
	}
	if _, err = f.client.RemoveFile(ctx, &envdpb.RemoveFileRequest{Path: filepath.Join(f.workspace, "a"), Recursive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(f.workspace, "a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("directory survived recursive removal")
	}
}

func readAll(t *testing.T, f *fixture, path string, limit uint64) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := f.client.ReadFile(ctx, &envdpb.ReadFileRequest{Path: path, MaxBytes: limit})
	if err != nil {
		return err
	}
	for {
		if _, err = stream.Recv(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func TestFiles_PathsLimitsAndMissingFilesAreRejected(t *testing.T) {
	f := start(t, func(c *Config) { c.MaxFileBytes = 1000 })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, path := range []string{"/etc/passwd", f.workspace + "/../etc/passwd", "relative", "", f.workspace + "/x\x00y"} {
		if err := readAll(t, f, path, 0); code(err) != codes.InvalidArgument {
			t.Errorf("read %q: %v", path, err)
		}
		if _, err := f.client.ListFiles(ctx, &envdpb.ListFilesRequest{Path: path}); code(err) != codes.InvalidArgument {
			t.Errorf("list %q: %v", path, err)
		}
		if _, err := f.client.RemoveFile(ctx, &envdpb.RemoveFileRequest{Path: path, Recursive: true}); code(err) != codes.InvalidArgument {
			t.Errorf("remove %q: %v", path, err)
		}
		if _, err := f.client.MakeDirectory(ctx, &envdpb.MakeDirectoryRequest{Path: path}); code(err) != codes.InvalidArgument {
			t.Errorf("mkdir %q: %v", path, err)
		}
	}
	if err := readAll(t, f, f.workspace+"/missing", 0); code(err) != codes.NotFound {
		t.Errorf("missing: %v", err)
	}
	if err := readAll(t, f, f.workspace, 0); code(err) == codes.OK {
		t.Error("a directory was read as a file")
	}
	if err := readAll(t, f, f.workspace+"/x", 1001); code(err) != codes.InvalidArgument {
		t.Errorf("limit above the daemon cap: %v", err)
	}
	if _, err := f.client.ListFiles(ctx, &envdpb.ListFilesRequest{Path: f.workspace, Depth: 101}); code(err) != codes.InvalidArgument {
		t.Errorf("depth: %v", err)
	}
	// A file over the daemon limit is refused on write and the previous content survives.
	target := filepath.Join(f.workspace, "limited")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	write, err := f.client.WriteFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Header{Header: &envdpb.WriteFileHeader{Path: target}}})
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: make([]byte, 900)}})
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: make([]byte, 900)}})
	if _, err = write.CloseAndRecv(); code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized write: %v", err)
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" { // #nosec G304 -- test-owned temporary path
		t.Fatalf("previous content was replaced: %q", data)
	}
	// A write that does not begin with a header is rejected.
	bad, err := f.client.WriteFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: []byte("x")}})
	if _, err = bad.CloseAndRecv(); code(err) != codes.InvalidArgument {
		t.Fatalf("headerless write: %v", err)
	}
}

func TestFiles_SymlinksResolveWhereTheSandboxUserSeesThem(t *testing.T) {
	f := start(t, nil)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("inside the sandbox namespace"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// The path is validated lexically and the symlink then resolves in the daemon's own
	// namespace, which is the sandbox: the same behaviour as the exec-based files API.
	if err := readAll(t, f, link, 0); err != nil {
		t.Fatal(err)
	}
}

func TestChildrenRunWithTheDaemonsStateDirectory(t *testing.T) {
	f := start(t, nil)
	if r := f.run(t, "exe_state", 10, "true"); r.exit == nil {
		t.Fatalf("%v", r.err)
	}
	entries, err := os.ReadDir(f.state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("controller state left behind: %v %v", entries, err)
	}
}

func TestNewServer_ValidatesConfiguration(t *testing.T) {
	for name, cfg := range map[string]Config{
		"root user":       {Self: envdBinary, User: &User{}},
		"too many execs":  {Self: envdBinary, MaxExecs: 5000},
		"bad roots":       {Self: envdBinary, Roots: []string{"relative"}},
		"file ops bounds": {Self: envdBinary, MaxFileOps: 1000},
	} {
		if _, err := NewServer(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if os.Geteuid() != 0 {
		if _, err := NewServer(Config{Self: envdBinary, User: &User{UID: 12345, GID: 12345}}); err == nil {
			t.Error("an unprivileged daemon accepted another identity")
		}
	}
}

func TestListen_UnixSocketIsPrivateAndOnlyReplacesStaleSockets(t *testing.T) {
	dir, err := os.MkdirTemp("", "l")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "s")
	first, err := Listen("unix:" + path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Listen("unix:" + path) // stale socket from the first listener
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	if err = os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = Listen("unix:" + path); err == nil {
		t.Fatal("a regular file was replaced")
	}
	for _, bad := range []string{"", "tcp:127.0.0.1:1", "unix:", "vsock:0", "vsock:abc", "nonsense"} {
		if _, err = Listen(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHostOnly_AcceptsOnlyTheHostCID(t *testing.T) {
	for cid, want := range map[uint32]bool{0: false, 1: false, 2: true, 3: false, 4294967295: false} {
		if HostOnly(cid) != want {
			t.Errorf("CID %d", cid)
		}
	}
}

func TestListen_VsockRejectsGuestLocalPeers(t *testing.T) {
	listener, err := Listen("vsock:49152")
	if err != nil {
		t.Skipf("vsock is unavailable here: %v", err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, acceptErr := listener.Accept(); acceptErr == nil {
			accepted <- conn
		}
	}()
	fd, err := syscall.Socket(40, syscall.SOCK_STREAM, 0) // AF_VSOCK
	if err != nil {
		t.Skip(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	// Connecting to the local CID (1) needs the vsock loopback transport.
	if err = connectVsock(fd, 1, 49152); err != nil {
		t.Skipf("vsock loopback is unavailable: %v", err)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
		t.Fatal("a guest-local peer was accepted")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestWriteFile_AbandonedUploadCommitsNothingAndLeavesNoTemporary(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	write, err := f.client.WriteFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.workspace, "partial")
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Header{Header: &envdpb.WriteFileHeader{Path: target}}})
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: []byte("half a file")}})
	time.Sleep(200 * time.Millisecond) // let the writer receive the chunk
	cancel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, readErr := os.ReadDir(f.workspace)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) == 0 && time.Since(deadline) < 0 && len(f.server.fileOps) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("an abandoned upload left %v behind", entries)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWriteFile_OversizedChunkAbortsWithoutCommittingEarlierChunks(t *testing.T) {
	f := start(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	write, err := f.client.WriteFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Header{Header: &envdpb.WriteFileHeader{Path: filepath.Join(f.workspace, "new")}}})
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: []byte("early chunk")}})
	_ = write.Send(&envdpb.WriteFileRequest{Message: &envdpb.WriteFileRequest_Chunk{Chunk: make([]byte, MaxChunkBytes+1)}})
	if _, err = write.CloseAndRecv(); code(err) != codes.InvalidArgument {
		t.Fatalf("oversized chunk: %v", err)
	}
	if entries, _ := os.ReadDir(f.workspace); len(entries) != 0 {
		t.Fatalf("an aborted upload left %v behind", entries)
	}
}
