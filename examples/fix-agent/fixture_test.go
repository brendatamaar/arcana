package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseArchive(data []byte) (map[string]string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(tr)
		out[header.Name] = string(body)
	}
}

func readArchive(t *testing.T, data []byte) map[string]string {
	t.Helper()
	files, err := parseArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLoadFixtures_ShippedFixturesAreCompleteAndDistinct(t *testing.T) {
	fixtures, err := LoadFixtures("../fixtures")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 10 {
		t.Fatalf("want 10 fixtures, got %d", len(fixtures))
	}
	languages := map[string]int{}
	names := map[string]bool{}
	for _, f := range fixtures {
		languages[f.Language]++
		if names[f.Name] || !strings.HasPrefix(f.Name, map[string]string{"python": "py-", "node": "js-", "go": "go-"}[f.Language]) {
			t.Errorf("%s: bad or duplicate name for language %s", f.Name, f.Language)
		}
		names[f.Name] = true
		if f.Template != f.Language || f.VisibleTest == f.HiddenTest {
			t.Errorf("%s: template/test commands", f.Name)
		}
		project, err := f.Archive("project")
		if err != nil {
			t.Fatal(err)
		}
		files := readArchive(t, project)
		if _, ok := files["README.md"]; !ok {
			t.Errorf("%s: project needs a README describing the intended behaviour", f.Name)
		}
		// The agent's project must not contain the hidden tests or the answer.
		for name, body := range files {
			if strings.Contains(name, "hidden") || strings.Contains(body, "TestHidden") {
				t.Errorf("%s: project leaks hidden tests via %s", f.Name, name)
			}
		}
		hidden, err := f.Archive("hidden")
		if err != nil {
			t.Fatal(err)
		}
		for name := range readArchive(t, hidden) {
			if strings.HasSuffix(name, stripSuffix) {
				t.Errorf("%s: %s kept the %s suffix", f.Name, name, stripSuffix)
			}
		}
	}
	if languages["python"] != 4 || languages["node"] != 3 || languages["go"] != 3 {
		t.Fatalf("language mix: %v", languages)
	}
}

func TestArchive_StripsSuffixIsDeterministicAndRejectsUnsafeContent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		path := filepath.Join(dir, "fx", "project", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a_test.go.fixture", "package x")
	write("sub/b.txt", "b")
	f := Fixture{Dir: filepath.Join(dir, "fx")}
	first, err := f.Archive("project")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := f.Archive("project")
	if !bytes.Equal(first, second) {
		t.Fatal("archive is not deterministic")
	}
	files := readArchive(t, first)
	if files["a_test.go"] != "package x" || files["sub/b.txt"] != "b" || len(files) != 3 { // plus the sub/ directory
		t.Fatalf("%v", files)
	}
	// A symlink could smuggle host files into a sandbox.
	if err = os.Symlink("/etc/passwd", filepath.Join(dir, "fx", "project", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Archive("project"); err == nil || !strings.Contains(err.Error(), "regular files") {
		t.Fatalf("symlink accepted: %v", err)
	}
	_ = os.Remove(filepath.Join(dir, "fx", "project", "link"))
	write("big.bin", strings.Repeat("x", maxFixtureBytes+1))
	if _, err = f.Archive("project"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized fixture accepted: %v", err)
	}
}

func TestLoadFixtures_RejectsMalformedFixtures(t *testing.T) {
	for name, tc := range map[string]struct {
		json string
		dirs []string
	}{
		"name mismatch":  {`{"name":"other","template":"base","visible_test":"a","hidden_test":"b"}`, []string{"project", "hidden", "solution"}},
		"missing hidden": {`{"name":"fx","template":"base","visible_test":"a","hidden_test":"b"}`, []string{"project", "solution"}},
		"unknown field":  {`{"name":"fx","template":"base","visible_test":"a","hidden_test":"b","extra":1}`, []string{"project", "hidden", "solution"}},
		"missing test":   {`{"name":"fx","template":"base","visible_test":"a"}`, []string{"project", "hidden", "solution"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "fx")
			for _, sub := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "fixture.json"), []byte(tc.json), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFixtures(root); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if _, err := LoadFixtures(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory accepted")
	}
}
