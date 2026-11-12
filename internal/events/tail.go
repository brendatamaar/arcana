package events

import (
	"context"
	"errors"
	"sync"
	"time"
)

// maxTails bounds the registry: worst-case memory is maxTails × tail_buffer_bytes.
const maxTails = 512

// ErrTooManyTailSubscribers is returned when an exec already has its maximum of readers.
var ErrTooManyTailSubscribers = errors.New("too many output subscribers")

// Chunk is a run of output bytes from one stream.
type Chunk struct {
	Seq    uint64
	Stderr bool
	Data   []byte
}

// End is the terminal summary of an exec.
type End struct {
	ExitCode  int
	Known     bool // false when the real exit status is unknown (for example a destroyed sandbox)
	TimedOut  bool
	Truncated bool
	Cancelled bool
}

// Tails is a bounded registry of in-memory exec output rings. Output is never written to disk
// (FR-EX-8): each ring keeps only the newest bytes and is dropped after a retention period.
type Tails struct {
	clock     Clock
	capBytes  int64
	retention time.Duration
	maxSubs   int

	mu     sync.Mutex
	byExec map[string]*Tail
	order  []string // insertion order, for eviction
}

// NewTails configures ring size, post-exit retention and the reader cap per exec.
func NewTails(clock Clock, capBytes int64, retention time.Duration, maxSubscribers int) *Tails {
	return &Tails{clock: clock, capBytes: max(capBytes, 1), retention: retention, maxSubs: max(maxSubscribers, 1), byExec: make(map[string]*Tail)}
}

// Tail is one exec's output ring.
type Tail struct {
	owner, sandbox string
	capBytes       int64
	maxSubs        int

	mu      sync.Mutex
	chunks  []Chunk
	bytes   int64
	next    uint64 // sequence of the next chunk
	end     *End
	expires time.Time
	subs    int
	wake    chan struct{}
}

// Open registers a tail for an exec. It returns nil when the registry is full of running execs; a
// nil *Tail accepts writes and drops them, so callers need no special case.
func (t *Tails) Open(execID, ownerKeyID, sandboxID string) *Tail {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	if len(t.byExec) >= maxTails {
		t.evictEndedLocked()
		if len(t.byExec) >= maxTails {
			return nil
		}
	}
	tail := &Tail{owner: ownerKeyID, sandbox: sandboxID, capBytes: t.capBytes, maxSubs: t.maxSubs, next: 1, wake: make(chan struct{})}
	t.byExec[execID] = tail
	t.order = append(t.order, execID)
	return tail
}

// Lookup returns a retained tail, dropping expired ones first.
func (t *Tails) Lookup(execID string) (*Tail, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	tail, ok := t.byExec[execID]
	return tail, ok
}

// Finish records the exec's end and starts the retention clock.
func (t *Tails) Finish(tail *Tail, end End) {
	if tail == nil {
		return
	}
	tail.finish(end)
	t.mu.Lock()
	tail.mu.Lock()
	tail.expires = t.clock.Now().Add(t.retention)
	tail.mu.Unlock()
	t.mu.Unlock()
}

func (t *Tails) pruneLocked() {
	now := t.clock.Now()
	kept := t.order[:0]
	for _, id := range t.order {
		tail, ok := t.byExec[id]
		if !ok {
			continue
		}
		tail.mu.Lock()
		gone := tail.end != nil && !tail.expires.IsZero() && !now.Before(tail.expires)
		tail.mu.Unlock()
		if gone {
			delete(t.byExec, id)
			continue
		}
		kept = append(kept, id)
	}
	t.order = kept
}

func (t *Tails) evictEndedLocked() {
	for i, id := range t.order {
		tail := t.byExec[id]
		tail.mu.Lock()
		ended := tail.end != nil
		tail.mu.Unlock()
		if ended {
			delete(t.byExec, id)
			t.order = append(t.order[:i], t.order[i+1:]...)
			return
		}
	}
}

// Len returns the number of retained tails, for tests and diagnostics.
func (t *Tails) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byExec)
}

// Owner returns the key that owns the exec's sandbox.
func (tail *Tail) Owner() string { return tail.owner }

// Sandbox returns the exec's sandbox ID.
func (tail *Tail) Sandbox() string { return tail.sandbox }

// Write appends output, discarding the oldest bytes beyond the ring size. A nil tail drops it.
func (tail *Tail) Write(stderr bool, data []byte) {
	if tail == nil || len(data) == 0 {
		return
	}
	tail.mu.Lock()
	defer tail.mu.Unlock()
	if int64(len(data)) > tail.capBytes {
		data = data[int64(len(data))-tail.capBytes:]
	}
	tail.chunks = append(tail.chunks, Chunk{Seq: tail.next, Stderr: stderr, Data: append([]byte(nil), data...)})
	tail.next++
	tail.bytes += int64(len(data))
	for tail.bytes > tail.capBytes && len(tail.chunks) > 1 {
		tail.bytes -= int64(len(tail.chunks[0].Data))
		tail.chunks[0] = Chunk{}
		tail.chunks = tail.chunks[1:]
	}
	if tail.bytes > tail.capBytes { // a single chunk is larger than the ring
		extra := tail.bytes - tail.capBytes
		tail.chunks[0].Data = tail.chunks[0].Data[extra:]
		tail.bytes = tail.capBytes
	}
	tail.signalLocked()
}

func (tail *Tail) finish(end End) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	if tail.end == nil {
		tail.end = &end
	}
	tail.signalLocked()
}

func (tail *Tail) signalLocked() {
	close(tail.wake)
	tail.wake = make(chan struct{})
}

// TailSub reads a tail from where it was opened. Release must be called.
type TailSub struct {
	tail *Tail
	pos  uint64
	done bool
}

// Subscribe starts a reader at the oldest retained chunk.
func (tail *Tail) Subscribe() (*TailSub, error) {
	tail.mu.Lock()
	defer tail.mu.Unlock()
	if tail.subs >= tail.maxSubs {
		return nil, ErrTooManyTailSubscribers
	}
	tail.subs++
	return &TailSub{tail: tail, pos: 0}, nil
}

// Release frees the reader slot; it is idempotent.
func (s *TailSub) Release() {
	s.tail.mu.Lock()
	defer s.tail.mu.Unlock()
	if !s.done {
		s.done = true
		s.tail.subs--
	}
}

// Batch is what a reader receives: buffered output, whether older output was lost to the ring, and
// the terminal summary once the exec ended and all output was delivered.
type Batch struct {
	Chunks  []Chunk
	Dropped bool
	End     *End
}

// Next blocks until there is output past the reader's position, the exec has ended, or ctx is done.
func (s *TailSub) Next(ctx context.Context) (Batch, error) {
	for {
		s.tail.mu.Lock()
		var batch Batch
		for _, c := range s.tail.chunks {
			if c.Seq > s.pos {
				batch.Chunks = append(batch.Chunks, Chunk{Seq: c.Seq, Stderr: c.Stderr, Data: append([]byte(nil), c.Data...)})
			}
		}
		if n := len(batch.Chunks); n > 0 {
			// A gap between what the reader has seen and the first chunk it can still get means the
			// ring overwrote older output (for a new reader, sequence 1 is the start of the output).
			batch.Dropped = batch.Chunks[0].Seq > s.pos+1
			s.pos = batch.Chunks[n-1].Seq
			s.tail.mu.Unlock()
			return batch, nil
		}
		if s.tail.end != nil {
			end := *s.tail.end
			s.tail.mu.Unlock()
			return Batch{End: &end}, nil
		}
		wake := s.tail.wake
		s.tail.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return Batch{}, ctx.Err()
		}
	}
}

// Subscribers returns the number of active output readers across all tails, for metrics.
func (t *Tails) Subscribers() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for _, tail := range t.byExec {
		tail.mu.Lock()
		total += tail.subs
		tail.mu.Unlock()
	}
	return total
}
