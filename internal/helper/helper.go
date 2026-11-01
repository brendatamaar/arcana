// Package helper embeds and atomically installs the standalone Linux exec helper.
package helper

//go:generate go run ../../scripts/buildhelper --root ../..

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed assets/*
var embedded embed.FS

// Artifact returns an independently verified static helper for a supported architecture.
func Artifact(arch string) ([]byte, string, error) {
	if arch != "amd64" && arch != "arm64" {
		return nil, "", fmt.Errorf("unsupported helper architecture %q", arch)
	}
	data, err := embedded.ReadFile("assets/linux_" + arch + ".bin")
	if err != nil {
		return nil, "", fmt.Errorf("reading embedded helper: %w", err)
	}
	manifestData, err := embedded.ReadFile("assets/manifest.json")
	if err != nil {
		return nil, "", err
	}
	var manifest struct {
		GoVersion string            `json:"go_version"`
		SHA256    map[string]string `json:"sha256"`
	}
	if len(manifestData) > 4096 {
		return nil, "", errors.New("helper manifest exceeds limit")
	}
	if err = json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, "", fmt.Errorf("decoding helper manifest: %w", err)
	}
	expected := manifest.SHA256["linux_"+arch+".bin"]
	digest := sha256.Sum256(data)
	if len(data) == 0 || len(data) > 10485760 || len(expected) != 64 || hex.EncodeToString(digest[:]) != expected {
		return nil, "", errors.New("embedded helper checksum mismatch")
	}
	return data, expected, nil
}

// Install writes data_dir/bin/sbx-exec as mode 0755, preserving live bind-mount inodes.
func Install(ctx context.Context, dataDir string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("helper installation requires Linux")
	}
	data, digest, err := Artifact(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	return install(ctx, dataDir, data, digest)
}

func install(ctx context.Context, dataDir string, data []byte, digest string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	actual := sha256.Sum256(data)
	if len(data) == 0 || len(data) > 10485760 || hex.EncodeToString(actual[:]) != digest {
		return "", errors.New("helper checksum mismatch")
	}
	directory := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("creating helper directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	filename := filepath.Join(directory, "sbx-exec")
	existing, err := root.Open("sbx-exec")
	if err == nil {
		old, readErr := io.ReadAll(io.LimitReader(existing, int64(len(data))+1))
		info, statErr := existing.Stat()
		if readErr == nil && statErr == nil && info.Mode().IsRegular() && len(old) == len(data) && sha256.Sum256(old) == actual {
			modeErr := existing.Chmod(0o755) // #nosec G302 -- required non-writable executable mode for sandbox UID 1000
			closeErr := existing.Close()
			if modeErr != nil {
				return "", modeErr
			}
			if closeErr != nil {
				return "", closeErr
			}
			return filename, nil
		}
		if err = existing.Close(); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking installed helper: %w", err)
	}
	var entropy [8]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("naming helper temporary file: %w", err)
	}
	temporary := ".sbx-exec-" + hex.EncodeToString(entropy[:])
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close(); _ = root.Remove(temporary) }()
	for offset := 0; offset < len(data); {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		end := offset + 32768
		if end > len(data) {
			end = len(data)
		}
		n, writeErr := file.Write(data[offset:end])
		if writeErr != nil {
			return "", writeErr
		}
		if n != end-offset {
			return "", io.ErrShortWrite
		}
		offset = end
	}
	if err = file.Chmod(0o755); err != nil {
		return "", err
	} // #nosec G302 -- static helper must execute as sandbox UID 1000
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = root.Rename(temporary, "sbx-exec"); err != nil {
		return "", fmt.Errorf("installing helper atomically: %w", err)
	}
	return filename, nil
}
