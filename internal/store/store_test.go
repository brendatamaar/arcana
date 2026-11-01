package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/store/db"
)

func TestOpen_MigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "state", "arcana.db")
	s, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	original := s.InstallID
	if original == "" {
		t.Fatal("missing install ID")
	}
	for _, table := range []string{"api_keys", "sandboxes", "execs", "meta"} {
		var found string
		if err := s.SQL.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&found); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if s.InstallID != original {
		t.Fatalf("installation ID changed: %q -> %q", original, s.InstallID)
	}
	var migrations int
	if err = s.SQL.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied=1").Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("migration reapplied: %d %v", migrations, err)
	}
}

func TestOpen_ConnectionSettingsAndQueries(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "special # name.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	for pragma, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if err := s.SQL.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", pragma, got, want)
		}
	}
	if s.SQL.Stats().MaxOpenConnections != 1 {
		t.Fatal("writer pool is not limited")
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	params := db.CreateKeyParams{ID: "key_a", Name: "test", Prefix: "arc_live", Hash: []byte("hash"), IsAdmin: 1, CreatedAt: now}
	key, err := s.Queries.CreateKey(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "test" || !key.CreatedAt.Equal(now) {
		t.Fatalf("key round trip: %+v", key)
	}
	if _, err = s.Queries.CreateKey(ctx, params); err == nil {
		t.Fatal("duplicate key accepted")
	}
	got, err := s.Queries.GetKeyByHash(ctx, []byte("hash"))
	if err != nil || got.ID != key.ID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if _, err = s.SQL.ExecContext(ctx, "INSERT INTO execs (id,sandbox_id,command,is_shell,started_at) VALUES ('exe_a','missing','[]',0,?)", now); err == nil {
		t.Fatal("foreign key not enforced")
	}
	count, err := s.Queries.CountKeys(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count: %d %v", count, err)
	}
	changed, err := s.Queries.RevokeKey(ctx, db.RevokeKeyParams{ID: key.ID, RevokedAt: sql.NullTime{Time: now, Valid: true}})
	if err != nil || changed != 1 {
		t.Fatalf("revoke: %d %v", changed, err)
	}
	changed, err = s.Queries.RevokeKey(ctx, db.RevokeKeyParams{ID: key.ID, RevokedAt: sql.NullTime{Time: now, Valid: true}})
	if err != nil || changed != 0 {
		t.Fatalf("second revoke: %d %v", changed, err)
	}
}

func TestOpen_RejectsInvalidPathsAndCancellation(t *testing.T) {
	if _, err := Open(context.Background(), ""); err == nil {
		t.Fatal("accepted empty path")
	}
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("accepted directory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "cancelled.db")); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestQueries_ListLimitsAreBounded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = s.SQL.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1001)
 INSERT INTO api_keys(id,name,prefix,hash,is_admin,created_at)
 SELECT printf('key_%04d',x),'test','prefix',CAST(printf('hash_%04d',x) AS BLOB),0,'2026-11-01 00:00:00' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ limit, want int64 }{{-1, 1}, {2, 2}, {5000, 1000}} {
		rows, err := s.Queries.ListKeys(ctx, db.ListKeysParams{ID: "", PageLimit: tt.limit})
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(rows)) != tt.want {
			t.Fatalf("limit %d returned %d rows, want %d", tt.limit, len(rows), tt.want)
		}
	}
}
