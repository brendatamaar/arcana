package orchestrator

import "testing"

func FuzzStep_TerminalStatesNeverRestart(f *testing.F) {
	f.Add([]byte{0, 2, 3, 0, 1, 5})
	f.Add([]byte{1, 0})
	f.Add([]byte{0, 2, 4})
	f.Fuzz(func(t *testing.T, events []byte) {
		if len(events) > 1000 {
			t.Skip()
		}
		state := Creating
		for _, value := range events {
			before := state
			next, commands := Step(state, Event(value%6))
			changed := len(commands) > 0
			if changed && (len(commands) != 1 || commands[0].From != before || commands[0].To != next) {
				t.Fatal("incorrect persistence command")
			}
			if !next.Valid() {
				t.Fatal("invalid state")
			}
			if terminal(before) && (changed || next != before) {
				t.Fatal("terminal state restarted")
			}
			if changed && next == before {
				t.Fatal("transition duplicated")
			}
			if next == Running && before != Running && before != Creating {
				t.Fatal("running without creation")
			}
			if (next == Destroyed || next == Expired) && next != before && before != Destroying {
				t.Fatal("ended without cleanup")
			}
			state = next
		}
	})
}
