package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store/db"
)

type manualTimer struct {
	ch    chan time.Time
	after time.Duration
}

func (t *manualTimer) Chan() <-chan time.Time { return t.ch }
func (*manualTimer) Stop()                    {}

type manualClock struct {
	mu     sync.Mutex
	at     time.Time
	timers chan *manualTimer
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.at }
func (c *manualClock) NewTimer(after time.Duration) Timer {
	timer := &manualTimer{make(chan time.Time, 1), after}
	c.timers <- timer
	return timer
}

func (c *manualClock) fire(timer *manualTimer) {
	c.mu.Lock()
	c.at = c.at.Add(timer.after)
	at := c.at
	c.mu.Unlock()
	timer.ch <- at
}

// helperFake preserves the runtime contract while supplying ID-specific helper metadata.
type helperFake struct {
	*fake.Driver
	mu                  sync.Mutex
	script              fake.Script
	code                int
	timedOut, cancelled bool
	raw                 []byte
	alter               func(*protocol.Status)
	controlExit         int
	killErr             error
	onKill              func()
	started             chan runtime.ExecRequest
	session             *fake.Session
}

func (d *helperFake) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(request.Cmd) < 4 || request.Cmd[0] != helperPath {
		return nil, runtime.Invalid{Field: "expected helper invocation"}
	}
	script := fake.Script{}
	switch request.Cmd[1] {
	case "run":
		script = d.script
	case "status":
		status := protocol.Status{ID: request.Cmd[3], ExitCode: d.code, TimedOut: d.timedOut, Cancelled: d.cancelled, CleanupComplete: true}
		if d.alter != nil {
			d.alter(&status)
		}
		data, err := json.Marshal(status)
		if err != nil {
			return nil, err
		}
		if d.raw != nil {
			data = d.raw
		}
		script = fake.Script{Stdout: data, Exit: runtime.ExitInfo{ExitCode: d.controlExit}}
	case "kill":
		if d.killErr != nil {
			return nil, d.killErr
		}
		if d.onKill != nil {
			d.onKill()
		}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	session, err := d.Driver.Exec(ctx, id, request)
	if err == nil && request.Cmd[1] == "run" {
		d.session = session.(*fake.Session)
		d.started <- request
	}
	return session, err
}

func execFixture(t *testing.T) (*Service, *helperFake, auth.Principal, auth.Principal, db.Sandbox, *manualClock) {
	t.Helper()
	service, base, p, other, clock := fixture(t)
	manual := &manualClock{at: clock.at, timers: make(chan *manualTimer, 16)}
	driver := &helperFake{Driver: base, started: make(chan runtime.ExecRequest, 16)}
	service.driver = driver
	service.clock = manual
	row, err := service.Create(context.Background(), p, CreateRequest{Template: "base"})
	if err != nil {
		t.Fatal(err)
	}
	return service, driver, p, other, row, manual
}
func argv(values ...string) ExecRequest { return ExecRequest{Cmd: &values} }

func TestExec_RealExitCodesMetadataAndInput(t *testing.T) {
	for _, code := range []int{0, 7, 124, 125, 137, 255} {
		t.Run("exit_"+strconv.Itoa(code), func(t *testing.T) {
			service, driver, p, _, row, _ := execFixture(t)
			driver.code = code
			driver.script = fake.Script{Stdout: []byte("stdout"), Stderr: []byte("stderr"), Exit: runtime.ExitInfo{ExitCode: protocol.TransportExit(code)}}
			request := argv("/usr/bin/printf", "; untrusted argv remains data")
			request.Stdin = []byte("input")
			request.Env = map[string]string{"SECRET": "not-persisted"}
			result, err := service.Exec(context.Background(), p, row.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != code || result.Stdout != "stdout" || result.Stderr != "stderr" || result.Truncated || result.TimedOut {
				t.Fatal("incorrect command result", result.ExitCode)
			}
			recorded, err := service.store.Queries.GetExec(context.Background(), result.ID)
			if err != nil || !recorded.EndedAt.Valid || recorded.ExitCode.Int64 != int64(code) || recorded.StdoutBytes.Int64 != 6 || recorded.StderrBytes.Int64 != 6 || recorded.IsShell != 0 || strings.Contains(recorded.Command, "not-persisted") {
				t.Fatal("incorrect persisted exec", err)
			}
			run := <-driver.started
			if run.Cmd[1] != "run" || run.Cmd[len(run.Cmd)-1] != "; untrusted argv remains data" || run.Cwd != "/workspace" || run.Env["SECRET"] != "not-persisted" || string(driver.session.Input()) != "input" {
				t.Fatal("argv/env/stdin lost or reinterpreted")
			}
			current, err := service.Get(context.Background(), p, row.ID)
			if err != nil || current.State != "running" {
				t.Fatal("ordinary exit destroyed sandbox", err)
			}
		})
	}
}

func TestExec_CombinedCapKeepsDrainingAndSanitizesUTF8(t *testing.T) {
	service, driver, p, _, row, _ := execFixture(t)
	service.cfg.Limits.Exec.MaxOutputBytes = 7
	driver.script = fake.Script{Stdout: []byte(strings.Repeat("é", 20) + "\xff"), Stderr: []byte(strings.Repeat("x", 100))}
	result, err := service.Exec(context.Background(), p, row.ID, argv("program"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || len(result.Stdout)+len(result.Stderr) > 7 || !utf8.ValidString(result.Stdout) || !utf8.ValidString(result.Stderr) {
		t.Fatal("output cap or UTF-8 violated")
	}
	recorded, err := service.store.Queries.GetExec(context.Background(), result.ID)
	if err != nil || recorded.StdoutBytes.Int64 != 41 || recorded.StderrBytes.Int64 != 100 || recorded.Truncated.Int64 != 1 {
		t.Fatal("did not drain all raw output", err)
	}
}

func TestExec_HostDeadlineDestroysAndRecordsUncertainExit(t *testing.T) {
	service, driver, p, _, row, clock := execFixture(t)
	driver.script = fake.Script{OutputGate: make(chan struct{}), ExitGate: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := service.Exec(context.Background(), p, row.ID, argv("stuck")); done <- err }()
	timer := <-clock.timers
	<-driver.started
	if timer.after != 130*time.Second {
		t.Fatal("hard deadline omitted slack", timer.after)
	}
	clock.fire(timer)
	if err := <-done; !errors.Is(err, ErrRuntime) {
		t.Fatal("deadline did not fail closed", err)
	}
	ended, err := service.Get(context.Background(), p, row.ID)
	if err != nil || ended.State != "destroyed" || ended.EndReason.String != "exec_deadline" {
		t.Fatal("deadline did not destroy sandbox", err)
	}
	rows, err := service.store.Queries.ListExecs(context.Background(), db.ListExecsParams{SandboxID: row.ID, PageLimit: 10})
	if err != nil || len(rows) != 1 || rows[0].ExitCode.Valid || rows[0].TimedOut.Int64 != 1 || !rows[0].EndedAt.Valid {
		t.Fatal("claimed a real exit without proof", err)
	}
}

func TestExec_CancellationAndConcurrencyHaveOwnedCleanup(t *testing.T) {
	service, driver, p, _, row, _ := execFixture(t)
	service.cfg.Limits.Exec.MaxConcurrentPerSandbox = 1
	exit := make(chan struct{})
	driver.script = fake.Script{ExitGate: exit, Exit: runtime.ExitInfo{ExitCode: 124}}
	driver.code = 137
	driver.onKill = func() { driver.cancelled = true; close(exit) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := service.Exec(ctx, p, row.ID, argv("waiting")); done <- err }()
	<-driver.started
	if _, err := service.Exec(context.Background(), p, row.ID, argv("second")); !errors.Is(err, ErrExecLimit) {
		t.Fatal("concurrency limit bypassed", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	service.execMu.Lock()
	remaining := len(service.activeExecs)
	service.execMu.Unlock()
	if remaining != 0 {
		t.Fatal("concurrency reservation leaked")
	}
	current, err := service.Get(context.Background(), p, row.ID)
	if err != nil || current.State != "running" {
		t.Fatal("proven cancellation destroyed sandbox", err)
	}
	rows, err := service.store.Queries.ListExecs(context.Background(), db.ListExecsParams{SandboxID: row.ID, PageLimit: 10})
	if err != nil || len(rows) != 1 || rows[0].Cancelled.Int64 != 1 || !rows[0].EndedAt.Valid {
		t.Fatal("cancelled exec not recorded", err)
	}
}

func TestExec_UnconfirmedCancellationHasTenSecondDeadline(t *testing.T) {
	service, driver, p, _, row, clock := execFixture(t)
	driver.script = fake.Script{ExitGate: make(chan struct{})}
	driver.killErr = errors.New("controller unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := service.Exec(ctx, p, row.ID, argv("waiting")); done <- err }()
	<-driver.started
	<-clock.timers
	cancel()
	deadline := <-clock.timers
	if deadline.after != 10*time.Second {
		t.Fatal("cancellation is not bounded")
	}
	clock.fire(deadline)
	if err := <-done; !errors.Is(err, ErrRuntime) {
		t.Fatal(err)
	}
	current, err := service.Get(context.Background(), p, row.ID)
	if err != nil || current.State != "destroyed" || current.EndReason.String != "exec_deadline" {
		t.Fatal("unconfirmed cancellation survived", err)
	}
}

func TestExec_UntrustedCompletionDestroysSandbox(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*helperFake)
	}{
		{"helper_fault", func(d *helperFake) { d.script.Exit.ExitCode = 125 }},
		{"controller_killed", func(d *helperFake) { d.script.Exit.ExitCode = 137 }},
		{"driver_fault", func(d *helperFake) { d.script.WaitError = io.ErrUnexpectedEOF }},
		{"start_fault", func(d *helperFake) { d.script.ExecError = io.ErrClosedPipe }},
		{"wrong_id", func(d *helperFake) { d.alter = func(s *protocol.Status) { s.ID = "exe_other" } }},
		{"missing_cleanup", func(d *helperFake) { d.alter = func(s *protocol.Status) { s.CleanupComplete = false } }},
		{"wrong_exit_mapping", func(d *helperFake) { d.code = 7 }},
		{"bad_json", func(d *helperFake) { d.raw = []byte("invalid") }},
		{"large_status", func(d *helperFake) { d.raw = []byte(strings.Repeat("x", 4097)) }},
		{"status_fault", func(d *helperFake) { d.controlExit = 125 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, driver, p, _, row, _ := execFixture(t)
			test.setup(driver)
			_, err := service.Exec(context.Background(), p, row.ID, argv("command"))
			if !errors.Is(err, ErrRuntime) {
				t.Fatal(err)
			}
			current, err := service.Get(context.Background(), p, row.ID)
			if err != nil || current.State != "destroyed" || current.EndReason.String != "exec_uncertain" {
				t.Fatal("uncertain exec left sandbox running", err)
			}
		})
	}
}

func TestExec_RejectsInvalidRequestsAndForeignResources(t *testing.T) {
	service, _, p, other, row, _ := execFixture(t)
	shell := "echo user"
	cmd := []string{"argv"}
	empty := []string{}
	zero := int64(0)
	high := int64(3601)
	badPath := "/etc"
	relative := "relative"
	requests := []ExecRequest{{}, {Cmd: &cmd, Shell: &shell}, {Cmd: &empty}, {Shell: new(string)}, {Cmd: &cmd, TimeoutSeconds: &zero}, {Cmd: &cmd, TimeoutSeconds: &high}, {Cmd: &cmd, Cwd: &badPath}, {Cmd: &cmd, Cwd: &relative}, {Cmd: &cmd, Stdin: make([]byte, 65537)}, {Cmd: &cmd, Env: map[string]string{"BAD=KEY": "value"}}}
	for i, request := range requests {
		if _, err := service.Exec(context.Background(), p, row.ID, request); err == nil {
			t.Fatalf("accepted invalid request %d", i)
		}
	}
	if _, err := service.Exec(context.Background(), other, row.ID, argv("command")); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong owner leaked", err)
	}
	if err := service.Destroy(context.Background(), p, row.ID, "requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Exec(context.Background(), p, row.ID, argv("command")); !errors.Is(err, ErrNotRunning) {
		t.Fatal("ended sandbox accepted exec", err)
	}
	plan, err := resolveExec(service.cfg, ExecRequest{Shell: &shell})
	if err != nil || len(plan.argv) != 3 || plan.argv[2] != shell || !plan.shell {
		t.Fatal("explicit shell changed", err)
	}
}
