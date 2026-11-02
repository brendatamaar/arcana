package orchestrator

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/config"
)

func TestResolveExec_SupportsDocumentedEntryLimits(t *testing.T) {
	args := make([]string, 256)
	for i := range args {
		args[i] = "argument"
	}
	request := ExecRequest{Cmd: &args, Env: make(map[string]string)}
	for i := range 100 {
		request.Env["ENV_"+strconv.Itoa(i)] = "value"
	}
	plan, err := resolveExec(config.Default(), request)
	if err != nil || len(plan.argv) != 256 || len(plan.request("exe_aaaaaaaaaaaaaaaaaaaa").Cmd) != 265 {
		t.Fatal("documented argument/env limits rejected", err)
	}
	args = append(args, "extra")
	if _, err = resolveExec(config.Default(), request); err == nil {
		t.Fatal("argument cap bypassed")
	}
	args = args[:256]
	request.Env["EXTRA"] = "value"
	if _, err = resolveExec(config.Default(), request); err == nil {
		t.Fatal("env cap bypassed")
	}
}

func FuzzExecStep_CompletionRequiresProofAndCancellationBarrier(f *testing.F) {
	f.Add([]byte{2, 1, 0})
	f.Add([]byte{2, 0, 1})
	f.Add([]byte{4, 0})
	f.Add([]byte{3, 0})
	f.Add([]byte{0, 2, 4})
	f.Fuzz(func(t *testing.T, events []byte) {
		if len(events) > 1000 {
			t.Skip()
		}
		var state execState = execRunning{}
		completed, cancelled, cancelDone, terminal := false, false, false, false
		for _, value := range events {
			var input execInput
			switch value % 5 {
			case 0:
				input = commandCompleted{}
				completed = true
			case 1:
				input = cancellationCompleted{}
				if cancelled {
					cancelDone = true
				}
			case 2:
				input = clientCancelled{}
				cancelled = true
			case 3:
				input = helperFailed{}
			case 4:
				input = hardDeadline{}
			}
			next, commands := execStep(state, input)
			if terminal && len(commands) != 0 {
				t.Fatal("terminal state produced effects")
			}
			for _, command := range commands {
				switch effect := command.(type) {
				case persistExec:
					if !completed || cancelled && !cancelDone {
						t.Fatal("persisted without completed cleanup or cancellation barrier")
					}
					terminal = true
				case destroyExecSandbox:
					if effect.reason != "exec_deadline" && effect.reason != "exec_uncertain" {
						t.Fatal("unknown destroy reason")
					}
					terminal = true
				case cancelHelper:
					if _, ok := state.(execRunning); !ok {
						t.Fatal("duplicate cancel")
					}
				case scheduleCancelDeadline:
					if effect.after.Seconds() != 10 {
						t.Fatal("unbounded cancellation")
					}
				}
			}
			state = next
		}
	})
}

func FuzzResolveExec_AcceptedInputsAreBounded(f *testing.F) {
	f.Add("command", "/workspace", int64(1))
	f.Add("\x00", "/etc", int64(-1))
	f.Add("arg", "/tmp/a", int64(3600))
	f.Fuzz(func(t *testing.T, arg, cwd string, timeout int64) {
		if len(arg) > 4096 || len(cwd) > 4096 {
			t.Skip()
		}
		args := []string{arg}
		plan, err := resolveExec(config.Default(), ExecRequest{Cmd: &args, Cwd: &cwd, TimeoutSeconds: &timeout})
		if err != nil {
			return
		}
		if plan.timeout < 1 || plan.timeout > 3600 || len(plan.argv) != 1 || plan.argv[0] == "" || strings.ContainsRune(plan.argv[0], 0) {
			t.Fatal("invalid accepted exec")
		}
		if plan.cwd != "/workspace" && !strings.HasPrefix(plan.cwd, "/workspace/") && plan.cwd != "/tmp" && !strings.HasPrefix(plan.cwd, "/tmp/") {
			t.Fatal("cwd escaped roots")
		}
		for _, part := range strings.Split(plan.cwd, "/") {
			if part == ".." {
				t.Fatal("traversal accepted")
			}
		}
	})
}

func FuzzParseStatus_AcceptedCompletionHasProof(f *testing.F) {
	f.Add([]byte(`{"id":"exe_test","exit_code":0,"cleanup_complete":true}`), uint8(0))
	f.Add([]byte(`{"id":"exe_test","exit_code":7,"cleanup_complete":true}`), uint8(124))
	f.Add([]byte(`{"id":"exe_test","exit_code":0}`), uint8(0))
	f.Fuzz(func(t *testing.T, data []byte, transport uint8) {
		if len(data) > 5000 {
			t.Skip()
		}
		status, err := parseStatus(data, "exe_test", int(transport))
		if err != nil {
			return
		}
		var object map[string]json.RawMessage
		if err = json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		var cleanup bool
		if err = json.Unmarshal(object["cleanup_complete"], &cleanup); err != nil {
			t.Fatal("missing cleanup proof")
		}
		if status.ID != "exe_test" || !cleanup || status.ExitCode < 0 || status.ExitCode > 255 || len(data) > 4096 {
			t.Fatal("unproved completion accepted")
		}
		if status.ExitCode == 0 && transport != 0 || status.ExitCode != 0 && transport != 124 {
			t.Fatal("transport failure accepted as command result")
		}
	})
}
