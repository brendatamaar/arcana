package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/brendatamaar/arcana/pkg/client"
)

// fakeArcana models sandboxes as maps of files. A fixture counts as fixed once sumrange.py contains
// the correct loop bound, and hidden tests pass only then.
type fakeArcana struct {
	mu        sync.Mutex
	next      int
	files     map[string]map[string]string
	created   []client.CreateSandboxRequest
	destroyed []string
	uploads   []string // "<id>:<names>"
	commands  []string
	snapshots []string
	deleted   []string
	forkItems int
	forkFail  error
	uploadErr error
	hidden    []string // hidden file names seen in the last upload per sandbox
}

func newFake() *fakeArcana { return &fakeArcana{files: map[string]map[string]string{}} }

func (f *fakeArcana) sandbox() string {
	f.next++
	id := fmt.Sprintf("sbx_%d", f.next)
	f.files[id] = map[string]string{}
	return id
}

func (f *fakeArcana) CreateSandbox(_ context.Context, in client.CreateSandboxRequest) (client.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	return client.Sandbox{Id: f.sandbox(), State: "running"}, nil
}

func (f *fakeArcana) DestroySandbox(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeArcana) Exec(_ context.Context, id string, in client.ExecRequest) (client.ExecResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := *in.Shell
	f.commands = append(f.commands, id+": "+command)
	code := 1
	fixed := strings.Contains(f.files[id]["/workspace/sumrange.py"], "end + 1")
	if (strings.Contains(command, "unittest") && fixed) || strings.HasPrefix(command, "rm -rf") {
		code = 0
	}
	return client.ExecResult{ExitCode: code, Stdout: "ran " + command}, nil
}

func (f *fakeArcana) ReadFile(_ context.Context, id, path string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return io.NopCloser(strings.NewReader(f.files[id][path])), nil
}

func (f *fakeArcana) WriteFile(_ context.Context, id, path string, in io.Reader) error {
	data, _ := io.ReadAll(in)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[id][path] = string(data)
	return nil
}

func (f *fakeArcana) ListFiles(context.Context, string, string, int64) (client.FileList, error) {
	return client.FileList{}, nil
}

func (f *fakeArcana) UploadArchive(_ context.Context, id, _ string, in io.Reader) error {
	data, _ := io.ReadAll(in)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return f.uploadErr
	}
	names := untar(data)
	f.uploads = append(f.uploads, id+":"+strings.Join(names, ","))
	return nil
}

func untar(data []byte) []string {
	files, _ := parseArchive(data)
	var names []string
	for name := range files {
		names = append(names, name)
	}
	return names
}

func (f *fakeArcana) CreateSnapshot(_ context.Context, id string) (client.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, id)
	return client.Snapshot{Id: "snp_1"}, nil
}

func (f *fakeArcana) DeleteSnapshot(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeArcana) Fork(_ context.Context, _ string, in client.ForkRequest) (client.ForkResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forkFail != nil {
		return client.ForkResult{}, f.forkFail
	}
	var result client.ForkResult
	for i := 0; i < f.forkItems; i++ {
		result.Items = append(result.Items, client.Sandbox{Id: f.sandbox()})
	}
	for i := f.forkItems; i < int(in.Count); i++ {
		result.Failures = append(result.Failures, client.ForkFailure{Index: int64(i), Code: "quota_exceeded", Message: "sandbox quota exceeded"})
	}
	return result, nil
}

func offByOne(t *testing.T) Fixture {
	t.Helper()
	f, err := loadFixture("../fixtures/py-off-by-one")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const fixSource = "def sum_range(start, end):\n    return sum(range(start, end + 1))\n"

func writeFix(id string) ToolCall {
	return call("w"+id, "write_file", map[string]any{"path": "/workspace/sumrange.py", "content": fixSource})
}

func TestRunTask_SingleAttemptJudgesWithHiddenTestsThenRemovesThem(t *testing.T) {
	api := newFake()
	fx := offByOne(t)
	llm := &scriptedLLM{replies: []Completion{turn(10, call("r", "read_file", map[string]any{"path": "/workspace/README.md"})), turn(10, writeFix("1"))}}
	result, err := RunTask(context.Background(), Deps{Arcana: api, LLM: llm}, fx, TaskOptions{Parallel: 1, Budget: Budget{MaxSteps: 5, MaxTokens: 1000}, Temperature: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Solved || result.Winner != 0 || result.Steps != 2 || result.Tokens != 20 || result.Reason != "solved" {
		t.Fatalf("%+v", result)
	}
	if got := api.created[0]; got.Template != "python" || (*got.Labels)["fix-agent"] != "py-off-by-one" {
		t.Fatalf("%+v", got)
	}
	// The model's first prompt carries the visible test command and no hint about the answer.
	prompt := llm.calls[0][1].Content
	if !strings.Contains(prompt, fx.VisibleTest) || strings.Contains(strings.ToLower(prompt), "hidden") || strings.Contains(prompt, "end + 1") {
		t.Fatalf("prompt: %s", prompt)
	}
	// The project upload has no hidden tests; they appear only in the judge's upload, and are removed.
	if strings.Contains(api.uploads[0], "test_hidden") || !strings.Contains(api.uploads[0], "sumrange.py") {
		t.Fatalf("project upload: %s", api.uploads[0])
	}
	if len(api.uploads) != 2 || !strings.Contains(api.uploads[1], "test_hidden.py") {
		t.Fatalf("uploads: %v", api.uploads)
	}
	var sawCleanup bool
	for i, command := range api.commands {
		if strings.Contains(command, "rm -rf -- '/workspace/tests/test_hidden.py'") {
			sawCleanup = true
			if i == 0 || !strings.Contains(api.commands[i-1], fx.HiddenTest) {
				t.Fatal("cleanup must directly follow the hidden run")
			}
		}
	}
	if !sawCleanup {
		t.Fatalf("hidden tests were not removed: %v", api.commands)
	}
	if len(api.destroyed) != 1 || api.destroyed[0] != "sbx_1" {
		t.Fatalf("destroyed: %v", api.destroyed)
	}
}

func TestRunTask_ParallelForksFromSnapshotKeepsFirstSolverAndCleansUp(t *testing.T) {
	api := newFake()
	api.forkItems = 3 // 4 requested, one fork failed
	fx := offByOne(t)
	var once sync.Once
	llm := &funcLLM{fn: func(ctx context.Context, _ []Message, temperature float64) (Completion, error) {
		if temperature > 0.39 && temperature < 0.41 { // the second attempt solves immediately
			var reply Completion
			once.Do(func() { reply = turn(7, writeFix("w")) })
			if len(reply.Message.ToolCalls) > 0 {
				return reply, nil
			}
		}
		<-ctx.Done() // the others never finish on their own: the winner must cancel them
		return Completion{}, ctx.Err()
	}}
	result, err := RunTask(context.Background(), Deps{Arcana: api, LLM: llm}, fx, TaskOptions{Parallel: 4, Budget: Budget{MaxSteps: 5, MaxTokens: 1000}, Temperature: 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Solved || result.Winner != 1 || len(result.Attempts) != 3 {
		t.Fatalf("%+v", result)
	}
	for i, a := range result.Attempts {
		if i != 1 && (a.Solved || a.Reason != "cancelled") {
			t.Fatalf("attempt %d: %+v", i, a.Outcome)
		}
	}
	if len(api.snapshots) != 1 || api.snapshots[0] != "sbx_1" {
		t.Fatalf("snapshot of the prepared sandbox: %v", api.snapshots)
	}
	// Prepared sandbox freed before forking, forks and snapshot removed afterwards.
	want := map[string]bool{"sbx_1": true, "sbx_2": true, "sbx_3": true, "sbx_4": true}
	for _, id := range api.destroyed {
		delete(want, id)
	}
	if len(want) != 0 || len(api.deleted) != 1 || api.deleted[0] != "snp_1" {
		t.Fatalf("leaked: sandboxes=%v destroyed=%v deleted=%v", want, api.destroyed, api.deleted)
	}
}

type funcLLM struct {
	fn func(context.Context, []Message, float64) (Completion, error)
}

func (l *funcLLM) Chat(ctx context.Context, messages []Message, _ []ToolSpec, temperature float64) (Completion, error) {
	return l.fn(ctx, messages, temperature)
}

func TestRunTask_CleansUpWhenSetupFails(t *testing.T) {
	fx := offByOne(t)
	opts := TaskOptions{Parallel: 1, Budget: Budget{MaxSteps: 2, MaxTokens: 100}}
	api := newFake()
	api.uploadErr = errors.New("upload refused")
	if _, err := RunTask(context.Background(), Deps{Arcana: api, LLM: &scriptedLLM{}}, fx, opts); err == nil || len(api.destroyed) != 1 {
		t.Fatalf("upload failure: %v destroyed=%v", err, api.destroyed)
	}
	api = newFake()
	api.forkFail = errors.New("fork refused")
	opts.Parallel = 3
	if _, err := RunTask(context.Background(), Deps{Arcana: api, LLM: &scriptedLLM{}}, fx, opts); err == nil || len(api.deleted) != 1 {
		t.Fatalf("fork failure must still delete the snapshot: %v deleted=%v", err, api.deleted)
	}
}

func TestRunTask_UnsolvedReportsTheReasonAndTotalCost(t *testing.T) {
	api := newFake()
	llm := &scriptedLLM{replies: []Completion{turn(100, call("l", "list_files", map[string]any{"path": "/workspace"})), turn(100, call("l2", "list_files", map[string]any{"path": "/workspace"}))}}
	result, err := RunTask(context.Background(), Deps{Arcana: api, LLM: llm}, offByOne(t), TaskOptions{Parallel: 1, Budget: Budget{MaxSteps: 2, MaxTokens: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Solved || result.Winner != -1 || result.Reason != "step budget" || result.Tokens != 200 || result.Steps != 2 {
		t.Fatalf("%+v", result)
	}
}

func TestHiddenJudge_CleansUpEvenWhenTheRunFailsAndJudgesAdHocProjectsByTheVisibleTest(t *testing.T) {
	api := newFake()
	id := api.sandbox()
	fx := offByOne(t)
	judge, err := newHiddenJudge(api, id, fx)
	if err != nil {
		t.Fatal(err)
	}
	solved, err := judge.Solved(context.Background())
	if err != nil || solved {
		t.Fatalf("%v %v", solved, err)
	}
	if !strings.HasPrefix(api.commands[len(api.commands)-1], id+": rm -rf") {
		t.Fatalf("no cleanup: %v", api.commands)
	}
	adHoc := Fixture{Name: "x", Template: "python", VisibleTest: "python -m unittest"}
	judge, err = newHiddenJudge(api, id, adHoc)
	if err != nil {
		t.Fatal(err)
	}
	before := len(api.uploads)
	if _, err = judge.Solved(context.Background()); err != nil || len(api.uploads) != before {
		t.Fatal("an ad-hoc project must not upload hidden tests", err)
	}
}

func TestQuoteAll_NeutralisesShellMetacharacters(t *testing.T) {
	got := strings.Join(quoteAll([]string{"/workspace/a b", "/workspace/it's;rm -rf /"}), " ")
	if got != `'/workspace/a b' '/workspace/it'\''s;rm -rf /'` {
		t.Fatal(got)
	}
}

func TestVerifyFixture_RequiresBothSuitesToFailThenPass(t *testing.T) {
	fx := offByOne(t)
	// The fake treats the solution upload as fixing the file by writing it directly afterwards.
	api := newFake()
	result := Deps{Arcana: api}.VerifyFixture(context.Background(), fx)
	if result.Err != nil || !result.VisibleFailsBefore || !result.HiddenFailsBefore || result.VisiblePassesAfter || result.OK() {
		t.Fatalf("a fixture whose solution does not fix it must not verify: %+v", result)
	}
	if len(api.destroyed) != 1 {
		t.Fatal("verification sandbox leaked")
	}
	ok := VerifyResult{VisibleFailsBefore: true, HiddenFailsBefore: true, VisiblePassesAfter: true, HiddenPassesAfter: true}
	if !ok.OK() {
		t.Fatal("complete result rejected")
	}
	for i := 0; i < 4; i++ {
		broken := ok
		switch i {
		case 0:
			broken.VisibleFailsBefore = false
		case 1:
			broken.HiddenFailsBefore = false
		case 2:
			broken.VisiblePassesAfter = false
		case 3:
			broken.Err = errors.New("x")
		}
		if broken.OK() {
			t.Fatalf("case %d accepted", i)
		}
	}
}
