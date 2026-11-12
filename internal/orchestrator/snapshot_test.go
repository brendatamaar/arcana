package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store/db"
)

func TestSnapshotBudget_CountAndBytesAreEnforcedBeforeStreaming(t *testing.T) {
	limits := snapshotLimits{maxPerKey: 2, maxTotalBytes: 100, maxArchive: 40}
	for _, tc := range []struct {
		name   string
		usage  snapshotUsage
		limit  int64
		global bool
		err    error
	}{
		{"empty store uses the per-archive cap", snapshotUsage{}, 40, false, nil},
		{"remaining budget below the archive cap", snapshotUsage{ownerCount: 1, totalBytes: 80}, 20, true, nil},
		{"owner at count limit", snapshotUsage{ownerCount: 2}, 0, false, ErrQuota},
		{"store full", snapshotUsage{totalBytes: 100}, 0, true, ErrQuota},
		{"store overfull", snapshotUsage{totalBytes: 500}, 0, true, ErrQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit, global, err := snapshotBudget(limits, tc.usage)
			if !errors.Is(err, tc.err) || limit != tc.limit || global != tc.global {
				t.Fatalf("got %d %t %v", limit, global, err)
			}
		})
	}
}

func TestSnapshotMember_OnlyArcanaNamesAreOurs(t *testing.T) {
	id := "snp_abcdefghijklmnopqrst"
	for _, tc := range []struct {
		name            string
		partial, ours   bool
		wantID          string
		wantPartialOnly bool
	}{
		{id + ".tar.gz", false, true, id, false},
		{id + ".tar.gz.part", true, true, id, true},
		{"notes.txt", false, false, "", false},
		{"snp_short.tar.gz", false, false, "", false},
		{"../" + id + ".tar.gz", false, false, "", false},
		{id + ".tar.gz.bak", false, false, "", false},
		{"snp_ABCDEFGHIJKLMNOPQRST.tar.gz", false, false, "", false},
	} {
		got, partial, ours := snapshotMember(tc.name)
		if ours != tc.ours || partial != tc.partial || got != tc.wantID {
			t.Fatalf("%q: %q %t %t", tc.name, got, partial, ours)
		}
	}
}

func snapshotFixture(t *testing.T) (*Service, *helperFake, auth.Principal, auth.Principal, db.Sandbox, *manualClock) {
	t.Helper()
	s, d, p, other, row, clock := execFixture(t)
	s.cfg.DataDir = t.TempDir()
	return s, d, p, other, row, clock
}

func drainStarted(d *helperFake) (last runtime.ExecRequest) {
	for {
		select {
		case last = <-d.started:
		default:
			return last
		}
	}
}

func TestSnapshot_StoresOpaqueArchiveAndSpecWithoutEnvValues(t *testing.T) {
	s, d, p, _, row, _ := snapshotFixture(t)
	archive := []byte("\x1f\x8bopaque-archive\x00\xff")
	d.script = fake.Script{Stdout: archive}
	snap, err := s.CreateSnapshot(context.Background(), p, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := drainStarted(d)
	if got := strings.Join(run.Cmd[len(run.Cmd)-6:], " "); got != "/bin/tar -czf - -C /workspace ." {
		t.Fatalf("snapshot must archive only /workspace through argv: %q", got)
	}
	sum := sha256.Sum256(archive)
	if snap.OwnerKeyID != p.ID || snap.SourceSandboxID != row.ID || snap.Template != "base" || snap.SizeBytes != int64(len(archive)) || snap.Sha256 != hex.EncodeToString(sum[:]) || !strings.HasPrefix(snap.ID, "snp_") || snap.Path != snap.ID+".tar.gz" {
		t.Fatalf("unexpected record: %+v", snap)
	}
	file := filepath.Join(s.snapshotDir(), snap.Path)
	stored, err := os.ReadFile(file) // #nosec G304 -- test-owned temporary path
	if err != nil || !bytes.Equal(stored, archive) {
		t.Fatal("archive bytes changed", err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("archive must be private", info.Mode(), err)
	}
	if info, err := os.Stat(s.snapshotDir()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("snapshot directory must be private", err)
	}
	if strings.Contains(snap.Spec, "template-secret") || !strings.Contains(snap.Spec, "DEFAULT") {
		t.Fatalf("spec must keep env keys and never values: %s", snap.Spec)
	}
	if entries, _ := os.ReadDir(s.snapshotDir()); len(entries) != 1 {
		t.Fatal("temporary file left behind")
	}
}

func TestSnapshot_LimitsLeaveNothingBehind(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Service)
		want  error
	}{
		{"archive over the per-archive cap", func(s *Service) { s.cfg.Limits.Files.MaxArchiveBytes = 4 }, ErrFileTooLarge},
		{"archive over the remaining total budget", func(s *Service) { s.cfg.Snapshots.MaxTotalBytes = 4 }, ErrQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, p, _, row, _ := snapshotFixture(t)
			d.script = fake.Script{Stdout: []byte("0123456789")}
			tc.setup(s)
			if _, err := s.CreateSnapshot(context.Background(), p, row.ID); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if entries, _ := os.ReadDir(s.snapshotDir()); len(entries) != 0 {
				t.Fatalf("partial archive kept: %v", entries)
			}
			if n, err := s.store.Queries.CountSnapshots(context.Background(), ""); err != nil || n != 0 {
				t.Fatal("record created for a failed snapshot", n, err)
			}
			if s.snaps.pendingBytes != 0 || len(s.snaps.pendingOwner) != 0 {
				t.Fatal("reservation leaked")
			}
		})
	}
}

func TestSnapshot_PerKeyCountAndInFlightReservation(t *testing.T) {
	s, d, p, _, row, _ := snapshotFixture(t)
	s.cfg.Snapshots.MaxPerKey = 1
	// An admitted but unfinished snapshot counts, so concurrent starts cannot overshoot.
	_, _, release, err := s.admitSnapshot(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.admitSnapshot(context.Background(), p.ID); !errors.Is(err, ErrQuota) {
		t.Fatal("in-flight snapshot ignored", err)
	}
	release()
	d.script = fake.Script{Stdout: []byte("a")}
	if _, err = s.CreateSnapshot(context.Background(), p, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateSnapshot(context.Background(), p, row.ID); !errors.Is(err, ErrQuota) {
		t.Fatal("per-key limit not enforced", err)
	}
}

func TestSnapshot_OwnershipStateAndExecFailure(t *testing.T) {
	s, d, p, other, row, _ := snapshotFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte("a")}
	if _, err := s.CreateSnapshot(ctx, other, row.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("foreign sandbox must look absent", err)
	}
	snap, err := s.CreateSnapshot(ctx, p, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSnapshot(ctx, other, snap.ID); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal("foreign snapshot must look absent", err)
	}
	if err = s.DeleteSnapshot(ctx, other, snap.ID); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal(err)
	}
	if _, err = s.GetSnapshot(ctx, auth.Principal{ID: "admin", Admin: true}, snap.ID); err != nil {
		t.Fatal("admin must see all snapshots", err)
	}
	rows, err := s.ListSnapshots(ctx, other, ListRequest{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("listing leaked a foreign snapshot", rows, err)
	}
	if rows, err = s.ListSnapshots(ctx, p, ListRequest{Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if _, err = s.ListSnapshots(ctx, p, ListRequest{Limit: 0}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded page accepted", err)
	}
	d.code = 2
	d.script = fake.Script{Stdout: []byte("partial"), Exit: runtime.ExitInfo{ExitCode: protocol.TransportExit(2)}}
	if _, err = s.CreateSnapshot(ctx, p, row.ID); err == nil {
		t.Fatal("failed tar produced a snapshot")
	}
	if n, _ := s.store.Queries.CountSnapshots(ctx, ""); n != 1 {
		t.Fatal("failed archive recorded", n)
	}
	if err = s.Destroy(ctx, p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateSnapshot(ctx, p, row.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatal("ended sandbox was snapshotted", err)
	}
}

func TestSnapshot_DeleteRemovesArchiveThenRecord(t *testing.T) {
	s, d, p, _, row, _ := snapshotFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte("a")}
	snap, err := s.CreateSnapshot(ctx, p, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSnapshot(ctx, p, snap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(s.snapshotDir(), snap.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive kept", err)
	}
	if err = s.DeleteSnapshot(ctx, p, snap.ID); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal("second delete", err)
	}
	// A record whose archive vanished can still be deleted.
	again, err := s.CreateSnapshot(ctx, p, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.snapshotDir(), again.Path)); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSnapshot(ctx, p, again.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRestore_StreamsVerifiedArchiveAndAppliesSpec(t *testing.T) {
	s, d, p, _, _, _ := snapshotFixture(t)
	ctx := context.Background()
	memory := int64(512)
	source, err := s.Create(ctx, p, CreateRequest{Template: "base", Resources: Overrides{MemoryMB: &memory}})
	if err != nil {
		t.Fatal(err)
	}
	archive := []byte("workspace-bytes")
	d.script = fake.Script{Stdout: archive}
	snap, err := s.CreateSnapshot(ctx, p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	drainStarted(d)
	// The fake exits immediately, so hold the command open until its stdin is complete,
	// as a real tar that exits successfully has done.
	gate := make(chan struct{})
	d.script = fake.Script{ExitGate: gate}
	started := make(chan runtime.ExecRequest, 1)
	go func() {
		defer close(gate)
		started <- <-d.started
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(d.session.Input()) < len(archive); {
			time.Sleep(time.Millisecond)
		}
	}()
	// A ready pool member must stay unused: restores need a fresh, recorded instance.
	s.pool["base"] = []runtime.Instance{{ID: "pooled", SandboxID: "sbx_pooled"}}
	row, err := s.Create(ctx, p, CreateRequest{Template: "base", FromSnapshot: snap.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.pool["base"]) != 1 || row.ContainerID.String == "pooled" {
		t.Fatal("restore must not use a warm pool member")
	}
	if row.State != "running" || row.Source != "snapshot" || row.FromSnapshotID.String != snap.ID {
		t.Fatalf("restored sandbox metadata: %+v", row)
	}
	run := <-started
	if got := strings.Join(run.Cmd[len(run.Cmd)-7:], " "); got != "/bin/tar -xzf - --no-same-owner --no-same-permissions -C /workspace" {
		t.Fatalf("restore must extract inside the sandbox: %q", got)
	}
	if !bytes.Equal(d.session.Input(), archive) {
		t.Fatal("archive was not streamed unchanged")
	}
	spec, ok := d.Spec(row.ContainerID.String)
	if !ok || spec.Resources.MemoryMB != 512 {
		t.Fatalf("recorded resources not restored: %+v", spec.Resources)
	}
}

func TestRestore_FailuresDestroyTheSandbox(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(*testing.T, *Service, *helperFake, db.Snapshot)
		want   error
	}{
		{"tampered archive", func(t *testing.T, s *Service, _ *helperFake, snap db.Snapshot) {
			if err := os.WriteFile(filepath.Join(s.snapshotDir(), snap.Path), []byte("tampered-bytes!"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrSnapshotCorrupt},
		{"truncated archive", func(t *testing.T, s *Service, _ *helperFake, snap db.Snapshot) {
			if err := os.WriteFile(filepath.Join(s.snapshotDir(), snap.Path), []byte("short"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, ErrSnapshotCorrupt},
		{"missing archive", func(t *testing.T, s *Service, _ *helperFake, snap db.Snapshot) {
			if err := os.Remove(filepath.Join(s.snapshotDir(), snap.Path)); err != nil {
				t.Fatal(err)
			}
		}, ErrSnapshotCorrupt},
		{"extraction fails in the sandbox", func(_ *testing.T, _ *Service, d *helperFake, _ db.Snapshot) {
			d.code = 2
			d.script = fake.Script{Exit: runtime.ExitInfo{ExitCode: protocol.TransportExit(2)}}
		}, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, d, p, _, source, _ := snapshotFixture(t)
			ctx := context.Background()
			d.script = fake.Script{Stdout: []byte("workspace-bytes")}
			snap, err := s.CreateSnapshot(ctx, p, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			d.script = fake.Script{}
			tc.damage(t, s, d, snap)
			if _, err = s.Create(ctx, p, CreateRequest{Template: "base", FromSnapshot: snap.ID}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			rows, err := s.List(ctx, p, ListRequest{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Source == "snapshot" && (row.State != "destroyed" || row.EndReason.String != "restore_failed") {
					t.Fatalf("half-restored sandbox survived: %s %s", row.State, row.EndReason.String)
				}
			}
		})
	}
}

func TestRestore_RejectsMismatchesAndForeignSnapshots(t *testing.T) {
	s, d, p, other, source, _ := snapshotFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte("x")}
	snap, err := s.CreateSnapshot(ctx, p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, p, CreateRequest{Template: "other", FromSnapshot: snap.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatal("template mismatch accepted", err)
	}
	if _, err = s.Create(ctx, other, CreateRequest{Template: "base", FromSnapshot: snap.ID}); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal("foreign snapshot restored", err)
	}
	if _, err = s.Create(ctx, p, CreateRequest{Template: "base", FromSnapshot: "snp_aaaaaaaaaaaaaaaaaaaa"}); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal(err)
	}
	// A path stored in the record is never trusted to leave the snapshot directory.
	if _, err = s.store.SQL.ExecContext(ctx, "UPDATE snapshots SET path='../escape' WHERE id=?", snap.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, p, CreateRequest{Template: "base", FromSnapshot: snap.ID}); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatal("record path escaped the snapshot directory", err)
	}
}

func TestFork_CreatesIndependentSandboxesAndReportsPartialFailure(t *testing.T) {
	s, d, p, other, source, _ := snapshotFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte("x")}
	snap, err := s.CreateSnapshot(ctx, p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.script = fake.Script{}
	s.cfg.Snapshots.MaxFork = 3
	for _, count := range []int{0, 4, -1} {
		if _, err = s.Fork(ctx, p, snap.ID, count, CreateRequest{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("count %d: %v", count, err)
		}
	}
	if _, err = s.Fork(ctx, other, snap.ID, 1, CreateRequest{}); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatal("foreign snapshot forked", err)
	}
	// The source sandbox already holds one of the key's slots, leaving room for two.
	s.cfg.Limits.MaxSandboxesPerKey = 3
	labels := map[string]string{"attempt": "x"}
	result, err := s.Fork(ctx, p, snap.ID, 3, CreateRequest{Labels: labels})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || len(result.Failures) != 1 || !errors.Is(result.Failures[0].Err, ErrQuota) || result.Failures[0].Index < 0 || result.Failures[0].Index > 2 {
		t.Fatalf("partial failure not reported: %d ok, %+v", len(result.Items), result.Failures)
	}
	seen := map[string]bool{}
	for _, row := range result.Items {
		if row.Source != "snapshot" || row.State != "running" || row.FromSnapshotID.String != snap.ID || seen[row.ID] || row.ID == source.ID {
			t.Fatalf("fork member: %+v", row)
		}
		seen[row.ID] = true
	}
	labels["attempt"] = "mutated"
	if got, _ := s.Get(ctx, p, result.Items[0].ID); !strings.Contains(got.Labels.String, `"x"`) {
		t.Fatal("members alias the caller's maps")
	}
}

func TestFork_CancelledRequestLeavesNoSandboxes(t *testing.T) {
	s, d, p, _, source, _ := snapshotFixture(t)
	d.script = fake.Script{Stdout: []byte("x")}
	snap, err := s.CreateSnapshot(context.Background(), p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Fork(ctx, p, snap.ID, 2, CreateRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background(), p, ListRequest{Limit: 100, State: "running"})
	if err != nil || len(rows) != 1 || rows[0].ID != source.ID {
		t.Fatal("cancelled fork left running sandboxes", rows, err)
	}
}

func TestPruneSnapshots_RemovesOnlyInterruptedAndUnrecordedArchives(t *testing.T) {
	s, d, p, _, source, _ := snapshotFixture(t)
	ctx := context.Background()
	d.script = fake.Script{Stdout: []byte("x")}
	kept, err := s.CreateSnapshot(ctx, p, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := s.snapshotDir()
	files := map[string]bool{
		"snp_aaaaaaaaaaaaaaaaaaaa.tar.gz.part": false,
		"snp_bbbbbbbbbbbbbbbbbbbb.tar.gz":      false,
		"notes.txt":                            true,
		kept.Path:                              true,
	}
	for name := range files {
		if _, statErr := os.Stat(filepath.Join(dir, name)); name != kept.Path && statErr != nil {
			if err = os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = s.pruneSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
	for name, survives := range files {
		_, err = os.Stat(filepath.Join(dir, name))
		if survives != (err == nil) {
			t.Fatalf("%s: survives=%t err=%v", name, survives, err)
		}
	}
	// A missing directory is a clean install, not an error.
	s.cfg.DataDir = t.TempDir()
	if err = s.pruneSnapshots(ctx); err != nil {
		t.Fatal(err)
	}
}
