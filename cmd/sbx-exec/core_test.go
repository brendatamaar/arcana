package main

import (
	"reflect"
	"testing"
	"time"
)

func TestStep_RequiresReapedTreeBeforeCompletion(t *testing.T) {
	state, commands := Step(Running{}, TreeEmpty{})
	if _, ok := state.(Running); !ok || len(commands) != 0 {
		t.Fatal("completed before main process exit")
	}
	state, commands = Step(state, MainExited{Exit: exitData{Code: 7}})
	if _, ok := state.(Draining); !ok || len(commands) != 1 {
		t.Fatal("main exit skipped descendant cleanup")
	}
	if _, ok := commands[0].(SignalTree); !ok {
		t.Fatal("missing tree kill")
	}
	state, commands = Step(state, TreeEmpty{})
	done, ok := state.(Finished)
	if !ok || done.Outcome.Exit.Code != 7 || len(commands) != 1 {
		t.Fatal("lost exit status or cleanup proof")
	}
	if _, ok := commands[0].(PersistOutcome); !ok {
		t.Fatal("status persistence missing")
	}
}

func TestStep_StopReasonAndGraceAreBoundedInputs(t *testing.T) {
	state, commands := Step(Running{}, CancellationRequested{Grace: 200 * time.Millisecond})
	if len(commands) != 2 {
		t.Fatal("missing TERM/grace actions")
	}
	if signal, ok := commands[0].(SignalTree); !ok || signal.Signal != terminateTree {
		t.Fatal("missing graceful termination")
	}
	state, commands = Step(state, GraceExpired{})
	if signal, ok := commands[0].(SignalTree); !ok || signal.Signal != killTree {
		t.Fatal("grace did not escalate")
	}
	state, _ = Step(state, DeadlineExpired{})
	state, _ = Step(state, MainExited{Exit: exitData{Code: 137, Signal: "killed"}})
	state, _ = Step(state, TreeEmpty{})
	if state.(Finished).Outcome.Reason != timeoutExit {
		t.Fatal("deadline was lost")
	}
}

func FuzzStep_CompletionRequiresExitAndKernelProof(f *testing.F) {
	f.Add([]byte{0, 4})
	f.Add([]byte{1, 0, 5, 4})
	f.Add([]byte{2, 3, 0, 4, 0, 1, 2})
	f.Fuzz(func(t *testing.T, events []byte) {
		if len(events) > 1000 {
			t.Skip()
		}
		var state State = Running{}
		history := make([]Input, 0, len(events))
		mainExited := false
		for _, b := range events {
			var input Input
			switch b % 6 {
			case 0:
				input = MainExited{Exit: exitData{Code: 7}}
				mainExited = true
			case 1:
				input = DeadlineExpired{}
			case 2:
				input = CancellationRequested{Grace: time.Millisecond}
			case 3:
				input = GraceExpired{}
			case 4:
				input = TreeEmpty{}
			case 5:
				input = TreePresent{}
			}
			before := state
			history = append(history, input)
			var commands []Command
			state, commands = Step(state, input)
			for _, command := range commands {
				if _, ok := command.(PersistOutcome); ok {
					if !mainExited {
						t.Fatal("persisted before main exit")
					}
					if _, ok := input.(TreeEmpty); !ok {
						t.Fatal("persisted without kernel cleanup proof")
					}
				}
			}
			if _, done := before.(Finished); done {
				if !reflect.DeepEqual(before, state) || len(commands) != 0 {
					t.Fatal("terminal state changed")
				}
			}
		}
		if !reflect.DeepEqual(Run(history), state) {
			t.Fatal("replay differs")
		}
	})
}
