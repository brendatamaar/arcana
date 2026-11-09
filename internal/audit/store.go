package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/internal/store/db"
)

// SQLBackend stores events through generated bounded queries.
type SQLBackend struct{ Store *store.Store }

// Append fills owner attribution for egress and internal lifecycle events.
func (b SQLBackend) Append(ctx context.Context, event Event) error {
	if event.KeyID == "" && event.SandboxID != "" {
		row, err := b.Store.Queries.GetSandbox(ctx, event.SandboxID)
		if err == nil {
			event.KeyID = row.OwnerKeyID
		}
	}
	return b.Store.Queries.AppendAuditEvent(ctx, db.AppendAuditEventParams{ID: event.ID, Ts: event.TS, KeyID: event.KeyID, SandboxID: event.SandboxID, Action: event.Action, Details: string(event.Details)})
}

// Prune deletes metadata older than the configured retention.
func (b SQLBackend) Prune(ctx context.Context, before time.Time) error {
	_, err := b.Store.Queries.PruneAuditEvents(ctx, before)
	return err
}

// Filter describes one bounded chronological audit page.
type Filter struct {
	SandboxID, KeyID, Action, Cursor string
	Since, Until                     time.Time
	Limit                            int64
}

// Page carries a chronological result and an opaque timestamp/ID cursor.
type Page struct {
	Items      []Event
	NextCursor string
}
type cursor struct {
	TS time.Time
	ID string
}

// ErrFilter indicates invalid pagination or filter input.
var ErrFilter = errors.New("invalid audit filters")

// List validates all filters before invoking SQL.
func (b SQLBackend) List(ctx context.Context, filter Filter) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Limit < 1 || filter.Limit > 1000 || len(filter.Cursor) > 512 || len(filter.SandboxID) > 128 || len(filter.KeyID) > 128 || len(filter.Action) > 128 {
		return Page{}, ErrFilter
	}
	after := cursor{TS: time.Unix(0, 0).UTC()}
	if filter.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(filter.Cursor)
		if err != nil || json.Unmarshal(data, &after) != nil || len(after.ID) > 128 || after.ID == "" || after.TS.IsZero() {
			return Page{}, ErrFilter
		}
	}
	if filter.Since.IsZero() {
		filter.Since = time.Unix(0, 0).UTC()
	}
	if filter.Until.IsZero() {
		filter.Until = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	if filter.Since.After(filter.Until) {
		return Page{}, ErrFilter
	}
	rows, err := b.Store.Queries.ListAuditEvents(ctx, db.ListAuditEventsParams{SandboxFilter: filter.SandboxID, KeyFilter: filter.KeyID, ActionFilter: filter.Action, SinceTs: filter.Since, UntilTs: filter.Until, AfterTs: after.TS, AfterID: after.ID, PageLimit: filter.Limit})
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: make([]Event, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, Event{ID: row.ID, TS: row.Ts, KeyID: row.KeyID, SandboxID: row.SandboxID, Action: row.Action, Details: json.RawMessage(row.Details)})
	}
	if int64(len(rows)) == filter.Limit {
		last := rows[len(rows)-1]
		data, _ := json.Marshal(cursor{TS: last.Ts, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}

// List delegates queries to a storage backend supporting bounded pagination.
func (w *Writer) List(ctx context.Context, filter Filter) (Page, error) {
	backend, ok := w.backend.(interface {
		List(context.Context, Filter) (Page, error)
	})
	if !ok {
		return Page{}, errors.New("audit backend cannot query")
	}
	return backend.List(ctx, filter)
}
