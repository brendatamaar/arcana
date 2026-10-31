# PLAN — Arcana execution plan

This file is the ordered, checkable plan for building Arcana. `PRD.md` defines **what** and **why**; this file defines **in what order** and **how we know a task is done**; `AGENTS.md` defines **how to work** in the repo.

## How to use this file (for Claude and other agents)

1. Work on **one task at a time**, in order. Pick the first task whose status is `[ ]` and whose dependencies are all `[x]`.
2. Before starting, change its status to `[~]` and read the PRD sections it references.
3. Implement until every acceptance criterion is met and every verify command passes.
4. Mark the task `[x]` and add a one-line note under it (`> Done: …`) with anything the next task should know.
5. If blocked, mark `[!]`, write the reason under the task, add an entry to **Open questions**, and stop. Do not work around a blocker by weakening a security requirement.
6. If a spike or implementation changes a design decision, add an entry to the **Decision log** and update `PRD.md` in the same commit.
7. Commit after each task (Conventional Commits, see `AGENTS.md`). Never batch several tasks into one commit.

Status legend: `[ ]` todo · `[~]` in progress · `[x]` done · `[!]` blocked

## Milestones

| Phase | Release | Goal | Exit criteria |
|---|---|---|---|
| 0 | — | Foundations and risk spikes | Repo builds in CI; spike findings recorded; PRD updated where spikes disagree |
| 1 | v0.1 | Core MVP | Lifecycle, exec, files, limits, reaper, CLI, doctor, Go client; escape suite v1 green on runsc in CI |
| 2 | v0.2 | Self-hosting ready | Templates, warm pool, allowlist networking, audit, metrics, releases and installers, docs |
| 3 | v0.3 | Agent ecosystem and showcase | Snapshots and fork, MCP, SDKs, reference agent, benchmarks, read-only React dashboard, docs site |
| 4 | Stretch | Depth | envd, Firecracker, runtime comparison, full web dashboard, containerd, Helm |

---

## Phase 0 — Foundations and spikes

### [x] T0.1 Repository scaffold
- **Deps:** none
- **Do:**
  - `go mod init github.com/brendatamaar/arcana`; directory layout from `AGENTS.md`.
  - `cmd/arcana/main.go` with Cobra root command and `version` subcommand (version injected through `-ldflags`).
  - `Makefile` targets: `build`, `test`, `test-integration`, `test-escape`, `lint`, `generate`, `verify-generated`, `check`, `clean`.
  - `.golangci.yml` (enable at least: `govet`, `staticcheck`, `errcheck`, `gosec`, `revive`, `gofumpt`, `misspell`).
  - `LICENSE` (Apache-2.0), `README.md` stub, `CHANGELOG.md`, `.gitignore`, `.editorconfig`.
  - GitHub Actions `ci.yml`: Go setup, `make check`, on push and pull request.
- **Acceptance:** `make check` passes locally and in CI; `arcana version` prints version, commit and build date.
- **Verify:** `make check && go run ./cmd/arcana version`
> Done: Repository scaffold, version metadata, Make targets, lint configuration and CI added. Local make check and version checks passed; CI candidate dd198bd passed (https://github.com/brendatamaar/arcana/actions/runs/41220302481). Runtime spikes will use the owner-authorized Linux VPS.

### [x] T0.2 Spike: Docker + runsc lifecycle and exec streaming
- **Deps:** T0.1
- **Do:** Throwaway program in `spikes/001-runsc-exec/` using the official Docker Go client: create and start a container with `Runtime: "runsc"` and the hardening flags from PRD FR-RL-2, run an exec with attached stdout/stderr (demultiplexed) and stdin, run a TTY exec with resize, then destroy. Measure cold create time (20 runs, p50/p95) for runc and runsc.
- **Acceptance:** `docs/spikes/001-runsc-exec.md` records: what works, API quirks, measured timings, and whether PRD §8.2 targets look realistic.
- **Verify:** document exists and includes a timing table.

> Done: Official Moby client lifecycle, attached demux/stdin and TTY resize passed under runc and runsc; 20 sequential timings per runtime recorded in docs/spikes/001-runsc-exec.md. VPS make check, test-integration and test-escape targets passed; the latter two remain scaffold targets until the real suites are introduced.

### [x] T0.3 Spike: process-tree termination with `sbx-exec`
- **Deps:** T0.2
- **Do:** Minimal `cmd/sbx-exec` prototype (static, `CGO_ENABLED=0`) implementing `run`, `kill` and `status` per PRD §9.4. Bind-mount it read-only into a runsc container. Test: a command that spawns children and ignores SIGTERM is fully killed on timeout and on `kill`; TTY mode keeps job control working (`Setpgid` + `Foreground` + `Ctty` in `SysProcAttr`). Repeat under runc. If the bind mount misbehaves under runsc, test the fallback of copying the helper in through exec + tar (PRD R5).
- **Acceptance:** `docs/spikes/002-process-termination.md` with results for runsc and runc, chosen mechanism, and any PRD changes.
- **Verify:** document exists; prototype tests pass.

> Done: Static stdlib helper, parser tests and attack tests passed under runc/runsc, including ioctl-verified foreground TTY and second-sandbox health. make check, test-integration and test-escape passed; runsc /proc tpgid quirk documented in docs/spikes/002-process-termination.md.

### [x] T0.4 Spike: workspace backends and exec-based file transfer
- **Deps:** T0.2
- **Do:** Under runsc: (a) named volume at `/workspace`; stream a tar in and out through exec (`tar -xzf -` / `tar -czf -`); confirm contents round-trip. (b) tmpfs `/workspace` with a size limit; confirm ENOSPC at the limit. (c) Confirm read-only rootfs + tmpfs `/tmp` and `/run/sbx`. (d) Verify that files written inside the sandbox can be read and archived back through exec with identical contents, confirming the chosen transfer path for PRD R1. (e) Measure `du -sb /workspace` cost on a 10k-file tree.
- **Acceptance:** `docs/spikes/003-workspace.md` with results and recommended watchdog interval.
- **Verify:** document exists.

> Done: Named-volume binary/archive round trips through exec, rootfs/tmpfs checks and workspace ENOSPC passed under runsc; 10k-file du measured at 547–644 ms, retaining the 15s watchdog interval. make check, test-integration and test-escape passed; findings in docs/spikes/003-workspace.md.

### [x] T0.5 Spike: allowlist networking topology
- **Deps:** T0.2
- **Do:** Create an internal Docker network with an explicit /28 subnet, attach a runsc sandbox and a proxy container (simple Go `CONNECT` proxy) that is also attached to a normal bridge network. Confirm: sandbox reaches an allowlisted host only through the proxy; direct TCP fails; `169.254.169.254` unreachable; a second sandbox on a different internal network is unreachable. Note any DNS behaviour on internal networks.
- **Acceptance:** `docs/spikes/004-network.md` with topology diagram, results and recommended design.
- **Verify:** document exists.

> Done: Internal /28 topology passed CONNECT allow/deny, direct internet/metadata denial and live second-sandbox isolation under runsc. DNS and pre-start interface requirements recorded in docs/spikes/004-network.md and PRD; make check, test-integration and test-escape passed.

### [x] T0.6 Record decisions
- **Deps:** T0.2–T0.5
- **Do:** Write ADRs in `docs/decisions/` (`0001-runtime-docker-runsc.md`, `0002-exec-helper.md`, `0003-workspace-and-files.md`, `0004-network-egress.md`). Update `PRD.md` wherever spikes changed the design. Delete `spikes/` code (keep the docs).
- **Acceptance:** ADRs exist; PRD consistent with findings; Decision log below updated.
- **Verify:** `make check`
> Done: ADRs 0001–0004 recorded; PRD DNS/interface details and exec-only spike scope are consistent with findings. Temporary spikes/ code and dependencies removed; prototype helper and reusable tests retained. Final VPS make check, test-integration, test-escape, static build and version checks passed; no spike containers, networks or volumes remain.

---

## Phase 1 — v0.1 Core MVP

### [ ] T1.1 Configuration
- **Deps:** T0.6 · **PRD:** §13, NFR-OPS-3
- **Do:** `internal/config` with koanf: defaults struct, YAML file, env overrides (`ARCANA_` + `__` separator), validation (ranges, CIDR parsing, defaults ≤ maximums, allowed roots absolute, runc guard). `--config` flag on root command. Ship `deploy/config.example.yaml` matching PRD §13 (v0.1 keys active, later keys commented).
- **Acceptance:** invalid configs produce clear, field-specific errors; env overrides work for nested keys.
- **Verify:** `go test ./internal/config/...` (table-driven: defaults, file, env, each validation rule)

### [ ] T1.2 Logging, IDs and API errors
- **Deps:** T1.1 · **PRD:** §9.6, §10.3, NFR-OPS-1, NFR-SEC-4
- **Do:** `slog` setup (json/text, level); request ID middleware; `internal/ids` (prefixed `crypto/rand` base32 IDs); `internal/apierr` with typed error codes mapped to HTTP status and the JSON error envelope; a redaction helper so API keys and env values never reach logs.
- **Acceptance:** every error code in PRD §10.3 has a constructor and status mapping; IDs match the regex `^(sbx|exe|snp|key|aud|ses|req)_[a-z2-7]{20}$`.
- **Verify:** `go test ./internal/ids/... ./internal/apierr/...`

### [ ] T1.3 Store
- **Deps:** T1.1 · **PRD:** §11, NFR-REL-3
- **Do:** `internal/store` using `modernc.org/sqlite` (WAL, `busy_timeout`, foreign keys on, single writer); goose migrations embedded with `embed.FS` and applied on open; sqlc config and queries for `api_keys`, `sandboxes`, `execs`, `meta` (snapshots and audit come later). Store `install_id` in `meta` on first open.
- **Acceptance:** fresh DB migrates; re-open is idempotent; queries generated by sqlc compile; `make verify-generated` detects stale sqlc output.
- **Verify:** `make generate && go test ./internal/store/...`

### [ ] T1.4 API keys and auth
- **Deps:** T1.2, T1.3 · **PRD:** FR-AU-1…5
- **Do:** `internal/auth`: key generation (`arc_live_` + 32 base62 chars), SHA-256 hashing, constant-time comparison, prefix display, `last_used_at` update (debounced), admin flag; bootstrap admin key on empty DB (print once, write `data_dir/admin.key` with `0600`); HTTP middleware putting the principal in context; `arcana keys create|list|revoke` operating on the local DB.
- **Acceptance:** revoked keys rejected; non-admin cannot call admin endpoints; bootstrap happens exactly once; key never appears in logs.
- **Verify:** `go test ./internal/auth/...`

### [ ] T1.5 OpenAPI spec and HTTP server skeleton
- **Deps:** T1.2, T1.4 · **PRD:** §10
- **Do:** `api/openapi.yaml` (OpenAPI 3.0) covering all v0.1 endpoints, schemas and error envelope; `oapi-codegen` configured for std `net/http` server and models into `internal/api/gen`; `make generate` wires it; `internal/api` server with routing, auth middleware, request ID, panic recovery, JSON helpers, `/v1/health`; `arcana serve` starts the server with graceful shutdown (NFR-REL-2). Warn when listen address is not loopback (NFR-SEC-3).
- **Acceptance:** spec validates; generated code compiles; unimplemented handlers return 501 with the standard envelope; `serve` shuts down cleanly on SIGTERM.
- **Verify:** `make generate verify-generated && go test ./internal/api/...`

### [ ] T1.6 Runtime driver interface and fake driver
- **Deps:** T1.1 · **PRD:** §9.3
- **Do:** `internal/runtime` with `Driver`, `Spec`, `Instance`, `ExecRequest`, `ExecSession`, `ExitInfo`, `Stats`, `Capabilities`. `internal/runtime/fake`: in-memory driver that can script exec output, exit codes, delays and failures, for orchestrator unit tests.
- **Acceptance:** fake driver satisfies the interface with compile-time assertion; fake has its own tests.
- **Verify:** `go test ./internal/runtime/...`

### [ ] T1.7 Docker driver
- **Deps:** T1.6 · **PRD:** FR-RL-1, FR-RL-2, FR-RL-3, §9.5, FR-DR-3
- **Do:** `internal/runtime/docker` using the official Docker Engine Go client: create + start with runtime from config, resource limits (NanoCPUs, Memory, MemorySwap = Memory, PidsLimit, ulimits), `CapDrop: ALL`, `SecurityOpt: no-new-privileges`, `ReadonlyRootfs`, `User: 1000:1000`, tmpfs `/tmp` and `/run/sbx` with size limits, workspace volume or tmpfs, `NetworkMode: none`, helper bind mount (read-only), labels and names; init process keeping the container alive (`--init` + `sleep infinity` or equivalent); exec with demuxed streams or TTY; resize; destroy (container, then volume; idempotent; ignores not-found); list by labels; stats.
- **Acceptance:** integration tests (build tag `integration`) create, exec, destroy a sandbox under runsc when available and runc otherwise; all hardening flags asserted via container inspect.
- **Verify:** `go test ./internal/runtime/docker/... && make test-integration`

### [ ] T1.8 `sbx-exec` helper
- **Deps:** T0.3, T1.7 · **PRD:** §9.4, FR-EX-4
- **Do:** Production version of `cmd/sbx-exec` from the spike: `run`, `kill`, `status`; status JSON in `/run/sbx`; exit code passthrough; TTY mode handling; no dependencies beyond stdlib. Build it in `make build` before Arcana, embed the binary with `embed.FS` (`internal/helper`), and write it to `data_dir/bin/sbx-exec` (mode 0755, checksum-verified) at `serve` start.
- **Acceptance:** unit tests for argument parsing and status; integration test: child processes that ignore SIGTERM are killed on timeout and on `kill`, under runsc.
- **Verify:** `go test ./cmd/sbx-exec/... && make test-integration`

### [ ] T1.9 Orchestrator: lifecycle
- **Deps:** T1.3, T1.6 · **PRD:** FR-LC-1…4, FR-LC-6, FR-LC-9, FR-TP-1
- **Do:** `internal/orchestrator`: resolve template and merge overrides (clamped to maximums), quota checks, create via driver, persist state transitions, get/list with ownership filtering, idempotent destroy, keepalive, activity tracking. Wire handlers for sandbox endpoints.
- **Acceptance:** unit tests with fake driver cover state machine, quotas, clamping, ownership (non-admin gets 404 for others' sandboxes), driver failure → `failed` with reason.
- **Verify:** `go test ./internal/orchestrator/... ./internal/api/...`

### [ ] T1.10 Sync exec
- **Deps:** T1.8, T1.9 · **PRD:** FR-EX-1…3, FR-EX-5, FR-EX-6, FR-EX-8, FR-EX-9
- **Do:** Exec manager that wraps commands with `sbx-exec run`, enforces timeout defaults and maximums, captures output with a combined cap (keeps draining after the cap), reads final status via `sbx-exec status`, records the `execs` row, enforces per-sandbox concurrency, and starts a host-side hard deadline (timeout + 10 s → destroy sandbox with `exec_deadline`). `cmd` vs `shell` validation. UTF-8 sanitising of output.
- **Acceptance:** unit tests with fake driver for caps, timeouts, concurrency, deadline; integration test runs `python3 -c`/`sh -c` style commands with expected exit codes.
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

### [ ] T1.11 Streaming exec (WebSocket)
- **Deps:** T1.10 · **PRD:** FR-EX-7, §10.4
- **Do:** `internal/stream` using `coder/websocket`: protocol messages exactly as PRD §10.4, start-first validation, stdin and stdin_close, TTY + resize, cancel via `sbx-exec kill`, cancel on client disconnect, backpressure with 1 MB send buffer, ping/pong keepalive, close after `exit`.
- **Acceptance:** tests with an in-process server and fake driver for every message type, disconnect handling and backpressure; integration test streams output of a command that prints over time.
- **Verify:** `go test ./internal/stream/... && make test-integration`

### [ ] T1.12 Files API
- **Deps:** T1.10 · **PRD:** FR-FS-1…5
- **Do:** Path validation (absolute, cleaned, under `allowed_roots`, reject NUL and `..` escape, including URL-encoded forms decoded by the router); operations through exec inside the sandbox: write (stdin → `cat > path` via argv, creating parent dirs), read (with size check first via `stat`), list (`find` with depth or a small helper subcommand of `sbx-exec` producing JSON — prefer the helper for consistent output), delete, archive upload (`tar -xzf - -C path`) and download (`tar -czf - -C path .`), size caps enforced while streaming.
- **Acceptance:** path validation table tests; integration tests for each operation; symlink in workspace pointing to `/etc/passwd` returns the **sandbox's** file, never the host's.
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

### [ ] T1.13 Reaper, watchdog and reconciliation
- **Deps:** T1.9, T1.10 · **PRD:** FR-LC-5, FR-LC-7, FR-LC-8, FR-RL-3, NFR-REL-1
- **Do:** Background loops with context cancellation: reaper (TTL, idle), workspace watchdog (`du` via exec for `volume` backend at the configured interval → destroy with `disk_quota`), startup reconciliation by labels (containers, volumes; networks once they exist), marking lost sandboxes `failed/lost`. Use an injectable clock for tests.
- **Acceptance:** unit tests with fake clock and fake driver; integration test: kill Arcana mid-exec, restart, orphans removed (ESC-20).
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

### [ ] T1.14 Go client
- **Deps:** T1.11, T1.12 · **PRD:** FR-SD-1
- **Do:** `pkg/client`: typed methods for all v0.1 endpoints, streaming exec helper (`ExecStream` returning readers and an exit result), typed errors carrying API error codes, context support, configurable HTTP client. Godoc examples.
- **Acceptance:** tests against an `httptest` server running the real API with fake driver.
- **Verify:** `go test ./pkg/client/...`

### [ ] T1.15 CLI
- **Deps:** T1.14 · **PRD:** FR-CL-1, FR-CL-2
- **Do:** Client subcommands via `pkg/client`: `sandbox create|list|get|rm`, `exec <id> -- argv` (streams; exits with remote code; `--tty` when stdin is a terminal), `files put|get|ls`. Human-readable tables by default, `--json` for scripting. `--server`/`--api-key` flags and env vars.
- **Acceptance:** CLI tests using the client against an in-process server; `arcana exec` returns the remote exit code.
- **Verify:** `go test ./cmd/arcana/...`

### [ ] T1.16 Doctor
- **Deps:** T1.7, T1.3 · **PRD:** FR-DR-1…3
- **Do:** `internal/doctor` with independent checks returning pass/warn/fail plus a fix hint (including gVisor install steps); `arcana doctor [--json]`; `serve` runs blocking checks at startup and refuses to start on failure.
- **Acceptance:** unit tests with injectable system probes for each check; manual run documented in the PR.
- **Verify:** `go test ./internal/doctor/... && go run ./cmd/arcana doctor`

### [ ] T1.17 Escape suite v1 and CI with runsc
- **Deps:** T1.12, T1.13 · **PRD:** §12.4 (v0.1 rows), NFR-SEC-2
- **Do:** `test/escape` (build tag `escape`) implementing ESC-01…14, ESC-20, ESC-21 through the public API. CI job installs gVisor from its apt repository, runs `sudo runsc install`, restarts Docker, then runs `make test-integration test-escape` against runsc.
- **Acceptance:** all v0.1 escape tests pass in CI on runsc; each test asserts host/Arcana health after the attack.
- **Verify:** `make test-escape` locally with runsc; CI green.

### [ ] T1.18 v0.1 docs and release
- **Deps:** T1.15–T1.17
- **Do:** README: what it is, security model summary, quickstart (`go install`, gVisor setup, `doctor`, `serve`, first exec), CLI examples, limitations (PRD §12.5). `CHANGELOG.md` entry. Tag `v0.1.0`.
- **Acceptance:** following the README on a clean Ubuntu VM reaches a first exec.
- **Verify:** `make check test-integration test-escape`

---

## Phase 2 — v0.2 Self-hosting ready

### [ ] T2.1 Official templates
- **Deps:** T1.18 · **PRD:** FR-TP-2, FR-TP-4, FR-RL-4
- **Do:** `templates/{base,python,node,go}/Dockerfile`: slim bases, UID 1000 user `sandbox`, `tar`, `du`, `find`, `coreutils`, `git`, `ca-certificates`; env so package managers install into `/workspace` (venv at `/workspace/.venv` created lazily by a profile script or documented usage, `npm_config_cache=/workspace/.npm`, `GOPATH=/workspace/.go`, `GOCACHE=/workspace/.cache/go`). CI workflow builds multi-arch images and pushes to GHCR on tags; smoke test each image under runsc.
- **Acceptance:** each template runs `python -m pip install requests` / `npm i lodash` / `go mod download` successfully into `/workspace` with allowlist networking (after T2.4) or offline smoke tests before it.
- **Verify:** `make templates && make test-integration`

### [ ] T2.2 Template registry endpoint
- **Deps:** T2.1 · **PRD:** FR-TP-3
- **Do:** Config-driven registry, `GET /v1/templates`, validation that template images exist at startup (warn, not fail).
- **Verify:** `go test ./internal/orchestrator/... ./internal/api/...`

### [ ] T2.3 Warm pool
- **Deps:** T2.2 · **PRD:** FR-LC-10
- **Do:** Per (template, network mode) pool with target size; background filler with backoff on errors; acquire applies labels and records `source: pool`; pool members labelled and reconciled; pool drained on shutdown when `destroy_on_shutdown`; metrics hooks.
- **Acceptance:** unit tests for filler/acquire races (`-race`); integration test shows faster acquire than cold create.
- **Verify:** `go test -race ./internal/orchestrator/... && make test-integration`

### [ ] T2.4 Allowlist networking and egress proxy
- **Deps:** T0.5, T2.2 · **PRD:** FR-NW-2…7, FR-CL-3
- **Do:** `internal/netpolicy`: subnet allocator from `subnet_pool` (persisted allocations or derived from Docker state on start), per-sandbox internal network creation and cleanup, proxy container lifecycle (`arcana proxy` using the Arcana image), connecting the proxy to each sandbox network, injecting `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`. Proxy server: `CONNECT` and plain HTTP, per-source-IP allowlist pushed from Arcana (control channel on a local socket or HTTP with a shared secret), DNS resolution then IP range checks, connect to resolved IP, byte counting, decision events back to Arcana. Presets from config. Doctor check for subnet overlap.
- **Acceptance:** unit tests for allocator and IP range checks (IPv4 and IPv6); integration tests for allowed and denied hosts.
- **Verify:** `go test ./internal/netpolicy/... && make test-integration`

### [ ] T2.5 Audit log
- **Deps:** T2.4 · **PRD:** FR-AD-1, FR-AD-2, FR-FS-6
- **Do:** Migration for `audit_events`; async buffered writer (never blocks request path; drops with a counter metric if the buffer overflows); events from all listed actions; `GET /v1/audit` with filters and cursor pagination; daily pruning.
- **Verify:** `go test ./internal/audit/... ./internal/api/...`

### [ ] T2.6 Metrics, dashboard and Compose
- **Deps:** T2.3, T2.5 · **PRD:** §15, NFR-OPS-2
- **Do:** `internal/metrics` with all PRD §15 metrics on `metrics_listen`; `deploy/grafana/arcana.json`; `deploy/compose/docker-compose.yml` with Arcana (host Docker socket mounted), Prometheus and Grafana with provisioning.
- **Acceptance:** `docker compose up` shows populated dashboard after running a few execs.
- **Verify:** `go test ./internal/metrics/...`; manual Compose check noted in PR.

### [ ] T2.7 Quotas and rate limiting
- **Deps:** T2.3 · **PRD:** FR-AU-6
- **Do:** Per-key token bucket for sandbox creation; `rate_limited` errors with `Retry-After`.
- **Verify:** `go test ./internal/api/...`

### [ ] T2.8 Escape suite v2 (network)
- **Deps:** T2.4 · **PRD:** §12.4 ESC-15…19
- **Do:** Implement network escape tests; add to CI.
- **Verify:** `make test-escape`

### [ ] T2.9 Releases and installers
- **Deps:** T2.6
- **Do:** `.goreleaser.yaml` (linux amd64/arm64, `CGO_ENABLED=0`, checksums, `.deb`/`.rpm` via nfpm, Arcana container image to GHCR, changelog); release workflow on tags; `scripts/install.sh` (POSIX sh, arch detection, checksum verification, config install, optional systemd unit, runs `doctor`); `deploy/systemd/arcana.service` with hardening directives compatible with Docker socket access. Test `install.sh` with `shellcheck` in CI.
- **Acceptance:** a pre-release tag produces all artifacts; install script works on a fresh Ubuntu VM.
- **Verify:** `goreleaser release --snapshot --clean` locally; `shellcheck scripts/install.sh`

### [ ] T2.10 Docs for self-hosters
- **Deps:** T2.9
- **Do:** `docs/` pages: install (each channel), configuration reference (generated from the config struct or hand-maintained and checked by a test that every key is documented), security model and limitations, networking, operations (upgrades, backups of `data_dir`, logs, metrics).
- **Verify:** docs build (see T3.8) or Markdown lint in CI.

### [ ] T2.11 v0.2 release
- **Deps:** T2.1–T2.10
- **Do:** Update README and CHANGELOG; tag `v0.2.0`.
- **Verify:** `make check test-integration test-escape`; release workflow green.

---

## Phase 3 — v0.3 Agent ecosystem and showcase

### [ ] T3.1 Snapshots, restore and fork
- **Deps:** T2.11 · **PRD:** FR-SN-1…5
- **Do:** Migration for `snapshots`; snapshot by streaming `tar -czf - -C /workspace .` through exec into `data_dir/snapshots/<id>.tar.gz` with SHA-256; `from_snapshot` create path (extract inside sandbox); fork N in parallel with `errgroup` and partial-failure reporting; limits; endpoints; Go client and CLI support (`arcana snapshot create|list|rm`, `arcana fork`).
- **Acceptance:** integration test: install a package into `/workspace`, snapshot, fork 3, package present in each, changes in one fork invisible to others.
- **Verify:** `make test-integration`

### [ ] T3.2 MCP server
- **Deps:** T3.1 · **PRD:** FR-MC-1…3, FR-CL-4
- **Do:** `internal/mcp` with the official MCP Go SDK, stdio transport, tools listed in PRD, session labelling and cleanup. Docs with a config snippet for MCP clients (for example Claude Desktop or Claude Code).
- **Acceptance:** tests using the SDK's in-memory client transport for each tool; manual test with a real MCP client noted in PR.
- **Verify:** `go test ./internal/mcp/...`

### [ ] T3.3 Python and TypeScript SDKs
- **Deps:** T3.1 · **PRD:** FR-SD-2
- **Do:** `sdk/python` and `sdk/typescript` generated from `api/openapi.yaml` (choose generators and record in an ADR), plus hand-written streaming exec wrappers and README examples; CI job running each SDK's tests against a live Arcana with runc.
- **Verify:** SDK test commands documented in each SDK README; CI green.

### [ ] T3.4 Reference agent and fixtures
- **Deps:** T3.2 · **PRD:** FR-SD-3, FR-SD-4
- **Do:** `examples/fix-agent` (Go): OpenAI-compatible chat client (base URL, model, key from env; default `http://localhost:11434/v1` for Ollama), tool loop with `run_command`, `read_file`, `write_file`, `list_files`; step and token budgets; stops when hidden tests pass. `--parallel N` mode using snapshot + fork. `examples/fixtures/` with 10 small buggy projects and hidden tests. `make eval-agent` prints a results table.
- **Acceptance:** agent runs end-to-end on at least one fixture with a local model; results recorded in `docs/agent-results.md` including failures.
- **Verify:** `go test ./examples/fix-agent/...`; manual eval run.

### [ ] T3.5 Benchmark harness
- **Deps:** T3.1 · **PRD:** §8.2, FR-CL-5
- **Do:** `arcana bench` scenarios: cold create, pool acquire, exec overhead, density (create until limits), snapshot and fork times; outputs JSON and Markdown; `docs/benchmarks.md` with results versus targets and host details; charts generated by a small script.
- **Verify:** `go run ./cmd/arcana bench --scenario all --out bench/results`

### [ ] T3.6 Dashboard backend support
- **Deps:** T3.1 · **PRD:** FR-WD-4…8, FR-AU-1, §11 (`sessions`), ESC-22, ESC-23
- **Do:**
  - Migration for `sessions`. In `internal/auth`: create, verify and revoke sessions (token hashed, TTL, invalid once the key is revoked, debounced `last_seen_at`); `POST /v1/sessions` sets the cookie per FR-WD-4; `DELETE /v1/sessions/current` clears it.
  - Auth middleware: cookie accepted only for `GET`/`HEAD` and session logout, otherwise `403 forbidden`; `Origin` / `Sec-Fetch-Site` check on every cookie-authenticated request.
  - `internal/events`: in-process pub/sub with bounded per-subscriber buffers (slow subscriber → `reset` event and disconnect), ring of the last `ui.event_buffer` events for `Last-Event-ID` resume, filtering by owner. Orchestrator publishes lifecycle, exec and pool events.
  - Per-exec output ring buffers fed by both sync and streaming exec paths, kept `tail_retention_seconds` after exit, subscriber cap.
  - SSE handlers for `/v1/events` and exec output (heartbeat comment every 15 s, flush per event, stop on context cancel). `GET /v1/overview` and `GET /v1/sandboxes/{id}/execs`.
  - OpenAPI spec, generated code, `pkg/client` methods (events and output tail) and metrics (`ui_sessions_active`, `event_subscribers`).
- **Acceptance:** unit tests for session lifecycle, cookie method restriction, origin checks, bus fan-out, backpressure and resume, ring buffer wrap and retention; ESC-22 and ESC-23 implemented and passing; sandbox output is still never written to disk.
- **Verify:** `make generate verify-generated && go test -race ./internal/auth/... ./internal/events/... ./internal/api/... && make test-escape`

### [ ] T3.7 Dashboard frontend (React)
- **Deps:** T3.6 · **PRD:** FR-WD-1…3, FR-WD-9…11, ESC-24, §8.2
- **Do:**
  - `web/`: React + TypeScript + Vite app managed with pnpm. Typed API client generated from `api/openapi.yaml` with `openapi-typescript` and called through `openapi-fetch`. TanStack Query for server state, React Router for routes under `/ui`, Tailwind CSS for styling.
  - Pages per FR-WD-2: Login, Overview, Sandboxes, Sandbox detail. Live updates through `EventSource` on `/v1/events`, with reconnect and query-cache updates; full refetch on `reset`.
  - Log viewer: subscribes to the exec output stream, parses ANSI colours into styled spans (never HTML), virtualised rendering for large output, auto-scroll that pauses when the user scrolls up.
  - Vite builds straight into `internal/webui/dist/` (git-ignored except `.gitkeep`). `internal/webui` embeds it, serves `/ui` with the FR-WD-9 headers and SPA fallback, and shows the FR-WD-10 placeholder when the build is absent.
  - Make targets `web`, `web-dev` (Vite dev server proxying `/v1` to a local Arcana), `web-generate`, `web-check`, `web-e2e`, `build-all`. CI job for the web checks; GoReleaser `before` hook runs `make web`.
- **Acceptance:** Vitest + React Testing Library tests for each page against mocked API responses (MSW), including ESC-24; Go tests for `internal/webui` headers, fallback and placeholder; Playwright smoke test against a real Arcana in CI (runc) that logs in, sees a new sandbox appear and watches an exec's output live with zero CSP violations; initial JavaScript ≤ 200 KB gzip.
- **Verify:** `make web-check && make build-all && go test ./internal/webui/... && make web-e2e`

### [ ] T3.8 Docs site and recordings
- **Deps:** T3.4, T3.5, T3.7
- **Do:** VitePress site in `docs/` published to GitHub Pages by a workflow; VHS tapes in `docs/tapes/` for quickstart, parallel fork demo and doctor; a short screen recording of the dashboard during the parallel fork demo; README embeds the quickstart GIF and a dashboard screenshot; architecture page with diagrams from the PRD.
- **Verify:** docs workflow green; site renders.

### [ ] T3.9 Devcontainer
- **Deps:** T3.8 · **PRD:** §14
- **Do:** `.devcontainer/` with Docker-in-Docker, Node LTS and pnpm for dashboard work, and an attempt to configure runsc; fall back to runc with `allow_insecure_runtime` and a loud banner if runsc fails. Document results.
- **Verify:** manual Codespaces check noted in PR.

### [ ] T3.10 v0.3 release
- **Deps:** T3.1–T3.9
- **Do:** README polish (recording, dashboard screenshot, benchmark highlights, security summary), CHANGELOG, tag `v0.3.0`; confirm release binaries include the dashboard.
- **Verify:** all CI workflows green; `/ui` loads from a downloaded release binary.

---

## Phase 4 — Stretch

### [ ] T4.1 `envd` in-sandbox daemon
- **Deps:** T3.10 · Evolve `sbx-exec` into a gRPC daemon (exec, files, streaming, signals) usable over Unix socket or vsock. ADR required.

### [ ] T4.2 Firecracker driver
- **Deps:** T4.1 · `internal/runtime/firecracker` with `firecracker-go-sdk`: rootfs images built from templates, tap networking, vsock to envd, memory snapshots. Skipped in tests when `/dev/kvm` is absent.

### [ ] T4.3 Runtime comparison
- **Deps:** T4.2 · Benchmarks and escape suite across runc, runsc and Firecracker; blog-ready write-up in `docs/runtime-comparison.md`.

### [ ] T4.4 Full web dashboard
- **Deps:** T3.10 · Extend the v0.3 React dashboard with write actions (create from template, destroy, keepalive), key management, snapshots and the audit view. Cookie sessions would then authorize mutations, so add CSRF protection (synchronizer or double-submit token plus the existing `Origin` checks) and extend the escape suite. ADR required before any write action ships.

### [ ] T4.5 containerd driver
- **Deps:** T3.10 · Direct containerd client driver with runsc shim; compare with Docker driver.

### [ ] T4.6 Helm chart
- **Deps:** T3.10 · Single-node chart with privileged access to the node's container runtime, documented trade-offs.

---

## Decision log

Append entries as `YYYY-MM-DD — decision — reason — link to ADR/PR`.

- 2026-10-13 — Docker Engine API + gVisor (`runsc`) as the default runtime — ubiquitous for self-hosters, strong isolation without KVM — PRD §9
- 2026-10-13 — File operations and snapshots run inside the sandbox via exec — avoids gVisor overlay visibility issues and host-side symlink attacks — PRD FR-FS-4, R1
- 2026-10-13 — Exec helper `sbx-exec` for process-tree termination — Docker exec cannot signal processes — PRD §9.4
- 2026-10-13 — No `open` network mode — keeps the security story simple and defensible — PRD FR-NW-8
- 2026-10-13 — Project name **Arcana** (module `github.com/brendatamaar/arcana`, binary `arcana`) — chosen by owner — PRD header
- 2026-10-13 — Minimal read-only React dashboard moved from stretch into v0.3 — makes the demo understandable at a glance; read-only keeps CSRF out of scope until T4.4 — PRD §7.14
- 2026-10-13 — Dashboard uses cookie sessions limited to safe methods, SSE for live data — no API key in browser storage, `EventSource` works with cookies — PRD FR-WD-4…7

- 2026-10-31 — Owner approved replacing T0.4(d) docker cp/docker commit experiments with exec-only read/archive round trips — validates the supported transfer path while preserving AGENTS security rule 4 and PRD FR-FS-4 — PLAN T0.4, PRD R1

- 2026-10-31 — Temporary spikes/ module for Phase 0 programs; use the official github.com/moby/moby/client successor module — isolates throwaway dependencies from the production module; removed by T0.6 — T0.2

- 2026-11-01 — Configure explicit outbound DNS for the proxy and attach all runsc proxy interfaces before start; recreate it when interfaces change — T0.5 verified Docker embedded DNS and hot-attached runsc interfaces do not work in this setup; internal sandbox isolation remains unchanged — docs/spikes/004-network.md, PRD FR-NW-2 and §13

- 2026-11-01 — Accept ADRs 0001–0004 and remove the temporary spikes/ module — risk probes support Docker/runsc, helper bind mounts, exec-only transfer and internal-network egress; prototype helper and reusable integration fixtures remain for later hardening — docs/decisions/

## Open questions

Append entries as `YYYY-MM-DD — question — context — blocking task (if any)`.

- 2026-10-31 — Resolved: owner supplied and authorized an experimental VPS; SSH, Docker, runsc and GitHub push access verified. CI passed for T0.1.
- 2026-10-31 — Resolved by owner: replace T0.4(d) docker cp/docker commit experiments with byte-identical read/archive round trips through exec; no security exception is introduced.
