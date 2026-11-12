package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// Snapshot failures keep the API vocabulary; callers map them at the boundary.
var (
	ErrSnapshotNotFound = errors.New("snapshot not found")
	ErrSnapshotCorrupt  = errors.New("snapshot archive failed verification")
)

// forkParallelism bounds concurrent restores for one fork request.
const forkParallelism = 4

// snapshotWorkspace is the only directory a snapshot captures (PRD FR-SN-2).
const snapshotWorkspace = "/workspace"

// SnapshotSpec is the creation spec stored beside an archive. Environment values are never
// persisted, so only their keys are kept and restored sandboxes start without them.
type SnapshotSpec struct {
	CPUs         float64  `json:"cpus"`
	MemoryMB     int64    `json:"memory_mb"`
	DiskMB       int64    `json:"disk_mb"`
	PIDs         int64    `json:"pids"`
	EnvKeys      []string `json:"env_keys"`
	NetworkMode  string   `json:"network_mode"`
	NetworkAllow []string `json:"network_allow"`
}

// SnapshotSpecOf decodes the stored spec of a snapshot row.
func SnapshotSpecOf(row db.Snapshot) (SnapshotSpec, error) {
	var spec SnapshotSpec
	if err := json.Unmarshal([]byte(row.Spec), &spec); err != nil {
		return SnapshotSpec{}, fmt.Errorf("decoding snapshot spec: %w", err)
	}
	return spec, nil
}

// snapshotState tracks admitted-but-unfinished snapshots under one lock.
type snapshotState struct {
	mu           sync.Mutex
	pendingOwner map[string]int
	pendingBytes int64
}

func (s *Service) snapshotDir() string { return filepath.Join(s.cfg.DataDir, "snapshots") }

func (s *Service) snapshotLimits() snapshotLimits {
	return snapshotLimits{maxPerKey: s.cfg.Snapshots.MaxPerKey, maxTotalBytes: s.cfg.Snapshots.MaxTotalBytes, maxArchive: s.cfg.Limits.Files.MaxArchiveBytes}
}

// admitSnapshot reserves count and byte budget; the returned release must always be called.
func (s *Service) admitSnapshot(ctx context.Context, owner string) (limit int64, global bool, release func(), err error) {
	s.snaps.mu.Lock()
	defer s.snaps.mu.Unlock()
	count, err := s.store.Queries.CountSnapshots(ctx, owner)
	if err != nil {
		return 0, false, nil, fmt.Errorf("counting snapshots: %w", err)
	}
	stored, err := s.store.Queries.SumSnapshotBytes(ctx)
	if err != nil {
		return 0, false, nil, fmt.Errorf("summing snapshot bytes: %w", err)
	}
	limit, global, err = snapshotBudget(s.snapshotLimits(), snapshotUsage{ownerCount: int(count) + s.snaps.pendingOwner[owner], totalBytes: stored + s.snaps.pendingBytes})
	if err != nil {
		return 0, false, nil, err
	}
	s.snaps.pendingOwner[owner]++
	s.snaps.pendingBytes += limit
	return limit, global, func() {
		s.snaps.mu.Lock()
		defer s.snaps.mu.Unlock()
		s.snaps.pendingOwner[owner]--
		if s.snaps.pendingOwner[owner] <= 0 {
			delete(s.snaps.pendingOwner, owner)
		}
		s.snaps.pendingBytes -= limit
	}, nil
}

// archiveWriter hashes and counts every byte and refuses to exceed its reservation.
type archiveWriter struct {
	file   *os.File
	hash   io.Writer
	count  int64
	limit  int64
	exceed error
}

func (w *archiveWriter) Write(data []byte) (int, error) {
	if w.count+int64(len(data)) > w.limit {
		return 0, w.exceed
	}
	n, err := w.file.Write(data)
	w.count += int64(n)
	_, _ = w.hash.Write(data[:n])
	return n, err
}

// CreateSnapshot streams a gzip tar of /workspace out of a running sandbox through exec.
// The host stores the opaque bytes and never opens sandbox files (AGENTS rule 4).
func (s *Service) CreateSnapshot(ctx context.Context, p auth.Principal, sandboxID string) (db.Snapshot, error) {
	source, err := s.Get(ctx, p, sandboxID)
	if err != nil {
		return db.Snapshot{}, err
	}
	if source.State != string(Running) {
		return db.Snapshot{}, ErrNotRunning
	}
	id, err := ids.New("snp")
	if err != nil {
		return db.Snapshot{}, fmt.Errorf("generating snapshot ID: %w", err)
	}
	limit, global, release, err := s.admitSnapshot(ctx, p.ID)
	if err != nil {
		return db.Snapshot{}, err
	}
	defer release()
	exceed := ErrFileTooLarge
	if global {
		exceed = ErrQuota
	}
	dir := s.snapshotDir()
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return db.Snapshot{}, fmt.Errorf("creating snapshot directory: %w", err)
	}
	name := id + ".tar.gz"
	partial := filepath.Join(dir, name+".part")
	// The name is generated above, never caller-controlled.
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304
	if err != nil {
		return db.Snapshot{}, fmt.Errorf("creating snapshot file: %w", err)
	}
	hasher := sha256.New()
	writer := &archiveWriter{file: file, hash: hasher, limit: limit, exceed: exceed}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
			_ = os.Remove(partial)
		}
	}()
	args := []string{"/bin/tar", "-czf", "-", "-C", snapshotWorkspace, "."}
	if _, err = s.fileExec(ctx, p, sandboxID, args, nil, 0, writer, s.cfg.Limits.Files.MaxArchiveBytes); err != nil {
		return db.Snapshot{}, err
	}
	if err = file.Sync(); err != nil {
		return db.Snapshot{}, fmt.Errorf("syncing snapshot: %w", err)
	}
	if err = file.Close(); err != nil {
		return db.Snapshot{}, fmt.Errorf("closing snapshot: %w", err)
	}
	keep = true
	final := filepath.Join(dir, name)
	if err = os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return db.Snapshot{}, fmt.Errorf("publishing snapshot: %w", err)
	}
	spec, err := json.Marshal(SnapshotSpec{CPUs: source.Cpus.Float64, MemoryMB: source.MemoryMb.Int64, DiskMB: source.DiskMb.Int64, PIDs: source.Pids.Int64, EnvKeys: jsonStrings(source.EnvKeys.String), NetworkMode: source.NetworkMode, NetworkAllow: jsonStrings(source.NetworkAllow.String)})
	if err != nil {
		_ = os.Remove(final)
		return db.Snapshot{}, err
	}
	// The request context may be cancelled while the archive finished; persist regardless.
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	row, err := s.store.Queries.CreateSnapshot(persist, db.CreateSnapshotParams{ID: id, OwnerKeyID: p.ID, SourceSandboxID: sandboxID, Template: source.Template, Spec: string(spec), Path: name, SizeBytes: writer.count, Sha256: hex.EncodeToString(hasher.Sum(nil)), CreatedAt: s.clock.Now()})
	if err != nil {
		_ = os.Remove(final)
		return db.Snapshot{}, fmt.Errorf("recording snapshot: %w", err)
	}
	s.audit.Record("snapshot.created", p.ID, sandboxID, map[string]any{"snapshot_id": id, "template": source.Template, "size_bytes": row.SizeBytes})
	return row, nil
}

func jsonStrings(data string) []string {
	var out []string
	_ = json.Unmarshal([]byte(data), &out)
	if out == nil {
		out = []string{}
	}
	return out
}

// GetSnapshot hides other keys' snapshots behind not-found for non-admin callers.
func (s *Service) GetSnapshot(ctx context.Context, p auth.Principal, id string) (db.Snapshot, error) {
	row, err := s.store.Queries.GetSnapshot(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !p.Admin && row.OwnerKeyID != p.ID {
		return db.Snapshot{}, ErrSnapshotNotFound
	}
	if err != nil {
		return db.Snapshot{}, fmt.Errorf("getting snapshot: %w", err)
	}
	return row, nil
}

// ListSnapshots returns one bounded page of the caller's snapshots, ordered by ID.
func (s *Service) ListSnapshots(ctx context.Context, p auth.Principal, request ListRequest) ([]db.Snapshot, error) {
	if request.Limit < 1 || request.Limit > 1000 || len(request.Cursor) > 128 {
		return nil, ErrInvalid
	}
	owner := p.ID
	if p.Admin {
		owner = ""
	}
	rows, err := s.store.Queries.ListSnapshots(ctx, db.ListSnapshotsParams{ID: request.Cursor, Owner: owner, PageLimit: request.Limit})
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	return rows, nil
}

// DeleteSnapshot removes the archive before the record, so a failed removal can be retried.
func (s *Service) DeleteSnapshot(ctx context.Context, p auth.Principal, id string) error {
	row, err := s.GetSnapshot(ctx, p, id)
	if err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(s.snapshotDir(), row.ID+".tar.gz")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing snapshot archive: %w", err)
	}
	if _, err = s.store.Queries.DeleteSnapshot(ctx, row.ID); err != nil {
		return fmt.Errorf("deleting snapshot record: %w", err)
	}
	s.audit.Record("snapshot.deleted", p.ID, row.SourceSandboxID, map[string]any{"snapshot_id": row.ID, "size_bytes": row.SizeBytes})
	return nil
}

// fromSnapshot fills unset creation options from the snapshot spec. Explicit caller
// values always win; env values are not stored and therefore are not restored.
func fromSnapshot(request CreateRequest, snap db.Snapshot) (CreateRequest, error) {
	spec, err := SnapshotSpecOf(snap)
	if err != nil {
		return CreateRequest{}, ErrSnapshotCorrupt
	}
	if request.Template != snap.Template {
		return CreateRequest{}, fmt.Errorf("%w: template must match the snapshot template", ErrInvalid)
	}
	request.FromSnapshot = snap.ID
	if request.Resources.CPUs == nil && spec.CPUs > 0 {
		request.Resources.CPUs = &spec.CPUs
	}
	if request.Resources.MemoryMB == nil && spec.MemoryMB > 0 {
		request.Resources.MemoryMB = &spec.MemoryMB
	}
	if request.Resources.DiskMB == nil && spec.DiskMB > 0 {
		request.Resources.DiskMB = &spec.DiskMB
	}
	if request.Resources.PIDs == nil && spec.PIDs > 0 {
		request.Resources.PIDs = &spec.PIDs
	}
	if request.NetworkMode == "" && len(request.NetworkAllow) == 0 && len(request.NetworkPresets) == 0 {
		request.NetworkMode = spec.NetworkMode
		request.NetworkAllow = slices.Clone(spec.NetworkAllow)
	}
	return request, nil
}

// restore streams the verified archive into the new sandbox's workspace and destroys the
// sandbox when that fails, so a half-restored workspace is never handed to a caller.
func (s *Service) restore(ctx context.Context, p auth.Principal, row db.Sandbox, snap db.Snapshot) (db.Sandbox, error) {
	err := s.extractSnapshot(ctx, p, row.ID, snap)
	if err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		destroyErr := s.Destroy(cleanup, p, row.ID, "restore_failed")
		return db.Sandbox{}, errors.Join(err, destroyErr)
	}
	s.audit.Record("sandbox.created", p.ID, row.ID, map[string]any{"template": row.Template, "runtime": row.Runtime, "source": row.Source, "from_snapshot": snap.ID, "duration_seconds": max(0, s.clock.Now().Sub(row.CreatedAt).Seconds())})
	return s.Get(ctx, p, row.ID)
}

func (s *Service) extractSnapshot(ctx context.Context, p auth.Principal, sandboxID string, snap db.Snapshot) error {
	if snap.Path != snap.ID+".tar.gz" {
		return ErrSnapshotCorrupt
	}
	// The path is derived from a validated ID, never from caller input.
	file, err := os.Open(filepath.Join(s.snapshotDir(), snap.Path)) // #nosec G304
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: archive missing", ErrSnapshotCorrupt)
	}
	if err != nil {
		return fmt.Errorf("opening snapshot: %w", err)
	}
	// Verify the whole archive on the descriptor that will be streamed, so a swapped
	// path cannot change what is extracted. The host only hashes; it never unpacks.
	hasher := sha256.New()
	size, err := io.Copy(hasher, io.LimitReader(file, s.cfg.Limits.Files.MaxArchiveBytes+1))
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("reading snapshot: %w", err)
	}
	if size != snap.SizeBytes || hex.EncodeToString(hasher.Sum(nil)) != snap.Sha256 {
		_ = file.Close()
		return ErrSnapshotCorrupt
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return fmt.Errorf("rewinding snapshot: %w", err)
	}
	args := []string{"/bin/tar", "-xzf", "-", "--no-same-owner", "--no-same-permissions", "-C", snapshotWorkspace}
	_, err = s.fileExec(ctx, p, sandboxID, args, archiveInput{file}, s.cfg.Limits.Files.MaxArchiveBytes, io.Discard, 4096)
	return err
}

// archiveInput lets the exec transport close the archive when the command exits while the
// copy loop is still finishing its last read. A read after close then ends the stream; a
// command that really needed more bytes fails on its own exit status, so nothing is hidden.
type archiveInput struct{ *os.File }

func (a archiveInput) Read(data []byte) (int, error) {
	n, err := a.File.Read(data)
	if errors.Is(err, os.ErrClosed) {
		return n, io.EOF
	}
	return n, err
}

// ForkFailure reports one sandbox of a fork that could not be created.
type ForkFailure struct {
	Index int
	Err   error
}

// ForkResult lists created sandboxes in request order plus per-index failures.
type ForkResult struct {
	Items    []db.Sandbox
	Failures []ForkFailure
}

// Fork creates count sandboxes from one snapshot. Members run in parallel up to a small
// bound; each is an independent creation, so quota failures are reported per member.
// If the caller disconnects, sandboxes already created are destroyed rather than orphaned.
func (s *Service) Fork(ctx context.Context, p auth.Principal, snapshotID string, count int, base CreateRequest) (ForkResult, error) {
	if count < 1 || count > s.cfg.Snapshots.MaxFork {
		return ForkResult{}, fmt.Errorf("%w: count must be 1..%d", ErrInvalid, s.cfg.Snapshots.MaxFork)
	}
	snap, err := s.GetSnapshot(ctx, p, snapshotID)
	if err != nil {
		return ForkResult{}, err
	}
	base.Template = snap.Template
	rows := make([]db.Sandbox, count)
	failures := make([]error, count)
	var group errgroup.Group
	group.SetLimit(min(count, forkParallelism))
	for i := range count {
		group.Go(func() error {
			rows[i], failures[i] = s.Create(ctx, p, withSnapshot(base, snap.ID))
			return nil
		})
	}
	_ = group.Wait()
	var result ForkResult
	for i := range count {
		if failures[i] != nil {
			result.Failures = append(result.Failures, ForkFailure{Index: i, Err: failures[i]})
		} else {
			result.Items = append(result.Items, rows[i])
		}
	}
	if ctx.Err() != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var destroyErr error
		for _, row := range result.Items {
			destroyErr = errors.Join(destroyErr, s.Destroy(cleanup, p, row.ID, "requested"))
		}
		return ForkResult{}, errors.Join(ctx.Err(), destroyErr)
	}
	return result, nil
}

// withSnapshot copies the shared request so members never alias mutable maps.
func withSnapshot(base CreateRequest, id string) CreateRequest {
	base.FromSnapshot = id
	base.Env = maps.Clone(base.Env)
	base.Labels = maps.Clone(base.Labels)
	base.NetworkAllow = slices.Clone(base.NetworkAllow)
	base.NetworkPresets = slices.Clone(base.NetworkPresets)
	return base
}

// pruneSnapshots removes interrupted writes and archives without a record. Run it before
// the listener opens; foreign file names are left alone.
func (s *Service) pruneSnapshots(ctx context.Context) error {
	dir, err := os.Open(s.snapshotDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opening snapshot directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	var result error
	for {
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			id, partial, ours := snapshotMember(entry.Name())
			if !ours || !entry.Type().IsRegular() {
				continue
			}
			orphan := partial
			if !partial {
				_, getErr := s.store.Queries.GetSnapshot(ctx, id)
				orphan = errors.Is(getErr, sql.ErrNoRows)
				if getErr != nil && !orphan {
					result = errors.Join(result, getErr)
					continue
				}
			}
			if orphan {
				if err = os.Remove(filepath.Join(s.snapshotDir(), entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, err)
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				result = errors.Join(result, readErr)
			}
			return result
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
