package envd

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
)

var testRoots = []string{"/workspace", "/tmp"}

func validStart() *envdpb.ExecStart {
	return &envdpb.ExecStart{ExecId: "exe_abc123", Argv: []string{"echo", "hi"}, TimeoutSeconds: 10}
}

func TestParseStart_AcceptsValidAndAppliesDefaults(t *testing.T) {
	spec, err := ParseStart(validStart(), testRoots)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Cwd != "/workspace" || spec.TimeoutSeconds != 10 || spec.TTY {
		t.Fatalf("unexpected spec %+v", spec)
	}
	start := validStart()
	start.Env = map[string]string{"B": "2", "A": "1"}
	start.Tty, start.Cols, start.Rows, start.Cwd = true, 80, 24, "/tmp//x"
	spec, err = ParseStart(start, testRoots)
	if err != nil || spec.Cwd != "/tmp/x" || spec.Cols != 80 || spec.Rows != 24 || strings.Join(spec.Env, ",") != "A=1,B=2" {
		t.Fatalf("unexpected spec %+v %v", spec, err)
	}
}

func TestParseStart_RejectsEveryInvalidField(t *testing.T) {
	long := strings.Repeat("a", 4097)
	tooMany := make(map[string]string)
	for i := range MaxEnvEntries + 1 {
		tooMany[strings.Repeat("k", 3)+string(rune('A'+i%26))+strings.Repeat("x", i)] = "v"
	}
	for name, mutate := range map[string]func(*envdpb.ExecStart){
		"missing id":       func(s *envdpb.ExecStart) { s.ExecId = "" },
		"uppercase id":     func(s *envdpb.ExecStart) { s.ExecId = "EXE" },
		"long id":          func(s *envdpb.ExecStart) { s.ExecId = strings.Repeat("a", 65) },
		"path id":          func(s *envdpb.ExecStart) { s.ExecId = "../x" },
		"empty argv":       func(s *envdpb.ExecStart) { s.Argv = nil },
		"empty program":    func(s *envdpb.ExecStart) { s.Argv = []string{""} },
		"nul in argv":      func(s *envdpb.ExecStart) { s.Argv = []string{"a\x00b"} },
		"too many args":    func(s *envdpb.ExecStart) { s.Argv = make([]string, MaxArgs+1) },
		"argv bytes":       func(s *envdpb.ExecStart) { s.Argv = []string{strings.Repeat("a", MaxArgBytes+1)} },
		"cwd outside":      func(s *envdpb.ExecStart) { s.Cwd = "/etc" },
		"cwd traversal":    func(s *envdpb.ExecStart) { s.Cwd = "/workspace/../etc" },
		"cwd relative":     func(s *envdpb.ExecStart) { s.Cwd = "workspace" },
		"cwd long":         func(s *envdpb.ExecStart) { s.Cwd = "/workspace/" + long },
		"zero timeout":     func(s *envdpb.ExecStart) { s.TimeoutSeconds = 0 },
		"huge timeout":     func(s *envdpb.ExecStart) { s.TimeoutSeconds = 3601 },
		"env empty key":    func(s *envdpb.ExecStart) { s.Env = map[string]string{"": "v"} },
		"env equals key":   func(s *envdpb.ExecStart) { s.Env = map[string]string{"A=B": "v"} },
		"env nul value":    func(s *envdpb.ExecStart) { s.Env = map[string]string{"A": "v\x00"} },
		"env long value":   func(s *envdpb.ExecStart) { s.Env = map[string]string{"A": long} },
		"env too many":     func(s *envdpb.ExecStart) { s.Env = tooMany },
		"tty without size": func(s *envdpb.ExecStart) { s.Tty = true },
		"tty huge size":    func(s *envdpb.ExecStart) { s.Tty, s.Cols, s.Rows = true, 5000, 24 },
	} {
		start := validStart()
		mutate(start)
		if _, err := ParseStart(start, testRoots); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseStart(nil, testRoots); err == nil {
		t.Error("nil start accepted")
	}
}

func TestProveCompletion_RequiresIDCleanupAndAgreeingTransport(t *testing.T) {
	good := protocol.Status{ID: "e1", ExitCode: 3, CleanupComplete: true, TimedOut: true}
	got, err := ProveCompletion("e1", good, 124)
	if err != nil || got.ExitCode != 3 || !got.TimedOut {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = ProveCompletion("e1", protocol.Status{ID: "e1", CleanupComplete: true}, 0); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		status    protocol.Status
		transport int
	}{
		"other id":         {protocol.Status{ID: "e2", CleanupComplete: true}, 0},
		"no cleanup":       {protocol.Status{ID: "e1"}, 0},
		"zero vs 124":      {protocol.Status{ID: "e1", CleanupComplete: true}, 124},
		"nonzero vs 0":     {protocol.Status{ID: "e1", ExitCode: 1, CleanupComplete: true}, 0},
		"helper fault":     {protocol.Status{ID: "e1", CleanupComplete: true}, 125},
		"killed by signal": {protocol.Status{ID: "e1", CleanupComplete: true}, -1},
	} {
		if _, err := ProveCompletion("e1", test.status, test.transport); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFileCode_MapsEveryHelperExit(t *testing.T) {
	for exit, want := range map[int]codes.Code{
		0: codes.OK, protocol.FileNotFound: codes.NotFound, protocol.FileDenied: codes.PermissionDenied,
		protocol.FileInvalid: codes.InvalidArgument, protocol.FileTooLarge: codes.ResourceExhausted,
		protocol.FileNoSpace: codes.ResourceExhausted, 1: codes.Internal, 125: codes.Internal,
	} {
		if got := FileCode(exit); got != want {
			t.Errorf("exit %d: %v, want %v", exit, got, want)
		}
	}
}

func TestParseGrace_BoundsTheGrace(t *testing.T) {
	if got, err := ParseGrace(0, true); err != nil || got != DefaultGrace {
		t.Fatal(got, err)
	}
	if got, err := ParseGrace(0, false); err != nil || got != 0 {
		t.Fatal(got, err)
	}
	if got, err := ParseGrace(MaxGraceMillis, false); err != nil || got != MaxGraceMillis {
		t.Fatal(got, err)
	}
	if _, err := ParseGrace(MaxGraceMillis+1, false); err == nil {
		t.Fatal("unbounded grace accepted")
	}
}
