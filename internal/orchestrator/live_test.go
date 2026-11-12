package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

func collect(sub *events.Subscription) []events.Event {
	var out []events.Event
	for {
		select {
		case e, ok := <-sub.Live():
			if !ok {
				return out
			}
			out = append(out, e)
		default:
			return out
		}
	}
}

func types(list []events.Event) []string {
	var out []string
	for _, e := range list {
		out = append(out, e.Type)
	}
	return out
}

func TestEvents_LifecycleStatesAndEndReasonsArePublishedToTheOwnerOnly(t *testing.T) {
	s, _, p, other, _, _ := execFixture(t)
	bus := events.NewBus(s.clock, 100, 10)
	s.WithEvents(bus)
	ownerSub, _ := bus.Subscribe(events.ForPrincipal(p.ID, false), false, 0)
	otherSub, _ := bus.Subscribe(events.ForPrincipal(other.ID, false), false, 0)
	adminSub, _ := bus.Subscribe(events.ForPrincipal("admin", true), false, 0)
	row, err := s.Create(context.Background(), p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Destroy(context.Background(), p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	got := collect(ownerSub)
	want := []string{events.SandboxState, events.SandboxCreated, events.SandboxState, events.SandboxState, events.SandboxEnded}
	if strings.Join(types(got), ",") != strings.Join(want, ",") {
		t.Fatalf("%v", types(got))
	}
	last := got[len(got)-1]
	var data map[string]any
	if err = json.Unmarshal(last.Data, &data); err != nil || data["reason"] != "requested" || data["state"] != "destroyed" || last.SandboxID != row.ID {
		t.Fatalf("%v %v", data, err)
	}
	if len(collect(otherSub)) != 0 {
		t.Fatal("another key saw this sandbox's events")
	}
	if len(collect(adminSub)) != len(got) {
		t.Fatal("admin must see every event")
	}
}

func TestEvents_ExecMetadataAndOutputTailNeverTouchDisk(t *testing.T) {
	s, d, p, other, row, _ := execFixture(t)
	bus := events.NewBus(s.clock, 100, 10)
	s.WithEvents(bus)
	sub, _ := bus.Subscribe(events.ForPrincipal(p.ID, false), false, 0)
	d.script = fake.Script{Stdout: []byte("out-bytes"), Stderr: []byte("err-bytes")}
	result, err := s.Exec(context.Background(), p, row.ID, argv("program", "SECRET-ARG"))
	if err != nil {
		t.Fatal(err)
	}
	got := collect(sub)
	if len(got) != 2 || got[0].Type != events.ExecStarted || got[1].Type != events.ExecEnded {
		t.Fatalf("%v", types(got))
	}
	for _, e := range got {
		if strings.Contains(string(e.Data), "out-bytes") || strings.Contains(string(e.Data), "SECRET-ARG") {
			t.Fatalf("event carries output or arguments: %s", e.Data)
		}
	}
	var ended map[string]any
	_ = json.Unmarshal(got[1].Data, &ended)
	if ended["exec_id"] != result.ID || ended["exit_code"] != float64(0) || ended["result"] != "ok" {
		t.Fatalf("%v", ended)
	}
	tail, rec, err := s.ExecOutput(context.Background(), p, row.ID, result.ID)
	if err != nil || tail == nil || rec.ID != result.ID {
		t.Fatalf("%v %v", tail, err)
	}
	reader, _ := tail.Subscribe()
	first, _ := reader.Next(context.Background())
	var out, errText string
	for _, c := range first.Chunks {
		if c.Stderr {
			errText += string(c.Data)
		} else {
			out += string(c.Data)
		}
	}
	end, _ := reader.Next(context.Background())
	if out != "out-bytes" || errText != "err-bytes" || end.End == nil || !end.End.Known || end.End.ExitCode != 0 {
		t.Fatalf("%q %q %+v", out, errText, end)
	}
	// Foreign callers cannot reach the tail, and a mismatched sandbox/exec pair is not found.
	if _, _, err = s.ExecOutput(context.Background(), other, row.ID, result.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	second, _ := s.Create(context.Background(), p, CreateRequest{Template: "base"})
	if _, _, err = s.ExecOutput(context.Background(), p, second.ID, result.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("exec of another sandbox served", err)
	}
	if _, _, err = s.ExecOutput(context.Background(), p, row.ID, "exe_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestEvents_FileTransfersAndDisabledDashboardPublishNothing(t *testing.T) {
	s, d, p, _, row, _ := execFixture(t)
	bus := events.NewBus(s.clock, 100, 10)
	s.WithEvents(bus)
	sub, _ := bus.Subscribe(func(events.Event) bool { return true }, false, 0)
	d.script = fake.Script{Stdout: []byte("{\"size_bytes\":3}\nabc")}
	var sink strings.Builder
	if err := s.ReadFile(context.Background(), p, row.ID, "/workspace/a", &sink, nil); err != nil {
		t.Fatal(err)
	}
	if got := collect(sub); len(got) != 0 {
		t.Fatalf("file contents reached the live views: %v", types(got))
	}
	// Without WithEvents the service behaves exactly as before.
	plain, pd, pp, _, prow, _ := execFixture(t)
	pd.script = fake.Script{Stdout: []byte("x")}
	if _, err := plain.Exec(context.Background(), pp, prow.ID, argv("p")); err != nil {
		t.Fatal(err)
	}
	if tail, _, err := plain.ExecOutput(context.Background(), pp, prow.ID, "exe_none"); !errors.Is(err, ErrNotFound) || tail != nil {
		t.Fatal(err)
	}
}

func TestEvents_ExpiredOutputIsReportedAsGoneNotAsAnError(t *testing.T) {
	s, d, p, _, row, clock := execFixture(t)
	s.WithEvents(events.NewBus(s.clock, 10, 10))
	d.script = fake.Script{Stdout: []byte("x")}
	result, err := s.Exec(context.Background(), p, row.ID, argv("p"))
	if err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.at = clock.at.Add(time.Duration(s.cfg.Limits.Exec.TailRetentionSeconds+1) * time.Second)
	clock.mu.Unlock()
	tail, rec, err := s.ExecOutput(context.Background(), p, row.ID, result.ID)
	if err != nil || tail != nil || rec.ID != result.ID {
		t.Fatalf("an expired tail must be nil with the exec record still found: %v %v", tail, err)
	}
}

func TestEvents_PoolLevelChangesAreAdminOnly(t *testing.T) {
	s, _, p, _, _, _ := execFixture(t)
	bus := events.NewBus(s.clock, 50, 10)
	s.WithEvents(bus)
	s.cfg.Templates[0].WarmPool = 1
	admin, _ := bus.Subscribe(events.ForPrincipal("admin", true), false, 0)
	user, _ := bus.Subscribe(events.ForPrincipal(p.ID, false), false, 0)
	if err := s.FillPool(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := collect(admin)
	if len(got) != 1 || got[0].Type != events.PoolChanged {
		t.Fatalf("%v", types(got))
	}
	var data map[string]any
	_ = json.Unmarshal(got[0].Data, &data)
	if data["template"] != "base" || data["available"] != float64(1) {
		t.Fatalf("%v", data)
	}
	if len(collect(user)) != 0 {
		t.Fatal("pool events leaked to a non-admin key")
	}
}

func TestListExecs_NewestFirstPaginationAndOwnership(t *testing.T) {
	s, d, p, other, row, clock := execFixture(t)
	d.script = fake.Script{}
	var ids []string
	for i := 0; i < 5; i++ {
		clock.mu.Lock()
		clock.at = clock.at.Add(time.Second)
		clock.mu.Unlock()
		result, err := s.Exec(context.Background(), p, row.ID, argv("p"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.ID)
	}
	page, err := s.ListExecs(context.Background(), p, row.ID, 2, "")
	if err != nil || len(page) != 2 || page[0].ID != ids[4] || page[1].ID != ids[3] {
		t.Fatalf("%v %v", page, err)
	}
	next, err := s.ListExecs(context.Background(), p, row.ID, 10, page[1].ID)
	if err != nil || len(next) != 3 || next[0].ID != ids[2] || next[2].ID != ids[0] {
		t.Fatalf("%v %v", next, err)
	}
	if _, err = s.ListExecs(context.Background(), other, row.ID, 5, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, bad := range []int64{0, 1001} {
		if _, err = s.ListExecs(context.Background(), p, row.ID, bad, ""); !errors.Is(err, ErrInvalid) {
			t.Fatal("unbounded page accepted", bad)
		}
	}
}

func TestOverview_AdminSeesGlobalNumbersOthersOnlyTheirOwn(t *testing.T) {
	s, _, p, other, _, _ := execFixture(t) // p already owns one running sandbox
	s.WithEvents(events.NewBus(s.clock, 10, 10))
	ctx := context.Background()
	second, err := s.Create(ctx, other, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Destroy(ctx, other, second.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, other, CreateRequest{Template: "base"}); err != nil {
		t.Fatal(err)
	}
	s.cfg.Templates[0].WarmPool = 2
	admin, err := s.Overview(ctx, auth.Principal{ID: "admin", Admin: true})
	if err != nil || len(admin.ActiveByTemplate) != 1 || admin.ActiveByTemplate[0].Count != 2 || len(admin.Recent) != 1 || len(admin.Pool) != 1 || admin.Pool[0].Target != 2 || admin.Runtime == "" {
		t.Fatalf("%+v %v", admin, err)
	}
	mine, err := s.Overview(ctx, p)
	if err != nil || mine.ActiveByTemplate[0].Count != 1 || len(mine.Recent) != 0 || len(mine.Pool) != 0 {
		t.Fatalf("%+v %v", mine, err)
	}
	theirs, _ := s.Overview(ctx, other)
	if theirs.ActiveByTemplate[0].Count != 1 || len(theirs.Recent) != 1 || theirs.Recent[0].EndReason.String != "requested" {
		t.Fatalf("%+v", theirs)
	}
}

type memAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (m *memAudit) Append(_ context.Context, e audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}
func (*memAudit) Prune(context.Context, time.Time) error { return nil }

// A shell string is stored raw in the execs table; the audit event must still be valid metadata.
func TestAudit_ShellExecStartIsRecordedNotSilentlyDropped(t *testing.T) {
	s, d, p, _, row, _ := execFixture(t)
	backend := &memAudit{}
	writer, err := audit.New(backend, audit.SystemClock{}, 30, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.audit = writer
	d.script = fake.Script{}
	shell := "echo \"quoted\" && exit 0"
	if _, err = s.Exec(context.Background(), p, row.ID, ExecRequest{Shell: &shell}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Exec(context.Background(), p, row.ID, argv("ls", "-l")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := writer.Stats(); stats.Dropped != 0 {
		t.Fatalf("audit events were dropped: %+v", stats)
	}
	var started []map[string]any
	for _, e := range backend.events {
		if e.Action == "exec.started" {
			var details map[string]any
			if err = json.Unmarshal(e.Details, &details); err != nil {
				t.Fatal(err)
			}
			started = append(started, details)
		}
	}
	if len(started) != 2 || started[0]["command"] != shell || started[0]["is_shell"] != true {
		t.Fatalf("shell exec not audited: %v", started)
	}
	if args, ok := started[1]["command"].([]any); !ok || len(args) != 2 || args[0] != "ls" {
		t.Fatalf("argv exec: %v", started[1])
	}
}
