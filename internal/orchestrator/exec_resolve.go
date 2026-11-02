package orchestrator

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/runtime"
)

const helperPath = "/opt/arcana/sbx-exec"

// ExecRequest preserves explicit argv versus explicit shell execution.
type ExecRequest struct {
	Cmd            *[]string
	Shell          *string
	Cwd            *string
	Env            map[string]string
	TimeoutSeconds *int64
	Stdin          []byte
}
type execPlan struct {
	argv    []string
	cwd     string
	env     map[string]string
	timeout int64
	command string
	shell   bool
	stdin   []byte
}

func resolveExec(cfg config.Config, request ExecRequest) (execPlan, error) {
	if len(request.Stdin) > 65536 {
		return execPlan{}, ErrInvalid
	}
	if err := validateMap(request.Env, true); err != nil {
		return execPlan{}, err
	}
	if (request.Cmd == nil) == (request.Shell == nil) {
		return execPlan{}, fmt.Errorf("%w: exactly one of cmd or shell is required", ErrInvalid)
	}
	plan := execPlan{cwd: "/workspace", timeout: int64(cfg.Limits.Exec.DefaultTimeoutSeconds), env: maps.Clone(request.Env), stdin: slices.Clone(request.Stdin)}
	if request.Cwd != nil {
		plan.cwd = *request.Cwd
	}
	if len(plan.cwd) > 4096 || !allowedPath(plan.cwd, cfg.Limits.Files.AllowedRoots) {
		return execPlan{}, ErrPath
	}
	plan.cwd = path.Clean(plan.cwd)
	if request.TimeoutSeconds != nil {
		plan.timeout = *request.TimeoutSeconds
	}
	if plan.timeout < 1 || plan.timeout > int64(cfg.Limits.Exec.MaxTimeoutSeconds) || len(plan.stdin) > 65536 {
		return execPlan{}, ErrInvalid
	}
	if request.Shell != nil {
		if len(*request.Shell) == 0 {
			return execPlan{}, ErrInvalid
		}
		plan.argv = []string{"/bin/sh", "-c", *request.Shell}
		plan.shell = true
		plan.command = *request.Shell
	} else {
		if len(*request.Cmd) == 0 || len(*request.Cmd) > 256 || (*request.Cmd)[0] == "" {
			return execPlan{}, ErrInvalid
		}
		plan.argv = *request.Cmd
	}
	if len(plan.argv) > 256 {
		return execPlan{}, ErrInvalid
	}
	size := 0
	for _, arg := range plan.argv {
		size += len(arg)
		if strings.ContainsRune(arg, 0) || size > 120000 {
			return execPlan{}, ErrInvalid
		}
	}
	plan.argv = slices.Clone(plan.argv)
	if !plan.shell {
		data, _ := json.Marshal(plan.argv)
		plan.command = string(data)
	}
	return plan, nil
}

func (p execPlan) request(id string) runtime.ExecRequest {
	argv := []string{helperPath, "run", "--id", id, "--timeout", strconv.FormatInt(p.timeout, 10), "--cwd", p.cwd, "--"}
	return runtime.ExecRequest{Cmd: append(argv, p.argv...), Cwd: "/workspace", Env: p.env}
}
