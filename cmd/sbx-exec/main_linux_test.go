package main

import "testing"

func TestID_RejectsUnsafePaths(t *testing.T) {
	for _, value := range []string{"", "../bad", "/tmp/bad", "x.y", "x\x00", "A"} {
		if validID(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	if !validID("exe_abc123") {
		t.Fatal("rejected valid ID")
	}
}

func TestRun_RejectsMissingCommandAndInvalidTimeout(t *testing.T) {
	for _, args := range [][]string{{}, {"run", "--id", "exe_test"}, {"run", "--id", "exe_test", "--timeout", "0", "--", "true"}, {"kill", "--id", "exe_test", "--grace", "11s"}, {"invalid", "--id", "exe_test"}} {
		if _, err := execute(args, t.TempDir()); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
