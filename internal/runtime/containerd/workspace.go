package containerd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// The volume workspace is a host directory bind-mounted at /workspace (ADR 0009). containerd has no named
// volumes, so the driver owns the directory's life: created before the container, removed after it.
//
// The directory belongs to the sandbox user (UID/GID 1000), because the sandbox cannot chown anything.
// Only a process that can make that so may create one: root (it chowns) or UID 1000 itself. Any other user
// would also be unable to delete what the sandbox writes, so it is refused up front rather than leaking.

// geteuid is replaced by tests to exercise the ownership rule without being that user.
var geteuid = os.Geteuid

// workspacePath is the directory for one sandbox; the id has been validated by the caller.
func (c Config) workspacePath(sandboxID string) string {
	return filepath.Join(c.WorkspaceDir, sandboxID)
}

// workspaceOwnerOK reports whether a process with this effective UID can own sandbox workspaces.
func workspaceOwnerOK(euid int) bool { return euid == 0 || euid == sandboxUID }

// prepareWorkspace creates the empty workspace directory for a sandbox. It fails if one exists, so a
// sandbox never inherits another's files.
func prepareWorkspace(root, dir string) error {
	if !workspaceOwnerOK(geteuid()) {
		return fmt.Errorf("volume workspaces need Arcana to run as root or UID %d, not UID %d: the sandbox user must own /workspace and Arcana must be able to delete its files (use workspace.backend: tmpfs otherwise)", sandboxUID, geteuid())
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("creating the workspace directory %s: %w", root, err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return runtime.Invalid{Field: "workspace already exists"}
		}
		return fmt.Errorf("creating the workspace: %w", err)
	}
	if err := ownWorkspace(dir); err != nil {
		return errors.Join(err, removeWorkspace(dir))
	}
	return nil
}

// ownWorkspace gives the directory to the sandbox user. The parent stays 0700 and Arcana's, so no other host
// user can reach it.
func ownWorkspace(dir string) error {
	if geteuid() == 0 {
		if err := os.Chown(dir, sandboxUID, sandboxGID); err != nil {
			return fmt.Errorf("giving the workspace to the sandbox user: %w", err)
		}
	}
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- the sandbox user owns it; the parent directory is 0700
		return fmt.Errorf("setting workspace permissions: %w", err)
	}
	return nil
}

// removeWorkspace deletes the directory and everything the sandbox wrote into it. A missing directory is
// success. It must only run once the container is gone, so nothing can race the removal.
func removeWorkspace(dir string) error {
	err := os.RemoveAll(dir)
	if err == nil {
		return nil
	}
	// A sandbox can chmod its own directories to 000, which stops a non-root owner descending into them.
	// Restore owner access to real directories (never following symlinks), then retry.
	relaxDirectories(dir)
	if again := os.RemoveAll(dir); again != nil {
		return fmt.Errorf("removing the workspace: %w", again)
	}
	return nil
}

func relaxDirectories(dir string) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return
	}
	_ = os.Chmod(dir, 0o700) // #nosec G302 -- directories need the execute bit; owner-only
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		relaxDirectories(filepath.Join(dir, entry.Name()))
	}
}

// listWorkspaces returns the sandbox ids that have a workspace directory.
func listWorkspaces(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && sandboxIDPattern.MatchString(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	return ids, nil
}

// checkWorkspaceDir proves the directory can be created, given to the sandbox user and removed, for
// `arcana doctor`.
func checkWorkspaceDir(root string) error {
	probe := filepath.Join(root, ".doctor-probe")
	if err := removeWorkspace(probe); err != nil {
		return err
	}
	if err := prepareWorkspace(root, probe); err != nil {
		return err
	}
	return removeWorkspace(probe)
}
