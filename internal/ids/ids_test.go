package ids

import (
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"
)

func TestNew_FormatAndUniqueness(t *testing.T) {
	pattern := regexp.MustCompile(`^(sbx|exe|snp|key|aud|ses|req)_[a-z2-7]{20}$`)
	seen := map[string]bool{}
	for _, prefix := range []string{"sbx", "exe", "snp", "key", "aud", "ses", "req"} {
		for range 1000 {
			id, err := New(prefix)
			if err != nil {
				t.Fatal(err)
			}
			if !pattern.MatchString(id) || !strings.HasPrefix(id, prefix+"_") || seen[id] {
				t.Fatalf("invalid or repeated ID: %q", id)
			}
			seen[id] = true
		}
	}
}

func TestGenerate_FailsClosed(t *testing.T) {
	if _, err := generate(strings.NewReader(""), "req"); !errors.Is(err, io.EOF) {
		t.Fatalf("want random read failure: %v", err)
	}
	if _, err := New("invalid"); err == nil {
		t.Fatal("accepted unsupported prefix")
	}
}
