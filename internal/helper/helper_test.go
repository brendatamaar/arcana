package helper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArtifact_SupportedArchitecturesMatchManifest(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		data, digest, err := Artifact(arch)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) < 4 || string(data[:4]) != "\x7fELF" {
			t.Fatal("helper is not ELF")
		}
		object, parseErr := elf.NewFile(bytes.NewReader(data))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if arch == "amd64" && object.Machine != elf.EM_X86_64 || arch == "arm64" && object.Machine != elf.EM_AARCH64 {
			t.Fatal("incorrect helper architecture")
		}
		for _, program := range object.Progs {
			if program.Type == elf.PT_INTERP {
				t.Fatal("helper is dynamically linked")
			}
		}
		actual := sha256.Sum256(data)
		if hex.EncodeToString(actual[:]) != digest {
			t.Fatal("checksum mismatch")
		}
	}
	if _, _, err := Artifact("unsupported"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}

func TestInstall_VerifiesAndAtomicallyReplacesHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic bind-inode preservation and mode 0755 are Linux installer behavior")
	}
	dir := t.TempDir()
	data, digest, err := Artifact("amd64")
	if err != nil {
		t.Fatal(err)
	}
	filename, err := install(context.Background(), dir, data, digest)
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.Open(filename) // #nosec G304 -- verified helper installed in isolated t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	info, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("helper mode: %o", info.Mode().Perm())
	}
	if _, err = install(context.Background(), dir, data, digest); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, again) {
		t.Fatal("valid helper was unnecessarily replaced")
	}
	other := []byte("replacement fixture")
	otherHash := sha256.Sum256(other)
	if _, err = install(context.Background(), dir, other, hex.EncodeToString(otherHash[:])); err != nil {
		t.Fatal(err)
	}
	// An already-bound old inode retains the original bytes during an upgrade.
	old, err := io.ReadAll(io.LimitReader(first, int64(len(data))+1))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(old) != sha256.Sum256(data) {
		t.Fatal("live helper inode was overwritten")
	}
	if _, err = install(context.Background(), dir, data, "invalid"); err == nil {
		t.Fatal("bad checksum accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = install(ctx, dir, data, digest); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestInstall_RejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation needs additional privileges")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "bin", "sbx-exec")); err != nil {
		t.Fatal(err)
	}
	data, digest, err := Artifact("amd64")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = install(context.Background(), dir, data, digest); err == nil {
		t.Fatal("escaping symlink accepted")
	}
}
