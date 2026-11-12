# fix-agent

A reference coding agent that shows what Arcana is for. Given a project with a failing test it
creates a sandbox, uploads the project, lets an LLM explore and fix the code through four tools
(`run_command`, `read_file`, `write_file`, `list_files`), and verifies the fix with tests. It is a demo,
not a product.

The model is any OpenAI-compatible chat endpoint with tool calling; the default targets a local
[Ollama](https://ollama.com):

| Variable | Default |
|---|---|
| `FIXAGENT_BASE_URL` | `http://localhost:11434/v1` |
| `FIXAGENT_MODEL` | `qwen2.5-coder:7b` |
| `FIXAGENT_API_KEY` | none (only if your endpoint needs one; never commit it) |
| `ARCANA_URL`, `ARCANA_API_KEY` | `http://127.0.0.1:7070`, none |

Small local models vary a lot in how well they call tools. Pick one that supports tool calling.

## Use

Prerequisites: a running `arcana serve` whose config has the `python`, `node` and `go` templates
(`make templates`, see `deploy/config.example.yaml`), and a model endpoint.

```sh
# Fix one fixture.
go run ./examples/fix-agent run --fixture examples/fixtures/py-off-by-one

# Fix any project that has a failing test (judged by that test command).
go run ./examples/fix-agent run --dir ./my-project --template python --test 'python -m unittest'

# Try four attempts in parallel from a snapshot; the first to pass wins.
go run ./examples/fix-agent run --fixture examples/fixtures/go-nil-map --parallel 4

# Run the whole 10-fixture set and print a results table.
make eval-agent
```

Budgets bound every run: `--max-steps` (model calls, default 20) and `--max-tokens` (default 60000,
the sum of each request's total tokens, so it grows with the conversation). Output of every tool call
is capped at 8 KiB, and a turn may make at most 8 tool calls.

## How success is judged

The model sees only the project, its README and the visible test command. After each turn that ran a
command or wrote a file, the harness places the fixture's hidden tests in the sandbox, runs them,
removes them again (and any bytecode caches that would reveal their names) and stops the run once they
pass. The model never sees hidden test files or their output. Sandboxes have no network.

With `--parallel N`, the prepared project is snapshotted, the snapshot is forked into N sandboxes and one
agent works in each (temperature rises by 0.2 per attempt for variety). The first attempt that passes
cancels the others. Sandboxes and the snapshot are always removed, including on errors.

## Fixtures

`examples/fixtures/` holds 10 small buggy projects (4 Python, 3 Node, 3 Go). See its README.
`fix-agent verify-fixtures` proves each is a real, fixable bug: both test suites must fail on the
project and pass with the reference solution.

## Tests

```sh
go test ./examples/fix-agent/...                       # unit tests: fake model and fake Arcana
go test -tags=integration ./examples/fix-agent/...     # needs Docker + runsc and the template images
```

The integration tests verify all 10 fixtures on real sandboxes and run the agent end to end against a
*scripted* model that already knows the answer. That validates the plumbing (HTTP client, tool loop,
Arcana API, hidden-test judge, snapshot and fork, cleanup); it says nothing about how well any real
model performs. See [docs/agent-results.md](../../docs/agent-results.md).
