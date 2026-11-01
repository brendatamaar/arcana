package protocol

import "testing"

func TestTransportExit_SeparatesCompletionFromControllerFailure(t *testing.T) {
	for code := 0; code <= 255; code++ {
		got := TransportExit(code)
		if code == 0 && got != 0 || code != 0 && got != 124 {
			t.Fatalf("command %d has invalid transport %d", code, got)
		}
		if got == 125 || got >= 128 || got == 2 {
			t.Fatal("helper fault/signal overlaps completed command")
		}
	}
}
