# Reference agent results

## Status: no real-model run has been recorded yet

The acceptance criterion for T3.4 asks for the fix-agent to run end to end on a fixture with a **local
model** and for the results, failures included, to be recorded here. That has **not been done**: the
environment where the agent was built had no model endpoint (no Ollama, and about 1 GB of RAM). No
pass rate for any model exists, and none is claimed. Do not read the validation below as a model result.

To produce real results, with Arcana running and an OpenAI-compatible endpoint available:

```sh
export ARCANA_URL=... ARCANA_API_KEY=... FIXAGENT_MODEL=...   # FIXAGENT_BASE_URL defaults to local Ollama
make eval-agent                                  # single attempt per fixture
make eval-agent EVAL_ARGS="--parallel 4"         # four attempts from a snapshot, first pass wins
```

Then replace this section with the table the command prints, plus the model name, quantisation,
hardware, budgets and every failure with its reason (`step budget`, `token budget`, `model stopped`,
`error: ...`). Parallel mode is meant to show a higher pass rate than a single attempt; report both
runs so that claim is measured rather than assumed.

## What has been validated

These checks use real sandboxes (runsc) but **no real model**.

| Check | How | Result (2026-11-12) |
|---|---|---|
| All 10 fixtures are real, fixable bugs | `fix-agent verify-fixtures`, also an integration test: visible and hidden tests fail on the project and pass with the reference solution | 10 of 10 verified |
| The agent loop, HTTP client, Arcana API, hidden-test judge, cleanup | integration test with a *scripted* OpenAI-compatible server that returns the reference solution (`py-off-by-one`, `js-numeric-sort`, `go-nil-map`) | all solved in 3 steps |
| Snapshot, fork and first-to-pass cancellation | the same test in `--parallel 2` mode on `py-mutable-default` | solved; no sandbox or snapshot left behind |
| Tool-loop behaviour | unit tests: budgets, malformed tool calls, oversize output, per-turn call cap, cancellation | pass |

Verifying the fixtures found a real environment constraint: Arcana mounts `/tmp` `noexec`, so `go test`
cannot run its test binary from there. The fixtures' Go commands set `GOTMPDIR` to the workspace.
