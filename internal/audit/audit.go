// Package audit persists bounded metadata events without blocking request handlers on storage.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
)

// Event contains metadata only; producers must exclude secrets, env values and output.
type Event struct {
	ID        string          `json:"id"`
	TS        time.Time       `json:"ts"`
	KeyID     string          `json:"key_id"`
	SandboxID string          `json:"sandbox_id"`
	Action    string          `json:"action"`
	Details   json.RawMessage `json:"details"`
}

// Backend is the worker-owned storage boundary.
type Backend interface {
	Append(context.Context, Event) error
	Prune(context.Context, time.Time) error
}

// Timer and Clock allow retention scheduling without sleeps in tests.
type Timer interface {
	Chan() <-chan time.Time
	Stop()
}

// Clock supplies timestamps and owned retention timers.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

// SystemClock supplies UTC wall time and owned timers.
type (
	SystemClock struct{}
	systemTimer struct{ timer *time.Timer }
)

// Now returns UTC wall time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// NewTimer creates a timer that the worker stops on shutdown.
func (SystemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }
func (t systemTimer) Chan() <-chan time.Time       { return t.timer.C }
func (t systemTimer) Stop()                        { t.timer.Stop() }

// Stats exposes monotonic counters for metrics and diagnostics.
type Stats struct {
	Dropped, Errors, Written uint64
	Queued                   int
}

// Writer owns one bounded queue and its joined storage worker.
type Writer struct {
	keyPattern                 *regexp.Regexp
	backend                    Backend
	clock                      Clock
	retention                  time.Duration
	queue                      chan Event
	mu                         sync.RWMutex
	closed                     bool
	cancel                     context.CancelFunc
	done                       chan struct{}
	dropped, failures, written atomic.Uint64
	observer                   func(Event)
}

// New starts the worker; its owner must call Close before closing the database.
func New(backend Backend, clock Clock, retentionDays, capacity int, observer func(Event)) (*Writer, error) {
	if backend == nil || clock == nil || retentionDays < 1 || retentionDays > 3650 || capacity < 1 || capacity > 4096 {
		return nil, errors.New("invalid audit configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Writer{keyPattern: regexp.MustCompile(`arc_live_[A-Za-z0-9]{32}`), backend: backend, clock: clock, retention: time.Duration(retentionDays) * 24 * time.Hour, queue: make(chan Event, capacity), cancel: cancel, done: make(chan struct{}), observer: observer}
	go w.run(ctx)
	return w, nil
}

// Record copies bounded metadata and performs nonblocking admission.
func (w *Writer) Record(action, key, sandbox string, details any) {
	if w == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	data, err := json.Marshal(details)
	if err != nil || len(data) == 0 || data[0] != '{' || len(data) > 131072 || len(action) > 128 || action == "" || len(key) > 128 || len(sandbox) > 128 {
		w.dropped.Add(1)
		return
	}
	data = w.keyPattern.ReplaceAllLiteral(data, []byte("[redacted]"))
	event := Event{TS: w.clock.Now(), Action: action, KeyID: key, SandboxID: sandbox, Details: slices.Clone(data)}
	if w.observer != nil {
		w.observer(event)
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	select {
	case w.queue <- event:
	default:
		w.dropped.Add(1)
	}
}

// AddDropped accounts for loss in upstream bounded proxy queues.
func (w *Writer) AddDropped(n uint64) {
	if w != nil {
		w.dropped.Add(n)
	}
}

// Stats returns a concurrent snapshot.
func (w *Writer) Stats() Stats {
	return Stats{Dropped: w.dropped.Load(), Errors: w.failures.Load(), Written: w.written.Load(), Queued: len(w.queue)}
}

func (w *Writer) run(ctx context.Context) {
	defer close(w.done)
	w.prune(ctx)
	timer := w.clock.NewTimer(24 * time.Hour)
	defer func() { timer.Stop() }()
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case _, ok := <-w.queue:
					if !ok {
						return
					}
					w.dropped.Add(1)
				default:
					return
				}
			}
		case <-timer.Chan():
			w.prune(ctx)
			timer.Stop()
			timer = w.clock.NewTimer(24 * time.Hour)
		case event, ok := <-w.queue:
			if !ok {
				return
			}
			id, err := ids.New("aud")
			event.ID = id
			op, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err == nil {
				err = w.backend.Append(op, event)
			}
			cancel()
			if err != nil {
				w.failures.Add(1)
			} else {
				w.written.Add(1)
			}
		}
	}
}

// Close stops admission, drains within ctx, and joins the worker on every path.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		w.cancel()
		return nil
	case <-ctx.Done():
		w.cancel()
		<-w.done
		return ctx.Err()
	}
}

func (w *Writer) prune(ctx context.Context) {
	op, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := w.backend.Prune(op, w.clock.Now().Add(-w.retention)); err != nil {
		w.failures.Add(1)
	}
}
