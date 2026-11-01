package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/arcana/internal/runtime"
)

func create(t *testing.T, d *Driver) runtime.Instance {
	t.Helper()
	instance, err := d.Create(context.Background(), runtime.Spec{SandboxID: "sbx_test", Image: "test", Env: map[string]string{"A": "value"}, Labels: map[string]string{"test": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Destroy(context.Background(), instance.ID); err != nil {
			t.Error(err)
		}
	})
	return instance
}

func TestDriver_CreateListAndDestroyAreIndependent(t *testing.T) {
	d := New()
	ctx := context.Background()
	instance := create(t, d)
	instance.Labels["test"] = "changed"
	spec, ok := d.Spec(instance.ID)
	if !ok || spec.Labels["test"] != "one" {
		t.Fatal("returned mutable instance alias")
	}
	spec.Env["A"] = "changed"
	spec.Labels["test"] = "changed"
	original, _ := d.Spec(instance.ID)
	if original.Env["A"] != "value" || original.Labels["test"] != "one" {
		t.Fatal("returned mutable spec alias")
	}
	listed, err := d.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatal("missing instance")
	}
	listed[0].Labels["test"] = "changed"
	listed, err = d.List(ctx)
	if err != nil || listed[0].Labels["test"] != "one" {
		t.Fatal("list aliases internal state")
	}
	if _, err = d.Stats(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	var missing runtime.NotFound
	if _, err = d.Stats(ctx, instance.ID); !errors.As(err, &missing) {
		t.Fatal("missing instance accepted")
	}
	if _, err = d.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"true"}}); !errors.As(err, &missing) {
		t.Fatal("exec on missing instance accepted")
	}
}

func TestSession_ScriptedStreamsInputExitAndTTY(t *testing.T) {
	d := New()
	instance := create(t, d)
	ctx := context.Background()
	script := Script{Stdout: []byte("out"), Stderr: []byte("err"), Exit: runtime.ExitInfo{ExitCode: 7}}
	if err := d.QueueExec(script); err != nil {
		t.Fatal(err)
	}
	script.Stdout[0] = 'X'
	execution, err := d.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"test"}, Env: map[string]string{"A": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(execution.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(execution.Stderr())
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "out" || string(stderr) != "err" {
		t.Fatalf("outputs: %q %q", stdout, stderr)
	}
	if _, err = io.WriteString(execution.Stdin(), "input"); err != nil {
		t.Fatal(err)
	}
	if err = execution.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(execution.Stdin(), "late"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("stdin close ignored")
	}
	session := execution.(*Session)
	if string(session.Input()) != "input" {
		t.Fatal("stdin not recorded")
	}
	request := session.Request()
	request.Cmd[0] = "mutated"
	if session.Request().Cmd[0] != "test" {
		t.Fatal("request alias")
	}
	result, err := execution.Wait(ctx)
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("exit: %+v %v", result, err)
	}
	if err = execution.Resize(80, 24); err == nil {
		t.Fatal("non-TTY resize accepted")
	}
	if err = d.QueueExec(Script{Stdout: []byte("one"), Stderr: []byte("two")}); err != nil {
		t.Fatal(err)
	}
	execution, err = d.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"test"}, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	if execution.Stderr() != nil {
		t.Fatal("TTY has separate stderr")
	}
	stdout, err = io.ReadAll(execution.Stdout())
	if err != nil || string(stdout) != "onetwo" {
		t.Fatal("TTY output not merged")
	}
	if err = execution.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	cols, rows := execution.(*Session).Size()
	if cols != 120 || rows != 40 {
		t.Fatal("resize not recorded")
	}
	if err = execution.Resize(0, 40); err == nil {
		t.Fatal("zero terminal size accepted")
	}
}

func TestSession_DelaysUnblockOnCancellationAndDestroy(t *testing.T) {
	for _, destroy := range []bool{false, true} {
		t.Run(fmt.Sprint(destroy), func(t *testing.T) {
			d := New()
			instance := create(t, d)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			gate := make(chan struct{})
			if err := d.QueueExec(Script{Stdout: []byte("later"), OutputGate: gate, ExitGate: gate}); err != nil {
				t.Fatal(err)
			}
			session, err := d.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"wait"}})
			if err != nil {
				t.Fatal(err)
			}
			if destroy {
				if err = d.Destroy(context.Background(), instance.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			if _, err = io.ReadAll(session.Stdout()); !errors.Is(err, context.Canceled) {
				t.Fatal("read ignored cancellation")
			}
			if _, err = session.Wait(context.Background()); !errors.Is(err, context.Canceled) {
				t.Fatal("wait ignored cancellation")
			}
		})
	}
	d := New()
	instance := create(t, d)
	gate := make(chan struct{})
	if err := d.QueueExec(Script{ExitGate: gate}); err != nil {
		t.Fatal(err)
	}
	session, err := d.Exec(context.Background(), instance.ID, runtime.ExecRequest{Cmd: []string{"wait"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = session.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("Wait ignored its caller context")
	}
	close(gate)
	if _, err = session.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDriver_FailuresAndLimits(t *testing.T) {
	d := New()
	ctx := context.Background()
	instance := create(t, d)
	fault := errors.New("injected")
	d.SetFailures(Failures{Create: fault, Destroy: fault, Stats: fault, List: fault})
	if _, err := d.Create(ctx, runtime.Spec{}); !errors.Is(err, fault) {
		t.Fatal("create failure ignored")
	}
	if err := d.Destroy(ctx, instance.ID); !errors.Is(err, fault) {
		t.Fatal("destroy failure ignored")
	}
	if _, err := d.Stats(ctx, instance.ID); !errors.Is(err, fault) {
		t.Fatal("stats failure ignored")
	}
	if _, err := d.List(ctx); !errors.Is(err, fault) {
		t.Fatal("list failure ignored")
	}
	d.SetFailures(Failures{})
	for _, script := range []Script{{ExecError: fault}, {WaitError: fault}, {ResizeError: fault}} {
		if err := d.QueueExec(script); err != nil {
			t.Fatal(err)
		}
		session, err := d.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"test"}, TTY: true})
		if script.ExecError != nil {
			if !errors.Is(err, fault) {
				t.Fatal("exec failure ignored")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if script.WaitError != nil {
			if _, err = session.Wait(ctx); !errors.Is(err, fault) {
				t.Fatal("wait failure ignored")
			}
		}
		if script.ResizeError != nil {
			if err = session.Resize(80, 24); !errors.Is(err, fault) {
				t.Fatal("resize failure ignored")
			}
		}
	}
	if _, err := d.Exec(ctx, instance.ID, runtime.ExecRequest{}); err == nil {
		t.Fatal("empty command accepted")
	}
	if _, err := d.Create(ctx, runtime.Spec{}); err == nil {
		t.Fatal("empty spec accepted")
	}
	for range maxScripts {
		if err := d.QueueExec(Script{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.QueueExec(Script{}); err == nil {
		t.Fatal("unbounded script queue")
	}
}

func TestDriver_ConcurrentCreationAndCancellation(t *testing.T) {
	d := New()
	var group sync.WaitGroup
	for i := range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := d.Create(context.Background(), runtime.Spec{SandboxID: fmt.Sprint(i), Image: "test"})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	listed, err := d.List(context.Background())
	if err != nil || len(listed) != 20 {
		t.Fatal("concurrent instances lost")
	}
	for _, instance := range listed {
		if err := d.Destroy(context.Background(), instance.ID); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = d.Create(ctx, runtime.Spec{Image: "test", SandboxID: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatal("create ignored cancellation")
	}
	if _, err = d.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("list ignored cancellation")
	}
	if !strings.Contains(d.String(), "0 instances") || d.Name() != "fake" || !d.Capabilities().SupportsTTY {
		t.Fatal("bad driver metadata")
	}
}

func TestDriver_RejectsExcessOutputInputAndInstances(t *testing.T) {
	d := New()
	instance := create(t, d)
	if err := d.QueueExec(Script{Stdout: make([]byte, maxScriptOutput+1)}); err == nil {
		t.Fatal("oversized script accepted")
	}
	session, err := d.Exec(context.Background(), instance.ID, runtime.ExecRequest{Cmd: []string{"input"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Stdin().Write(make([]byte, maxRecordedInput+1)); err == nil {
		t.Fatal("unbounded input recording")
	}
	if len(session.(*Session).Input()) != 0 {
		t.Fatal("failed write partially recorded")
	}
	for i := range maxInstances - 1 {
		if _, err = d.Create(context.Background(), runtime.Spec{SandboxID: fmt.Sprint(i), Image: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = d.Create(context.Background(), runtime.Spec{SandboxID: "excess", Image: "test"}); err == nil {
		t.Fatal("unbounded instances")
	}
	instances, err := d.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range instances {
		if err = d.Destroy(context.Background(), instance.ID); err != nil {
			t.Fatal(err)
		}
	}
}
