package audit_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

type backend struct {
	started chan struct{}
	release chan struct{}
	pruned  chan time.Time
	fail    bool
}

func (b *backend) Append(ctx context.Context, _ audit.Event) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if b.fail {
		return errors.New("write failed")
	}
	return nil
}

func (b *backend) Prune(ctx context.Context, before time.Time) error {
	if b.pruned == nil {
		return nil
	}
	select {
	case b.pruned <- before:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type timer struct{ channel chan time.Time }

func (t timer) Chan() <-chan time.Time { return t.channel }
func (timer) Stop()                    {}

type clock struct {
	at     time.Time
	timers chan timer
}

func (c clock) Now() time.Time { return c.at }
func (c clock) NewTimer(time.Duration) audit.Timer {
	t := timer{make(chan time.Time, 1)}
	c.timers <- t
	return t
}

func TestWriter_NonblockingOverflowDrainAndFailureCounters(t *testing.T) {
	b := &backend{started: make(chan struct{}, 1), release: make(chan struct{}), pruned: make(chan time.Time, 1)}
	writer, err := audit.New(b, audit.SystemClock{}, 30, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, writer)
	writer.Record("first", "", "", nil)
	<-b.started
	writer.Record("second", "", "", map[string]string{"path": "/workspace/probe"})
	writer.Record("overflow", "", "", nil)
	if writer.Stats().Dropped != 1 {
		t.Fatal(writer.Stats())
	}
	close(b.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	writer.Record("closed", "", "", nil)
	if writer.Stats().Written != 2 || writer.Stats().Dropped != 2 {
		t.Fatal(writer.Stats())
	}
	failing := &backend{started: make(chan struct{}, 1), fail: true}
	failed, err := audit.New(failing, audit.SystemClock{}, 30, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, failed)
	failed.Record("failure", "", "", nil)
	if err = failed.Close(ctx); err != nil || failed.Stats().Errors != 1 {
		t.Fatal(err, failed.Stats())
	}
}

func TestWriter_RetentionClockAndCancellationJoin(t *testing.T) {
	c := clock{at: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), timers: make(chan timer, 2)}
	b := &backend{started: make(chan struct{}, 1), release: make(chan struct{}), pruned: make(chan time.Time, 1)}
	writer, err := audit.New(b, c, 30, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, writer)
	<-b.pruned // startup pruning also enforces retention across frequent restarts.
	tick := <-c.timers
	tick.channel <- c.at
	if before := <-b.pruned; !before.Equal(c.at.Add(-30 * 24 * time.Hour)) {
		t.Fatal(before)
	}
	writer.Record("blocked", "", "", nil)
	<-b.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = writer.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func database(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestStore_TiedTimestampsFiltersCursorAndPruning(t *testing.T) {
	db := database(t)
	backend := audit.SQLBackend{Store: db}
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for _, event := range []audit.Event{
		{ID: "aud_aaaaaaaaaaaaaaaaaaaa", TS: now, KeyID: "owner", SandboxID: "sandbox", Action: "file.written", Details: []byte("{}")},
		{ID: "aud_bbbbbbbbbbbbbbbbbbbb", TS: now, KeyID: "owner", SandboxID: "sandbox", Action: "file.written", Details: []byte("{}")},
		{ID: "aud_cccccccccccccccccccc", TS: now.Add(-31 * 24 * time.Hour), Action: "old", Details: []byte("{}")},
	} {
		if err := backend.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	filter := audit.Filter{KeyID: "owner", SandboxID: "sandbox", Action: "file.written", Since: now.Add(-time.Hour), Until: now, Limit: 1}
	first, err := backend.List(ctx, filter)
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	filter.Cursor = first.NextCursor
	second, err := backend.List(ctx, filter)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatal(second, err)
	}
	if err = backend.Prune(ctx, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	all, err := backend.List(ctx, audit.Filter{})
	if err != nil || len(all.Items) != 2 {
		t.Fatal(all, err)
	}
	for _, filter := range []audit.Filter{{Cursor: "invalid!"}, {Limit: 1001}, {Limit: -1}, {Since: now, Until: now.Add(-time.Hour)}} {
		if _, err := backend.List(ctx, filter); !errors.Is(err, audit.ErrFilter) {
			t.Fatal(err)
		}
	}
}

func TestProducers_OwnerAttributionAndNoEnvironmentValues(t *testing.T) {
	db := database(t)
	writer, err := audit.New(audit.SQLBackend{Store: db}, audit.SystemClock{}, 30, 32, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys := auth.New(db, auth.SystemClock{}, writer)
	key, secret, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	service := orchestrator.New(db, fake.New(), config.Default(), orchestrator.SystemClock{}, writer)
	row, err := service.Create(ctx, auth.Principal{ID: key.ID}, orchestrator.CreateRequest{Template: "base", Env: map[string]string{"SECRET": "private-env-value"}}) // #nosec G101 -- synthetic fixture proving environment values never enter audit metadata.
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, writer)
	writer.Record("egress.decision", "", row.ID, map[string]any{"host": "example.com", "allowed": false})
	if err = service.Destroy(ctx, auth.Principal{ID: key.ID}, row.ID, "api"); err != nil {
		t.Fatal(err)
	}
	if err = keys.Revoke(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := writer.List(ctx, audit.Filter{KeyID: key.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Items {
		if event.Action == "egress.decision" {
			found = true
		}
		if strings.Contains(string(event.Details), secret) || strings.Contains(string(event.Details), "private-env-value") {
			t.Fatal("secret in audit metadata")
		}
	}
	if !found {
		t.Fatal("egress owner attribution missing")
	}
}

// Concurrent producers must remain race-free even when shutdown closes admission.
func TestWriter_ConcurrentAdmissionAndClose(t *testing.T) {
	b := &backend{started: make(chan struct{}, 1)}
	writer, err := audit.New(b, audit.SystemClock{}, 30, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, writer)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 100 {
				writer.Record("concurrent", "", "", nil)
			}
		})
	}
	if err = writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	group.Wait()
}

func cleanupWriter(t *testing.T, writer *audit.Writer) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := writer.Close(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestWriter_RedactsAPIKeysEvenInExplicitCommandMetadata(t *testing.T) {
	secret, _, err := auth.Generate()
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan audit.Event, 1)
	writer, err := audit.New(&backend{}, audit.SystemClock{}, 30, 1, func(event audit.Event) { observed <- event })
	if err != nil {
		t.Fatal(err)
	}
	cleanupWriter(t, writer)
	writer.Record("exec.started", "", "", map[string]any{"command": []string{"echo", secret}})
	event := <-observed
	if strings.Contains(string(event.Details), secret) || !strings.Contains(string(event.Details), "[redacted]") {
		t.Fatal("credential redaction failed")
	}
}
