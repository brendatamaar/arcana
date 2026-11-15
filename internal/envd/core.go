// Package envd implements the in-sandbox gRPC daemon. Decisions (request validation,
// completion proof, error mapping) live in this file as pure functions; the effects
// (processes, pipes, sockets) live in the other files.
package envd

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/pathutil"
	"github.com/brendatamaar/arcana/internal/sbxexec"
)

// Limits shared with the host-side orchestrator so the daemon enforces the same bounds.
const (
	MaxArgs         = 256
	MaxArgBytes     = 120000
	MaxEnvEntries   = 100
	MaxEnvBytes     = 32768
	MaxChunkBytes   = 64 * 1024
	MaxGraceMillis  = 10000
	DefaultGrace    = 2000
	MaxTTYDimension = 4096
	defaultCwd      = "/workspace"
)

// ExecSpec is a validated start request.
type ExecSpec struct {
	ID             string
	Argv           []string
	Cwd            string
	Env            []string // KEY=value pairs in a stable order
	TimeoutSeconds int
	TTY            bool
	Cols, Rows     uint16
}

// ParseStart validates a start message at the trust boundary and returns a spec that
// later code can use without re-checking.
func ParseStart(start *envdpb.ExecStart, roots []string) (ExecSpec, error) {
	if start == nil {
		return ExecSpec{}, errors.New("first message must be start")
	}
	if !sbxexec.ValidID(start.GetExecId()) {
		return ExecSpec{}, errors.New("invalid exec_id")
	}
	argv := start.GetArgv()
	if len(argv) == 0 || len(argv) > MaxArgs || argv[0] == "" {
		return ExecSpec{}, errors.New("argv must hold 1..256 entries with a non-empty program")
	}
	size := 0
	for _, arg := range argv {
		size += len(arg)
		if strings.ContainsRune(arg, 0) || size > MaxArgBytes {
			return ExecSpec{}, errors.New("invalid argv")
		}
	}
	cwd := start.GetCwd()
	if cwd == "" {
		cwd = defaultCwd
	}
	cwd, err := pathutil.Clean(cwd, roots)
	if err != nil {
		return ExecSpec{}, fmt.Errorf("invalid cwd: %w", err)
	}
	timeout := int(start.GetTimeoutSeconds())
	if timeout < 1 || timeout > protocol.MaxTimeoutSeconds {
		return ExecSpec{}, errors.New("timeout_seconds must be 1..3600")
	}
	env, err := parseEnv(start.GetEnv())
	if err != nil {
		return ExecSpec{}, err
	}
	spec := ExecSpec{ID: start.GetExecId(), Argv: slices.Clone(argv), Cwd: cwd, Env: env, TimeoutSeconds: timeout, TTY: start.GetTty()}
	if spec.TTY {
		if spec.Cols, spec.Rows, err = parseSize(start.GetCols(), start.GetRows()); err != nil {
			return ExecSpec{}, err
		}
	}
	return spec, nil
}

func parseEnv(values map[string]string) ([]string, error) {
	if len(values) > MaxEnvEntries {
		return nil, errors.New("too many env entries")
	}
	total := 0
	keys := make([]string, 0, len(values))
	for key, value := range values {
		total += len(key) + len(value)
		if key == "" || len(key) > 128 || len(value) > 4096 || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid env entry")
		}
		keys = append(keys, key)
	}
	if total > MaxEnvBytes {
		return nil, errors.New("env exceeds size limit")
	}
	slices.Sort(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+values[key])
	}
	return pairs, nil
}

func parseSize(cols, rows uint32) (uint16, uint16, error) {
	if cols == 0 || rows == 0 || cols > MaxTTYDimension || rows > MaxTTYDimension {
		return 0, 0, errors.New("cols and rows must be 1..4096")
	}
	return uint16(cols), uint16(rows), nil
}

// ParseGrace converts a client grace to a bounded duration in milliseconds.
func ParseGrace(millis uint32, useDefault bool) (int, error) {
	if millis == 0 && useDefault {
		return DefaultGrace, nil
	}
	if millis > MaxGraceMillis {
		return 0, errors.New("grace_millis must be at most 10000")
	}
	return int(millis), nil
}

// Completion is the proven end of an exec.
type Completion struct {
	ExitCode  int
	Signal    string
	TimedOut  bool
	Cancelled bool
}

// ProveCompletion applies the host acceptance rule of PRD §9.4 inside the daemon: the
// status must carry this exec's ID and the cleanup marker, and the controller's
// transport exit must agree with it. Anything else means cleanup is unproven and the
// caller must report an error so the host destroys the sandbox.
func ProveCompletion(id string, status protocol.Status, transportExit int) (Completion, error) {
	if status.ID != id || !status.CleanupComplete {
		return Completion{}, errors.New("exec status lacks cleanup proof")
	}
	if transportExit != protocol.TransportExit(status.ExitCode) {
		return Completion{}, errors.New("controller exit disagrees with exec status")
	}
	return Completion{ExitCode: status.ExitCode, Signal: status.Signal, TimedOut: status.TimedOut, Cancelled: status.Cancelled}, nil
}

// FileCode maps a file-utility exit code to a gRPC code.
func FileCode(exit int) codes.Code {
	switch exit {
	case 0:
		return codes.OK
	case protocol.FileNotFound:
		return codes.NotFound
	case protocol.FileDenied:
		return codes.PermissionDenied
	case protocol.FileInvalid:
		return codes.InvalidArgument
	case protocol.FileTooLarge, protocol.FileNoSpace:
		return codes.ResourceExhausted
	default:
		return codes.Internal
	}
}
