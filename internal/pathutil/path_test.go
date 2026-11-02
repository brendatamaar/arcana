package pathutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestClean_ContainmentTraversalAndRouterDecodedPaths(t *testing.T) {
	roots := []string{"/workspace", "/tmp"}
	for _, test := range []struct {
		value, want string
		valid       bool
	}{
		{"/workspace", "/workspace", true},
		{"/workspace/a//./b", "/workspace/a/b", true},
		{"/tmp/a b", "/tmp/a b", true},
		{"relative", "", false},
		{"/workspace-other/file", "", false},
		{"/etc/passwd", "", false},
		{"/workspace/../etc/passwd", "", false},
		{"/workspace/dir/../../tmp", "", false},
		{"/workspace/\x00bad", "", false},
		{"", "", false},
	} {
		got, err := Clean(test.value, roots)
		if (err == nil) != test.valid || got != test.want {
			t.Fatalf("%q: %q %v", test.value, got, err)
		}
	}
	for _, query := range []string{"path=%2Fworkspace%2F..%2Fetc%2Fpasswd", "path=%2Fworkspace%2F%00bad", "path=%2Fworkspace-other%2Ffile"} {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Clean(values.Get("path"), roots); err == nil {
			t.Fatal("router-decoded escape accepted", query)
		}
	}
	if _, err := Clean("/workspace/"+strings.Repeat("a", 4096), roots); err == nil {
		t.Fatal("unbounded path")
	}
	if _, err := Clean("/workspace", []string{"relative"}); err == nil {
		t.Fatal("invalid root accepted")
	}
}

func FuzzClean_AcceptedPathsRemainInRoots(f *testing.F) {
	for _, value := range []string{"/workspace/a", "/tmp/b", "/workspace/../etc", "/workspace-other/a", "/workspace/a//./b"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 5000 {
			t.Skip()
		}
		clean, err := Clean(value, []string{"/workspace", "/tmp"})
		if err != nil {
			return
		}
		if len(clean) > 4096 || strings.ContainsRune(clean, 0) {
			t.Fatal("unbounded/invalid accepted path")
		}
		if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") && clean != "/tmp" && !strings.HasPrefix(clean, "/tmp/") {
			t.Fatal("escaped roots")
		}
		for _, component := range strings.Split(clean, "/") {
			if component == ".." {
				t.Fatal("traversal survived")
			}
		}
	})
}
