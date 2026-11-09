package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/pathutil"
)

var errFileLimit = errors.New("file operation limit exceeded")

type rootFlags []string

func (r *rootFlags) String() string { return "allowed sandbox roots" }
func (r *rootFlags) Set(value string) error {
	if len(*r) >= pathutil.MaxRoots || len(value) > 4096 {
		return errors.New("too many or oversized roots")
	}
	*r = append(*r, value)
	return nil
}

func fileCommand(args []string, stdin io.Reader, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	filename := flags.String("path", "", "sandbox path")
	limit := flags.Int64("max-bytes", 52428800, "transfer limit")
	depth := flags.Int("depth", 1, "directory depth")
	recursive := flags.Bool("recursive", false, "remove directories recursively")
	metadata := flags.Bool("metadata", false, "return removed entry metadata")
	directory := flags.Bool("directory", false, "require a directory")
	var roots rootFlags
	flags.Var(&roots, "root", "allowed root")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *limit < 1 || *limit == math.MaxInt64 || *depth < 1 || *depth > 100 {
		return protocol.FileInvalid, nil
	}
	if len(roots) == 0 {
		roots = rootFlags{"/workspace", "/tmp"}
	}
	clean, err := pathutil.Clean(*filename, roots)
	if err != nil {
		return protocol.FileInvalid, nil
	}
	switch args[0] {
	case "write":
		err = writeSandboxFile(clean, *limit, stdin)
	case "read":
		err = readSandboxFile(clean, *limit, stdout)
	case "ls":
		err = listSandboxFiles(clean, roots, *depth, stdout)
	case "remove":
		var size int64
		if *metadata {
			info, statErr := os.Lstat(clean)
			if statErr == nil {
				size = info.Size()
			}
		} // #nosec G703 -- sandbox-only validated path; do not follow links.
		if *recursive {
			err = os.RemoveAll(clean) // #nosec G703 -- shared lexical validator checked allowed roots; runs only in sandbox namespace
		} else {
			err = os.Remove(clean) // #nosec G703 -- shared lexical validator checked allowed roots; runs only in sandbox namespace
		}
		if err == nil && *metadata {
			err = json.NewEncoder(stdout).Encode(protocol.FileHeader{SizeBytes: size})
		}
	case "mkdir":
		err = os.MkdirAll(clean, 0o755) // #nosec G301 G703 -- validated sandbox path; directories must be accessible to UID 1000 tools
	case "stat":
		var info os.FileInfo
		info, err = os.Stat(clean) // #nosec G703 -- shared validator checked this sandbox-only path
		if err == nil && *directory && !info.IsDir() {
			err = errors.New("not a directory")
		}
	}
	switch {
	case err == nil:
		return 0, nil
	case errors.Is(err, os.ErrNotExist):
		return protocol.FileNotFound, nil
	case errors.Is(err, os.ErrPermission):
		return protocol.FileDenied, nil
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return protocol.FileNoSpace, nil
	case errors.Is(err, errFileLimit):
		return protocol.FileTooLarge, nil
	default:
		return protocol.FileInvalid, nil
	}
}

func writeSandboxFile(filename string, limit int64, stdin io.Reader) error {
	parent := filepath.Dir(filename)
	if err := os.MkdirAll(parent, 0o755); err != nil { // #nosec G301 G703 -- parent of a validated sandbox path
		return err
	}
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	temporary := filepath.Join(parent, ".arcana-write-"+hex.EncodeToString(entropy[:]))
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 G703 -- random exclusive temporary file under validated sandbox parent
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(temporary) // #nosec G703 -- only our sandbox-local temporary path
	}()
	n, err := io.Copy(file, io.LimitReader(stdin, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errFileLimit
	}
	if err = file.Chmod(0o644); err != nil {
		return err
	} // #nosec G302 -- uploaded files are ordinary sandbox data, readable by sandbox tools
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, filename) // #nosec G703 -- both paths are validated sandbox-local paths
}

func readSandboxFile(filename string, limit int64, stdout io.Writer) error {
	// Nonblocking open avoids waiting on a FIFO before its type can be checked.
	file, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NONBLOCK, 0) // #nosec G304 G703 -- validated sandbox path; symlinks resolve only in that namespace
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return errors.New("not a regular file")
	}
	if info.Size() > limit {
		return errFileLimit
	}
	if err = json.NewEncoder(stdout).Encode(protocol.FileHeader{SizeBytes: info.Size()}); err != nil {
		return err
	}
	// Read the opened descriptor's original size, ignoring later appends. Truncation
	// fails the transfer rather than silently claiming a complete response.
	_, err = io.CopyN(stdout, file, info.Size())
	return err
}

func listSandboxFiles(filename string, roots []string, depth int, stdout io.Writer) error {
	type directory struct {
		path  string
		level int
	}
	pending := []directory{{filename, 0}}
	entries := make([]protocol.FileEntry, 0)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		file, err := os.Open(current.path) // #nosec G304 G703 -- validated sandbox path; entries are validated before queueing
		if err != nil {
			return err
		}
		for {
			batch, readErr := file.ReadDir(128)
			if readErr != nil && readErr != io.EOF {
				_ = file.Close()
				return readErr
			}
			for _, entry := range batch {
				if len(entries) >= protocol.MaxListEntries {
					_ = file.Close()
					return errFileLimit
				}
				full, err := pathutil.Clean(filepath.Join(current.path, entry.Name()), roots)
				if err != nil {
					_ = file.Close()
					return err
				}
				info, err := entry.Info()
				if err != nil {
					_ = file.Close()
					return err
				}
				kind := "other"
				switch {
				case info.Mode()&os.ModeSymlink != 0:
					kind = "symlink"
				case info.IsDir():
					kind = "directory"
				case info.Mode().IsRegular():
					kind = "file"
				}
				entries = append(entries, protocol.FileEntry{Name: entry.Name(), Path: full, Type: kind, SizeBytes: info.Size(), Mode: int64(info.Mode().Perm()), ModifiedAt: info.ModTime().UTC()})
				if kind == "directory" && current.level+1 < depth {
					pending = append(pending, directory{full, current.level + 1})
				}
			}
			if readErr == io.EOF {
				break
			}
		}
		if err = file.Close(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(struct {
		Items []protocol.FileEntry `json:"items"`
	}{entries})
	if err != nil {
		return err
	}
	if len(data) > protocol.MaxListBytes {
		return errFileLimit
	}
	_, err = stdout.Write(data)
	return err
}
