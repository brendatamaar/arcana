// Package events carries live, metadata-only lifecycle events and in-memory exec output tails to the
// dashboard. Nothing here is persisted: event and output buffers are fixed-size rings.
package events

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Event types published by the orchestrator.
const (
	SandboxCreated = "sandbox.created"
	SandboxState   = "sandbox.state"
	SandboxEnded   = "sandbox.destroyed"
	ExecStarted    = "exec.started"
	ExecEnded      = "exec.ended"
	PoolChanged    = "pool.changed"
)

// subscriberBuffer bounds each subscriber's backlog. A subscriber that falls this far behind is
// dropped and told to resync (FR-WD-6) rather than allowed to grow memory.
const subscriberBuffer = 64

// ErrTooManySubscribers is returned when the subscriber cap is reached.
var ErrTooManySubscribers = errors.New("too many event subscribers")

// Event is one lifecycle fact. OwnerKeyID is used only for filtering and is never sent to clients.
type Event struct {
	ID         uint64          `json:"-"`
	Type       string          `json:"type"`
	OwnerKeyID string          `json:"-"`
	SandboxID  string          `json:"sandbox_id,omitempty"`
	At         time.Time       `json:"at"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// Filter selects the events a subscriber may see.
type Filter func(Event) bool

// ForPrincipal returns the visibility filter for a key: admins see everything, other keys only
// events of sandboxes they own, and never installation-wide pool events.
func ForPrincipal(keyID string, admin bool) Filter {
	return func(e Event) bool {
		if admin {
			return true
		}
		return e.Type != PoolChanged && e.OwnerKeyID == keyID
	}
}

// Clock supplies event timestamps.
type Clock interface{ Now() time.Time }

// Bus is an in-process pub/sub with a ring of recent events for resume.
type Bus struct {
	clock    Clock
	capacity int
	maxSubs  int

	mu   sync.Mutex
	next uint64 // ID of the next event; IDs start at 1
	ring []Event
	subs map[*Subscription]struct{}
}

// NewBus keeps the last capacity events and admits at most maxSubscribers live subscribers.
func NewBus(clock Clock, capacity, maxSubscribers int) *Bus {
	return &Bus{clock: clock, capacity: max(capacity, 1), maxSubs: max(maxSubscribers, 1), next: 1, subs: make(map[*Subscription]struct{})}
}

// Publish records an event and fans it out without ever blocking the caller. A nil bus is a no-op,
// so the orchestrator can run without a dashboard.
func (b *Bus) Publish(eventType, ownerKeyID, sandboxID string, data any) {
	if b == nil {
		return
	}
	var raw json.RawMessage
	if data != nil {
		raw, _ = json.Marshal(data)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := Event{ID: b.next, Type: eventType, OwnerKeyID: ownerKeyID, SandboxID: sandboxID, At: b.clock.Now().UTC(), Data: raw}
	b.next++
	b.ring = append(b.ring, e)
	if len(b.ring) > b.capacity {
		b.ring = append(b.ring[:0], b.ring[len(b.ring)-b.capacity:]...)
	}
	for s := range b.subs {
		if !s.filter(e) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			// Too slow: drop it. Its channel is closed after the buffered events, and Dropped
			// tells the consumer to send a reset and disconnect.
			s.dropped.Store(true)
			delete(b.subs, s)
			close(s.ch)
		}
	}
}

// Subscribers returns the number of live subscribers, for metrics.
func (b *Bus) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Subscription delivers replayed and live events.
type Subscription struct {
	bus     *Bus
	filter  Filter
	ch      chan Event
	dropped atomic.Bool
	// Replay holds events newer than the client's Last-Event-ID, to deliver before Live.
	Replay []Event
	// ResetNow means the requested resume point is unknown (too old, or from before a restart):
	// send a reset immediately instead of events.
	ResetNow bool
}

// Live returns the channel of new events. It is closed when the subscriber is dropped or closed.
func (s *Subscription) Live() <-chan Event { return s.ch }

// Dropped reports whether the bus dropped this subscriber for being too slow.
func (s *Subscription) Dropped() bool { return s.dropped.Load() }

// Close unsubscribes; it is idempotent.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if _, ok := s.bus.subs[s]; ok {
		delete(s.bus.subs, s)
		close(s.ch)
	}
}

// Subscribe registers a subscriber. With resume set, events after lastID that pass the filter are
// returned in Replay, atomically with registration so none are missed or duplicated.
func (b *Bus) Subscribe(filter Filter, resume bool, lastID uint64) (*Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) >= b.maxSubs {
		return nil, ErrTooManySubscribers
	}
	s := &Subscription{bus: b, filter: filter, ch: make(chan Event, subscriberBuffer)}
	if resume {
		switch {
		case lastID >= b.next:
			s.ResetNow = true // from a previous run of the server
		case len(b.ring) > 0 && lastID+1 < b.ring[0].ID:
			s.ResetNow = true // the events in between have been overwritten
		case len(b.ring) == 0 && lastID+1 < b.next:
			s.ResetNow = true
		default:
			for _, e := range b.ring {
				if e.ID > lastID && filter(e) {
					s.Replay = append(s.Replay, e)
				}
			}
			if len(s.Replay) > subscriberBuffer*4 {
				// A replay this large is better served by a refetch than by a burst.
				s.Replay, s.ResetNow = nil, true
			}
		}
	}
	if s.ResetNow {
		close(s.ch)
		s.dropped.Store(true)
		return s, nil
	}
	b.subs[s] = struct{}{}
	return s, nil
}
