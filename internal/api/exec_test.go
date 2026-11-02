package api

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/brendatamaar/arcana/internal/api/gen"
	"github.com/brendatamaar/arcana/internal/helper/protocol"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

type apiExecDriver struct {
	*fake.Driver
	mu sync.Mutex
}

func (d *apiExecDriver) Exec(ctx context.Context, id string, request runtime.ExecRequest) (runtime.ExecSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	script := fake.Script{Stdout: []byte("api-output"), Stderr: []byte("api-error"), Exit: runtime.ExitInfo{ExitCode: 124}}
	if request.Cmd[1] == "status" {
		data, err := json.Marshal(protocol.Status{ID: request.Cmd[3], ExitCode: 7, CleanupComplete: true})
		if err != nil {
			return nil, err
		}
		script = fake.Script{Stdout: data}
	}
	if err := d.QueueExec(script); err != nil {
		return nil, err
	}
	return d.Driver.Exec(ctx, id, request)
}

func TestExecEndpoint_AuthValidationOwnershipAndCommandStatus(t *testing.T) {
	handler, _, owner, other := lifecycleFixture(t, &apiExecDriver{Driver: fake.New()})
	created := request(handler, "POST", "/v1/sandboxes", owner, `{"template":"base"}`)
	var row gen.Sandbox
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	if err := json.Unmarshal(created.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	endpoint := "/v1/sandboxes/" + row.Id + "/exec"
	body := `{"cmd":["program","untrusted ; argv"],"env":{"SECRET":"private"},"stdin":"aW5wdXQ="}`
	if got := request(handler, "POST", endpoint, "", body); got.Code != 401 {
		t.Fatal(got.Code)
	}
	if got := request(handler, "POST", endpoint, other, body); got.Code != 404 {
		t.Fatal("foreign resource leaked", got.Code)
	}
	for _, invalid := range []string{`null`, `{}`, `{"cmd":[]}`, `{"cmd":[""]}`, `{"cmd":["x"],"shell":"echo both"}`, `{"shell":""}`, `{"cmd":["x"],"timeout_seconds":3601}`, `{"cmd":["x"],"timeout_seconds":0}`, `{"cmd":["x"],"env":{"BAD=KEY":"value"}}`, `{"cmd":["x"],"cwd":"/etc"}`, `{"cmd":["x"],"stdin":"not base64"}`, `{"cmd":["x"],"extra":true}`} {
		if got := request(handler, "POST", endpoint, owner, invalid); got.Code != 400 {
			t.Fatal("invalid request accepted", invalid, got.Code, got.Body.String())
		}
	}
	for _, valid := range []string{body, `{"shell":"echo explicit user shell"}`} {
		got := request(handler, "POST", endpoint, owner, valid)
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		var result gen.ExecResult
		if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.ExitCode != 7 || result.Stdout != "api-output" || result.Stderr != "api-error" || result.ExecId == "" || result.TimedOut {
			t.Fatal("lost real exit or streams")
		}
	}
	if got := request(handler, "DELETE", "/v1/sandboxes/"+row.Id, owner, ""); got.Code != 204 {
		t.Fatal(got.Code)
	}
	if got := request(handler, "POST", endpoint, owner, body); got.Code != 409 {
		t.Fatal("ended exec accepted", got.Code)
	}
}
