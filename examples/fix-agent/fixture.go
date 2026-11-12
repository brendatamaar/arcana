package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Bounds keep a malformed or hostile fixture directory from exhausting memory.
const (
	maxFixtureFiles = 200
	maxFixtureBytes = 2 << 20
	// stripSuffix marks files that must not be compiled as part of this repository's own Go module
	// (hidden tests and reference solutions); it is removed on upload.
	stripSuffix = ".fixture"
)

// Fixture is a small buggy project with a visible failing test, hidden tests used only to judge a
// fix, and a reference solution used only to validate the fixture itself.
type Fixture struct {
	Name        string `json:"name"`
	Language    string `json:"language"`
	Template    string `json:"template"`
	Description string `json:"description"`
	// VisibleTest is the command the agent is told about; HiddenTest judges success. An empty
	// HiddenTest (an ad-hoc project given with --dir) judges success by VisibleTest.
	VisibleTest string `json:"visible_test"`
	HiddenTest  string `json:"hidden_test"`
	Dir         string `json:"-"`
	// projectDir overrides Dir/project for ad-hoc projects given with --dir.
	projectDir string
}

// LoadFixtures reads every subdirectory of dir that contains a fixture.json, sorted by name.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading fixtures: %w", err)
	}
	var out []Fixture
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		f, err := loadFixture(filepath.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Fixture) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func loadFixture(dir string) (Fixture, error) {
	// os.Root confines every access to the fixture directory, so a symlink cannot reach elsewhere.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Fixture{}, err
	}
	defer func() { _ = root.Close() }()
	file, err := root.Open("fixture.json")
	if err != nil {
		return Fixture{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 64<<10))
	if err != nil {
		return Fixture{}, err
	}
	var f Fixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&f); err != nil {
		return Fixture{}, fmt.Errorf("%s: invalid fixture.json: %w", dir, err)
	}
	f.Dir = dir
	if f.Name != filepath.Base(dir) || f.Template == "" || f.VisibleTest == "" || f.HiddenTest == "" {
		return Fixture{}, fmt.Errorf("%s: fixture.json needs name (matching the directory), template, visible_test and hidden_test", dir)
	}
	for _, sub := range []string{"project", "hidden", "solution"} {
		if info, statErr := root.Stat(sub); statErr != nil || !info.IsDir() {
			return Fixture{}, fmt.Errorf("%s: missing %s directory", dir, sub)
		}
	}
	return f, nil
}

// Archive packs one of the fixture's directories (project, hidden or solution) as a .tar.gz with
// the .fixture suffix removed. Symlinks and special files are rejected.
func (f Fixture) Archive(sub string) ([]byte, error) {
	if sub == "project" && f.projectDir != "" {
		return archiveDir(f.projectDir)
	}
	return archiveDir(filepath.Join(f.Dir, sub))
}

func archiveDir(dir string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	fsys := root.FS()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	files, total := 0, int64(0)
	err = fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || path == "." {
			return walkErr
		}
		name := strings.TrimSuffix(path, stripSuffix)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		case !info.Mode().IsRegular():
			return fmt.Errorf("%s: only regular files and directories are allowed", path)
		}
		files++
		total += info.Size()
		if files > maxFixtureFiles || total > maxFixtureBytes {
			return errors.New("fixture exceeds file count or size limit")
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = tw.Close(); err != nil {
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// hiddenPaths lists the sandbox paths hidden files occupy, so they can be removed after a check.
func (f Fixture) hiddenPaths() ([]string, error) {
	root, err := os.OpenRoot(filepath.Join(f.Dir, "hidden"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	var out []string
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		out = append(out, "/workspace/"+strings.TrimSuffix(path, stripSuffix))
		return nil
	})
	return out, err
}
