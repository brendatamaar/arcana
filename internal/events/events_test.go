package events

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func newClock() *testClock { return &testClock{at: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)} }

func drain(s *Subscription) []Event {
	var out []Event
	for {
		select {
		case e, ok := <-s.Live():
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestBus_FiltersByOwnerAndHidesPoolEventsFromNonAdmins(t *testing.T) {
	bus := NewBus(newClock(), 10, 10)
	admin, _ := bus.Subscribe(ForPrincipal("key_admin", true), false, 0)
	owner, _ := bus.Subscribe(ForPrincipal("key_a", false), false, 0)
	other, _ := bus.Subscribe(ForPrincipal("key_b", false), false, 0)
	bus.Publish(SandboxCreated, "key_a", "sbx_1", map[string]string{"template": "base"})
	bus.Publish(PoolChanged, "", "", map[string]int{"available": 2})
	bus.Publish(ExecStarted, "key_b", "sbx_2", nil)
	if got := drain(admin); len(got) != 3 {
		t.Fatalf("admin sees everything: %d", len(got))
	}
	if got := drain(owner); len(got) != 1 || got[0].SandboxID != "sbx_1" || got[0].ID != 1 {
		t.Fatalf("owner: %+v", got)
	}
	if got := drain(other); len(got) != 1 || got[0].SandboxID != "sbx_2" {
		t.Fatalf("other: %+v", got)
	}
	// The owner key is internal: it is never part of the JSON clients receive.
	data, _ := json.Marshal(drainOne(t, bus))
	if strings.Contains(string(data), "key_a") || strings.Contains(string(data), "OwnerKeyID") {
		t.Fatalf("owner leaked: %s", data)
	}
}

func drainOne(t *testing.T, bus *Bus) Event {
	t.Helper()
	s, _ := bus.Subscribe(func(Event) bool { return true }, false, 0)
	bus.Publish(SandboxState, "key_a", "sbx_z", map[string]string{"state": "running"})
	got := drain(s)
	if len(got) != 1 {
		t.Fatal("no event")
	}
	return got[0]
}

func TestBus_ResumeReplaysMissedEventsWithoutGapsOrDuplicates(t *testing.T) {
	bus := NewBus(newClock(), 5, 10)
	all := func(Event) bool { return true }
	for i := 0; i < 4; i++ {
		bus.Publish(SandboxState, "k", "sbx", nil) // IDs 1..4
	}
	s, err := bus.Subscribe(all, true, 2)
	if err != nil || s.ResetNow || len(s.Replay) != 2 || s.Replay[0].ID != 3 || s.Replay[1].ID != 4 {
		t.Fatalf("%+v %v", s, err)
	}
	bus.Publish(SandboxState, "k", "sbx", nil) // ID 5, live
	if live := drain(s); len(live) != 1 || live[0].ID != 5 {
		t.Fatalf("live after replay: %+v", live)
	}
	// Resuming at the newest event replays nothing and does not reset.
	if s, _ = bus.Subscribe(all, true, 5); s.ResetNow || len(s.Replay) != 0 {
		t.Fatalf("%+v", s)
	}
	// Overwritten history: only IDs 3..7 remain after two more events.
	bus.Publish(SandboxState, "k", "sbx", nil)
	bus.Publish(SandboxState, "k", "sbx", nil)
	if s, _ = bus.Subscribe(all, true, 1); !s.ResetNow || !s.Dropped() {
		t.Fatalf("gap not detected: %+v", s)
	}
	if s, _ = bus.Subscribe(all, true, 2); s.ResetNow {
		t.Fatal("ID 3 is the oldest retained, so resuming from 2 needs no reset")
	}
	// An ID from a previous server run (higher than anything issued) must reset too.
	if s, _ = bus.Subscribe(all, true, 9999); !s.ResetNow {
		t.Fatal("stale Last-Event-ID accepted")
	}
	// Filters apply to the replay.
	only := func(e Event) bool { return e.OwnerKeyID == "nobody" }
	if s, _ = bus.Subscribe(only, true, 3); len(s.Replay) != 0 {
		t.Fatal("replay ignored the filter")
	}
}

func TestBus_SlowSubscribersAreDroppedWithoutBlockingPublishers(t *testing.T) {
	bus := NewBus(newClock(), 1000, 10)
	slow, _ := bus.Subscribe(func(Event) bool { return true }, false, 0)
	fast, _ := bus.Subscribe(func(Event) bool { return true }, false, 0)
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*3; i++ {
			bus.Publish(SandboxState, "k", "s", nil)
			drain(fast) // the fast one keeps up
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled subscriber blocked Publish")
	}
	if !slow.Dropped() {
		t.Fatal("slow subscriber was not dropped")
	}
	got := 0
	for range slow.Live() { // closed after its buffered events
		got++
	}
	if got != subscriberBuffer {
		t.Fatalf("buffered events before the drop: %d", got)
	}
	if fast.Dropped() || bus.Subscribers() != 1 {
		t.Fatalf("fast subscriber affected; subscribers=%d", bus.Subscribers())
	}
	slow.Close() // idempotent after a drop
}

func TestBus_SubscriberCapCloseAndNilSafety(t *testing.T) {
	bus := NewBus(newClock(), 3, 2)
	a, _ := bus.Subscribe(func(Event) bool { return true }, false, 0)
	if _, err := bus.Subscribe(func(Event) bool { return true }, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.Subscribe(func(Event) bool { return true }, false, 0); err != ErrTooManySubscribers {
		t.Fatalf("cap not enforced: %v", err)
	}
	a.Close()
	a.Close()
	if bus.Subscribers() != 1 {
		t.Fatal("close did not free the slot")
	}
	if _, err := bus.Subscribe(func(Event) bool { return true }, false, 0); err != nil {
		t.Fatal(err)
	}
	var none *Bus
	none.Publish(SandboxState, "", "", nil)
	if none.Subscribers() != 0 {
		t.Fatal("nil bus")
	}
	if len(bus.ring) > 3 {
		t.Fatal("ring exceeded capacity")
	}
}

func TestBus_ConcurrentPublishAndSubscribeIsRaceFree(_ *testing.T) {
	bus := NewBus(newClock(), 50, 100)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				bus.Publish(SandboxState, "k", "s", map[string]int{"j": j})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s, err := bus.Subscribe(func(Event) bool { return true }, j%2 == 0, uint64(j))
				if err == nil {
					drain(s)
					s.Close()
				}
			}
		}()
	}
	wg.Wait()
}

func TestTail_RingKeepsNewestBytesAndReportsDroppedOutput(t *testing.T) {
	clock := newClock()
	tails := NewTails(clock, 10, time.Minute, 3)
	tail := tails.Open("exe_1", "key_a", "sbx_1")
	tail.Write(false, []byte("12345"))
	tail.Write(true, []byte("abcde"))
	tail.Write(false, []byte("XYZ")) // pushes out the first chunk
	sub, err := tail.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := sub.Next(context.Background())
	if err != nil || !batch.Dropped || len(batch.Chunks) != 2 || string(batch.Chunks[0].Data) != "abcde" || !batch.Chunks[0].Stderr || string(batch.Chunks[1].Data) != "XYZ" {
		t.Fatalf("%+v %v", batch, err)
	}
	// An oversized single write keeps only its newest bytes.
	big := tails.Open("exe_2", "key_a", "sbx_1")
	big.Write(false, []byte("0123456789ABCDEF"))
	sub2, _ := big.Subscribe()
	b2, _ := sub2.Next(context.Background())
	if len(b2.Chunks) != 1 || string(b2.Chunks[0].Data) != "6789ABCDEF" {
		t.Fatalf("%+v", b2)
	}
	if tails.Len() != 2 || tail.Owner() != "key_a" || tail.Sandbox() != "sbx_1" {
		t.Fatal("registry")
	}
}

func TestTail_ReadersWaitForLiveOutputThenSeeTheEnd(t *testing.T) {
	tails := NewTails(newClock(), 1024, time.Minute, 3)
	tail := tails.Open("exe_1", "k", "s")
	sub, _ := tail.Subscribe()
	got := make(chan Batch, 4)
	go func() {
		for {
			b, err := sub.Next(context.Background())
			if err != nil {
				return
			}
			got <- b
			if b.End != nil {
				return
			}
		}
	}()
	select {
	case <-got:
		t.Fatal("reader returned before any output")
	case <-time.After(30 * time.Millisecond):
	}
	tail.Write(false, []byte("hello"))
	if b := <-got; len(b.Chunks) != 1 || b.Dropped {
		t.Fatalf("%+v", b)
	}
	tail.Write(true, []byte("oops"))
	if b := <-got; len(b.Chunks) != 1 || string(b.Chunks[0].Data) != "oops" {
		t.Fatalf("%+v", b)
	}
	tails.Finish(tail, End{ExitCode: 3, Known: true})
	end := <-got
	if end.End == nil || end.End.ExitCode != 3 || !end.End.Known {
		t.Fatalf("%+v", end)
	}
	// Readers arriving after the end replay the retained output, then the end.
	late, _ := tail.Subscribe()
	first, _ := late.Next(context.Background())
	second, _ := late.Next(context.Background())
	if len(first.Chunks) != 2 || second.End == nil {
		t.Fatalf("%+v %+v", first, second)
	}
}

func TestTail_ContextCancelCapsRetentionAndRegistryBounds(t *testing.T) {
	clock := newClock()
	tails := NewTails(clock, 100, time.Minute, 1)
	tail := tails.Open("exe_1", "k", "s")
	sub, err := tail.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tail.Subscribe(); err != ErrTooManyTailSubscribers {
		t.Fatalf("reader cap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = sub.Next(ctx); err == nil {
		t.Fatal("cancelled read returned data")
	}
	sub.Release()
	sub.Release()
	if _, err = tail.Subscribe(); err != nil {
		t.Fatal("release did not free the slot", err)
	}
	// Retention: the tail disappears once the clock passes it.
	tails.Finish(tail, End{Known: true})
	clock.advance(30 * time.Second)
	if _, ok := tails.Lookup("exe_1"); !ok {
		t.Fatal("dropped before retention elapsed")
	}
	clock.advance(31 * time.Second)
	if _, ok := tails.Lookup("exe_1"); ok || tails.Len() != 0 {
		t.Fatal("kept past retention")
	}
	// Registry cap: ended tails are evicted for new ones; if everything is running, Open yields nil.
	for i := 0; i < maxTails; i++ {
		if tails.Open(string(rune('a'+i%26))+strings.Repeat("x", i), "k", "s") == nil {
			t.Fatalf("refused at %d", i)
		}
	}
	if tails.Open("overflow", "k", "s") != nil {
		t.Fatal("registry exceeded its cap with all execs running")
	}
	var nilTail *Tail
	nilTail.Write(false, []byte("dropped safely"))
	tails.Finish(nil, End{})
	first, _ := tails.Lookup("a" + "")
	if first == nil {
		t.Fatal("lookup")
	}
	tails.Finish(first, End{})
	if tails.Open("fits-after-eviction", "k", "s") == nil {
		t.Fatal("an ended tail should have been evicted to make room")
	}
	var none *Tails
	if none.Open("x", "k", "s") != nil {
		t.Fatal("nil registry")
	}
	if _, ok := none.Lookup("x"); ok {
		t.Fatal("nil registry lookup")
	}
}

func TestTail_ConcurrentWritersAndReadersAreRaceFree(_ *testing.T) {
	tails := NewTails(newClock(), 64, time.Minute, 8)
	tail := tails.Open("e", "k", "s")
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		sub, _ := tail.Subscribe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sub.Release()
			for {
				b, err := sub.Next(context.Background())
				if err != nil || b.End != nil {
					return
				}
			}
		}()
	}
	for w := 0; w < 3; w++ {
		go func() {
			for i := 0; i < 300; i++ {
				tail.Write(i%2 == 0, []byte("0123456789"))
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	tails.Finish(tail, End{Known: true})
	wg.Wait()
}
