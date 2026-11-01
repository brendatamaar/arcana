// Command buildhelper reproducibly embeds static Linux helpers for both release architectures.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func main() {
	root := flag.String("root", ".", "repository root")
	copyBin := flag.Bool("bin", false, "also write bin/sbx-exec")
	flag.Parse()
	if err := build(*root, *copyBin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func build(root string, copyBin bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	output := filepath.Join(root, "internal", "helper", "assets")
	if err = os.MkdirAll(output, 0o750); err != nil {
		return err
	}
	manifest := struct {
		GoVersion string            `json:"go_version"`
		SHA256    map[string]string `json:"sha256"`
	}{runtime.Version(), make(map[string]string)}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "linux_" + arch + ".bin"
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		// #nosec G204 -- fixed compiler and argv; output paths are operator-selected repository artifacts
		command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", filepath.Join(output, name), "./cmd/sbx-exec")
		command.Dir = root
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch, "GOAMD64=v1", "GOARM64=v8.0", "GOEXPERIMENT=", "GOWORK=off", "GOFLAGS=")
		err = command.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("building %s helper: %w", arch, err)
		}
		file, err := os.Open(filepath.Join(output, name)) // #nosec G304 -- fixed architecture artifacts under the operator-selected repository root
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, readErr := io.Copy(hash, io.LimitReader(file, 10485761))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n > 10485760 {
			return fmt.Errorf("helper artifact exceeds 10 MiB")
		}
		if err = os.Chmod(filepath.Join(output, name), 0o644); err != nil {
			return err
		} // #nosec G302 -- public non-executable embedded asset
		manifest.SHA256[name] = hex.EncodeToString(hash.Sum(nil))
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// These are public executable assets, not credentials.
	if err = os.WriteFile(filepath.Join(output, "manifest.json"), append(data, '\n'), 0o644); err != nil {
		return err
	} // #nosec G306 -- public generated checksum manifest
	if copyBin {
		return copyBinary(filepath.Join(output, "linux_"+runtime.GOARCH+".bin"), filepath.Join(root, "bin"))
	}
	return nil
}

func copyBinary(source, directory string) error {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	input, err := os.Open(source) // #nosec G304 -- fixed generated architecture artifact
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	file, err := os.CreateTemp(directory, ".sbx-exec-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	n, err := io.Copy(file, io.LimitReader(input, 10485761))
	if err != nil {
		return err
	}
	if n > 10485760 {
		return fmt.Errorf("helper exceeds artifact bound")
	}
	if err = file.Chmod(0o755); err != nil {
		return err
	} // #nosec G302 -- required static helper executable
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "sbx-exec"))
}
