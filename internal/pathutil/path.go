// Package pathutil validates POSIX sandbox paths without touching a filesystem.
package pathutil

import (
	"errors"
	"path"
	"strings"
)

// MaxRoots bounds configuration and the helper's argv representation.
const MaxRoots = 16

// Clean checks lexical containment only. Symlinks resolve inside the sandbox,
// never through host volume paths. URL query decoding belongs to the HTTP router.
func Clean(value string, roots []string) (string, error) {
	if len(value) == 0 || len(value) > 4096 || !path.IsAbs(value) || strings.ContainsRune(value, 0) || len(roots) == 0 || len(roots) > MaxRoots {
		return "", errors.New("invalid sandbox path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("parent traversal is not allowed")
		}
	}
	clean := path.Clean(value)
	for _, root := range roots {
		if !path.IsAbs(root) || path.Clean(root) != root || strings.ContainsRune(root, 0) || len(root) > 4096 {
			return "", errors.New("invalid allowed root")
		}
	}
	for _, root := range roots {
		if clean == root || strings.HasPrefix(clean, strings.TrimSuffix(root, "/")+"/") {
			return clean, nil
		}
	}
	return "", errors.New("path outside allowed roots")
}
