package docker

import (
	"slices"
	"testing"
)

func TestCommandArgLimit_OnlyCanonicalHelperOverheadIsAdditional(t *testing.T) {
	prefix := []string{"/opt/arcana/sbx-exec", "run", "--id", "exe_abc", "--timeout", "120", "--cwd", "/workspace", "--"}
	args := append(slices.Clone(prefix), make([]string, 256)...)
	if commandArgLimit(args) != len(args) {
		t.Fatal("256 documented user arguments rejected after wrapping")
	}
	tty := append(slices.Clone(prefix[:8]), "--tty", "--")
	tty = append(tty, make([]string, 256)...)
	if commandArgLimit(tty) != len(tty) {
		t.Fatal("TTY overhead counted as user arguments")
	}
	if commandArgLimit(make([]string, 257)) != 256 {
		t.Fatal("raw argv cap changed")
	}
	for _, change := range []struct {
		index int
		value string
	}{{0, "/workspace/fake"}, {1, "status"}, {2, "--other"}, {3, ""}, {4, "--other"}, {5, "3601"}, {6, "--other"}, {7, "relative"}, {8, "other"}} {
		bad := slices.Clone(args)
		bad[change.index] = change.value
		if commandArgLimit(bad) != 256 {
			t.Fatalf("invalid wrapper granted extra arguments: %d", change.index)
		}
	}
}
