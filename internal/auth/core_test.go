package auth

import "testing"

func TestBootstrapModel_IgnoresLateAndUnexpectedEvents(t *testing.T) {
	for _, input := range []bootstrapInput{fileWritten, keyCommitted, keyPrinted} {
		state, commands := step(bootstrapStart, input)
		if state != bootstrapStart || len(commands) != 0 {
			t.Fatal("unexpected event caused an effect")
		}
	}
	if got := run([]bootstrapInput{databaseEmpty, fileWritten, keyCommitted, keyPrinted}); got != bootstrapComplete {
		t.Fatal("happy path did not complete")
	}
	state, commands := step(bootstrapCommit, operationFailed)
	if state != bootstrapFailed || len(commands) != 1 || commands[0] != removeAdminFile {
		t.Fatal("failed commit must remove uncommitted file")
	}
	state, commands = step(bootstrapPrint, operationFailed)
	if state != bootstrapFailed || len(commands) != 0 {
		t.Fatal("committed file must survive output error")
	}
}

func FuzzBootstrapModel_PrintAtMostOnceAfterCommit(f *testing.F) {
	f.Add([]byte{0, 2, 3, 4, 0, 2, 3, 4})
	f.Add([]byte{1, 0, 2, 3, 4})
	f.Add([]byte{0, 2, 5, 3, 4})
	f.Fuzz(func(t *testing.T, events []byte) {
		if len(events) > 1000 {
			t.Skip()
		}
		state := bootstrapStart
		prints := 0
		committed := false
		inputs := make([]bootstrapInput, 0, len(events))
		for _, b := range events {
			input := bootstrapInput(b % 6)
			inputs = append(inputs, input)
			before := state
			if before == bootstrapCommit && input == keyCommitted {
				committed = true
			}
			var commands []bootstrapCommand
			state, commands = step(state, input)
			for _, command := range commands {
				if command == printAdminKey {
					prints++
					if !committed || prints > 1 {
						t.Fatal("printed before commit or more than once")
					}
				}
				if command == removeAdminFile && committed {
					t.Fatal("removed committed credential")
				}
			}
			if (before == bootstrapComplete || before == bootstrapFailed) && (state != before || len(commands) != 0) {
				t.Fatal("terminal state produced side effect")
			}
		}
		if run(inputs) != state {
			t.Fatal("replay differs")
		}
	})
}
