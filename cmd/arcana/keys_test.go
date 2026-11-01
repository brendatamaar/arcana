package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/auth"
)

func TestKeysCLI_CreateListRevoke(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(filename, []byte(fmt.Sprintf("data_dir: %q\n", dir)), 0o600); err != nil {
		t.Fatal(err)
	}
	execute := func(args ...string) (string, error) {
		root := newRootCommand()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"--config", filename, "keys"}, args...))
		err := root.Execute()
		return out.String(), err
	}
	raw, err := execute("create", "operator", "--admin")
	if err != nil {
		t.Fatal(err)
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "arc_live_") || len(raw) != 41 {
		t.Fatal("key was not printed")
	}
	output, err := execute("list")
	if err != nil {
		t.Fatal(err)
	}
	var keys []auth.Key
	if err = json.Unmarshal([]byte(output), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !keys[0].Admin || strings.Contains(output, raw) || strings.Contains(output, "hash") {
		t.Fatalf("unexpected list: %s", output)
	}
	if _, err = execute("revoke", keys[0].ID); err != nil {
		t.Fatal(err)
	}
	output, err = execute("list")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(output), &keys); err != nil {
		t.Fatal(err)
	}
	if keys[0].RevokedAt == nil {
		t.Fatal("revocation not persisted")
	}
	if _, err = execute("list", "--limit", "-1"); err == nil {
		t.Fatal("accepted invalid limit")
	}
	if _, err = execute("create"); err == nil {
		t.Fatal("accepted missing name")
	}
}
