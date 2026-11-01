package runtime

import (
	"strings"
	"testing"
)

func TestErrors_DescribeSafeIdentifiers(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{{NotFound{ID: "instance"}, "instance"}, {Invalid{Field: "cmd"}, "cmd"}, {Unsupported{Feature: "TTY"}, "TTY"}} {
		if !strings.Contains(tt.err.Error(), tt.want) {
			t.Fatalf("missing error context: %v", tt.err)
		}
	}
}
