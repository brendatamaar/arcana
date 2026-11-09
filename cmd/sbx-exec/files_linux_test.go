package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
)

func TestFiles_BoundedAtomicWriteBinaryReadAndSandboxSymlinks(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "nested", "file")
	args := func(op string) []string { return []string{op, "--root", root, "--path", filename, "--max-bytes", "4"} }
	content := []byte{'a', 0, 0xff, 'b'}
	var output bytes.Buffer
	code, err := fileCommand(args("write"), bytes.NewReader(content), &output)
	if err != nil || code != 0 {
		t.Fatal("write", code, err)
	}
	code, err = fileCommand(args("write"), bytes.NewReader([]byte("large")), &output)
	if err != nil || code != protocol.FileTooLarge {
		t.Fatal("overflow accepted", code, err)
	}
	actual, err := os.ReadFile(filename) // #nosec G304 -- owned unit-test fixture, not a Docker workspace
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("failed write replaced the old file", err)
	}
	code, err = fileCommand(args("read"), bytes.NewReader(nil), &output)
	if err != nil || code != 0 {
		t.Fatal("read", code, err)
	}
	reader := bufio.NewReader(&output)
	header, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var info protocol.FileHeader
	if err = json.Unmarshal(header, &info); err != nil || info.SizeBytes != 4 {
		t.Fatal("missing opened-file stat", err)
	}
	actual, err = io.ReadAll(reader)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatal("binary read changed", err)
	}
	outside := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(outside, []byte("symlink target"), 0o600); err != nil {
		t.Fatal(err)
	} // #nosec G304 -- owned fixture simulates a path elsewhere in the sandbox
	link := filepath.Join(root, "link")
	if err = os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	code, err = fileCommand([]string{"read", "--root", root, "--path", link}, bytes.NewReader(nil), &output)
	if err != nil || code != 0 || !bytes.Contains(output.Bytes(), []byte("symlink target")) {
		t.Fatal("sandbox symlink was not followed", code, err)
	}
	code, err = fileCommand([]string{"remove", "--root", root, "--path", link}, bytes.NewReader(nil), io.Discard)
	if err != nil || code != 0 {
		t.Fatal(err)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatal("removing a symlink removed its target")
	}
}

func TestFiles_ListDepthEntryCapAndInvalidInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir", "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for _, depth := range []int{1, 2} {
		output.Reset()
		code, err := fileCommand([]string{"ls", "--root", root, "--path", root, "--depth", strconv.Itoa(depth)}, bytes.NewReader(nil), &output)
		if err != nil || code != 0 {
			t.Fatal(code, err)
		}
		var list struct {
			Items []protocol.FileEntry `json:"items"`
		}
		if err = json.Unmarshal(output.Bytes(), &list); err != nil || len(list.Items) != depth {
			t.Fatal("depth ignored", err)
		}
	}
	for _, args := range [][]string{{"read", "--root", root, "--path", "/etc/passwd"}, {"write", "--root", root, "--path", filepath.Join(root, "x"), "--max-bytes", "0"}, {"ls", "--root", root, "--path", root, "--depth", "101"}} {
		if code, err := fileCommand(args, bytes.NewReader(nil), io.Discard); err != nil || code != protocol.FileInvalid {
			t.Fatal("invalid arguments accepted", code, err)
		}
	}
	for i := range protocol.MaxListEntries {
		if err := os.WriteFile(filepath.Join(root, "entry-"+strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	output.Reset()
	code, err := fileCommand([]string{"ls", "--root", root, "--path", root}, bytes.NewReader(nil), &output)
	if err != nil || code != protocol.FileTooLarge || output.Len() != 0 {
		t.Fatal("unbounded or partial listing", code, err)
	}
}

func TestFiles_RemoveMetadataContainsSizeOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "probe")
	if err := os.WriteFile(path, []byte("private file contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code, err := fileCommand([]string{"remove", "--root", root, "--path", path, "--metadata"}, bytes.NewReader(nil), &output)
	var header protocol.FileHeader
	if err != nil || code != 0 || json.Unmarshal(output.Bytes(), &header) != nil || header.SizeBytes != 21 {
		t.Fatal(code, err, output.String())
	}
	if bytes.Contains(output.Bytes(), []byte("private")) {
		t.Fatal("contents in metadata")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file was not removed", err)
	}
}
