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

### [x] T1.1 Configuration
- **Deps:** T0.6 · **PRD:** §13, NFR-OPS-3
- **Do:** `internal/config` with koanf: defaults struct, YAML file, env overrides (`ARCANA_` + `__` separator), validation (ranges, CIDR parsing, defaults ≤ maximums, allowed roots absolute, runc guard). `--config` flag on root command. Ship `deploy/config.example.yaml` matching PRD §13 (v0.1 keys active, later keys commented).
- **Acceptance:** invalid configs produce clear, field-specific errors; env overrides work for nested keys.
- **Verify:** `go test ./internal/config/...` (table-driven: defaults, file, env, each validation rule)

> Done: Added koanf defaults/YAML/environment loading, field-specific validation, safe runtime/network guards, root --config flag and the versioned example configuration. go test ./internal/config/... and make check passed.

### [x] T1.2 Logging, IDs and API errors
- **Deps:** T1.1 · **PRD:** §9.6, §10.3, NFR-OPS-1, NFR-SEC-4
- **Do:** `slog` setup (json/text, level); request ID middleware; `internal/ids` (prefixed `crypto/rand` base32 IDs); `internal/apierr` with typed error codes mapped to HTTP status and the JSON error envelope; a redaction helper so API keys and env values never reach logs.
- **Acceptance:** every error code in PRD §10.3 has a constructor and status mapping; IDs match the regex `^(sbx|exe|snp|key|aud|ses|req)_[a-z2-7]{20}$`.
- **Verify:** `go test ./internal/ids/... ./internal/apierr/...`

> Done: Added cryptographic IDs, typed API errors/statuses/envelopes, request ID middleware and JSON/text slog configuration with redaction. Task verification and make check passed; new package coverage is 96–100%.

### [x] T1.3 Store
- **Deps:** T1.1 · **PRD:** §11, NFR-REL-3
- **Do:** `internal/store` using `modernc.org/sqlite` (WAL, `busy_timeout`, foreign keys on, single writer); goose migrations embedded with `embed.FS` and applied on open; sqlc config and queries for `api_keys`, `sandboxes`, `execs`, `meta` (snapshots and audit come later). Store `install_id` in `meta` on first open.
- **Acceptance:** fresh DB migrates; re-open is idempotent; queries generated by sqlc compile; `make verify-generated` detects stale sqlc output.
- **Verify:** `make generate && go test ./internal/store/...`

> Done: Embedded goose migration, pure-Go SQLite with per-connection WAL/foreign keys/5s busy timeout and one writer, atomic installation ID, pinned sqlc tool and bounded generated queries added. make generate, store tests, CGO_ENABLED=0 store tests and make check passed; stale-source probe correctly failed verify-generated; store coverage 75.5% excluding generated code.

### [x] T1.4 API keys and auth
- **Deps:** T1.2, T1.3 · **PRD:** FR-AU-1…5
- **Do:** `internal/auth`: key generation (`arc_live_` + 32 base62 chars), SHA-256 hashing, constant-time comparison, prefix display, `last_used_at` update (debounced), admin flag; bootstrap admin key on empty DB (print once, write `data_dir/admin.key` with `0600`); HTTP middleware putting the principal in context; `arcana keys create|list|revoke` operating on the local DB.
- **Acceptance:** revoked keys rejected; non-admin cannot call admin endpoints; bootstrap happens exactly once; key never appears in logs.
- **Verify:** `go test ./internal/auth/...`

> Done: Added unbiased base62 keys, SHA-256 storage and constant-time verification, immediate revocation with debounced usage timestamps, serialized one-time admin bootstrap, bearer/admin middleware and local keys CLI. Auth/CLI tests, bootstrap mutation/fuzz checks, make check and CGO_ENABLED=0 make build passed. Native absolute host data paths are accepted; sandbox roots retain POSIX validation. Bootstrap is wired into serve by T1.5.

### [x] T1.5 OpenAPI spec and HTTP server skeleton
- **Deps:** T1.2, T1.4 · **PRD:** §10
- **Do:** `api/openapi.yaml` (OpenAPI 3.0) covering all v0.1 endpoints, schemas and error envelope; `oapi-codegen` configured for std `net/http` server and models into `internal/api/gen`; `make generate` wires it; `internal/api` server with routing, auth middleware, request ID, panic recovery, JSON helpers, `/v1/health`; `arcana serve` starts the server with graceful shutdown (NFR-REL-2). Warn when listen address is not loopback (NFR-SEC-3).
- **Acceptance:** spec validates; generated code compiles; unimplemented handlers return 501 with the standard envelope; `serve` shuts down cleanly on SIGTERM.
- **Verify:** `make generate verify-generated && go test ./internal/api/...`

> Done: Validated v0.1 OpenAPI contract, pinned standard HTTP generation, auth/request IDs/body caps/recovery/JSON helpers, key endpoints and protected 501 stubs added. serve bootstraps admin and drains on cancellation/SIGTERM. make generate verify-generated, API tests and make check passed; Linux CGO_ENABLED=0 build, actual SIGTERM/private-key checks, auth/API race tests and retained integration/escape suites passed on the authorized VPS. API coverage 84.2%; web-check deferred by owner until T3.7.

### [x] T1.6 Runtime driver interface and fake driver
- **Deps:** T1.1 · **PRD:** §9.3
- **Do:** `internal/runtime` with `Driver`, `Spec`, `Instance`, `ExecRequest`, `ExecSession`, `ExitInfo`, `Stats`, `Capabilities`. `internal/runtime/fake`: in-memory driver that can script exec output, exit codes, delays and failures, for orchestrator unit tests.
- **Acceptance:** fake driver satisfies the interface with compile-time assertion; fake has its own tests.
- **Verify:** `go test ./internal/runtime/...`

> Done: Shared driver/session/spec/resource types and typed runtime errors added. The concurrency-safe fake scripts streams, exits, failures and channel-controlled delays, bounds recorded data/instances/scripts, clones mutable inputs and cancels sessions on destroy. Runtime race tests, make check and VPS integration/escape suites passed.

### [x] T1.7 Docker driver
- **Deps:** T1.6 · **PRD:** FR-RL-1, FR-RL-2, FR-RL-3, §9.5, FR-DR-3
- **Do:** `internal/runtime/docker` using the official Docker Engine Go client: create + start with runtime from config, resource limits (NanoCPUs, Memory, MemorySwap = Memory, PidsLimit, ulimits), `CapDrop: ALL`, `SecurityOpt: no-new-privileges`, `ReadonlyRootfs`, `User: 1000:1000`, tmpfs `/tmp` and `/run/sbx` with size limits, workspace volume or tmpfs, `NetworkMode: none`, helper bind mount (read-only), labels and names; init process keeping the container alive (`--init` + `sleep infinity` or equivalent); exec with demuxed streams or TTY; resize; destroy (container, then volume; idempotent; ignores not-found); list by labels; stats.
- **Acceptance:** integration tests (build tag `integration`) create, exec, destroy a sandbox under runsc when available and runc otherwise; all hardening flags asserted via container inspect.
- **Verify:** `go test ./internal/runtime/docker/... && make test-integration`

> Done: Official Moby driver enforces OCI opt-in, all hardening/resource flags, managed ownership, bounded streaming/status polling, safe partial cleanup and idempotent container/volume destruction. Unit race tests and make check passed; VPS tests inspected actual flags and verified UID/workspace, readonly root, TTY resize, tmpfs ENOSPC, peer health and cleanup under runsc and explicit runc with both backends; full integration/escape suites passed. Unit coverage 79.4%. Minimal arcana-base:local Dockerfile seeds UID 1000 workspace ownership until T2.1 publishes official images.

### [x] T1.8 `sbx-exec` helper
- **Deps:** T0.3, T1.7 · **PRD:** §9.4, FR-EX-4
- **Do:** Production version of `cmd/sbx-exec` from the spike: `run`, `kill`, `status`; status JSON in `/run/sbx`; real command exit codes in status, with transport exits 0/124 for completed success/failure and 125 for helper faults; TTY mode handling; no dependencies beyond stdlib. Build it in `make build` before Arcana, embed the binary with `embed.FS` (`internal/helper`), and write it to `data_dir/bin/sbx-exec` (mode 0755, checksum-verified) at `serve` start.
- **Acceptance:** unit tests for argument parsing and status; integration test: child processes that ignore SIGTERM are killed on timeout and on `kill`, under runsc.
- **Verify:** `go test ./cmd/sbx-exec/... && make test-integration`

> Done: Production subreaper/pidfd controller reaps detached descendants before atomic completion, separates transport from actual exit codes, bounds state and cancellation, and embeds reproducible static amd64/arm64 helpers with checksum-verified atomic installation. Parser/status/core tests, mutation proof, 101125 fuzz executions, make check, Linux lint, full integration/escape suites and static build passed. Real serve verified helper checksum/mode, private admin key, health and SIGTERM. Linux helper coverage 70.6%; protocol 100%. tmpfs workspace execution verified under runsc/runc.

### [x] T1.9 Orchestrator: lifecycle
- **Deps:** T1.3, T1.6 · **PRD:** FR-LC-1…4, FR-LC-6, FR-LC-9, FR-TP-1
- **Do:** `internal/orchestrator`: resolve template and merge overrides (clamped to maximums), quota checks, create via driver, persist state transitions, get/list with ownership filtering, idempotent destroy, keepalive, activity tracking. Wire handlers for sandbox endpoints.
- **Acceptance:** unit tests with fake driver cover state machine, quotas, clamping, ownership (non-admin gets 404 for others' sandboxes), driver failure → `failed` with reason.
- **Verify:** `go test ./internal/orchestrator/... ./internal/api/...`

> Done: Added pure lifecycle decisions, atomic quota reservations and transition history, template/env/resource/TTL resolution, ownership-filtered state/label pagination, idempotent destroy and TTL-preserving activity. Sandbox HTTP handlers and production Docker wiring are active. Fake-driver/API race tests, terminal-state mutation proof, 2866 fuzz executions, Windows and Linux make check, full integration/escape suites, real runsc lifecycle and static build/serve checks passed.

### [x] T1.10 Sync exec
- **Deps:** T1.8, T1.9 · **PRD:** FR-EX-1…3, FR-EX-5, FR-EX-6, FR-EX-8, FR-EX-9
- **Do:** Exec manager that wraps commands with `sbx-exec run`, enforces timeout defaults and maximums, captures output with a combined cap (keeps draining after the cap), reads final status via `sbx-exec status`, records the `execs` row, enforces per-sandbox concurrency, and starts a host-side hard deadline (timeout + 10 s → destroy sandbox with `exec_deadline`). `cmd` vs `shell` validation. UTF-8 sanitising of output.
- **Acceptance:** unit tests with fake driver for caps, timeouts, concurrency, deadline; integration test runs `python3 -c`/`sh -c` style commands with expected exit codes.
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

> Done: Added supervised sync exec with strict argv/shell/path validation, real exit metadata, combined capped capture that keeps draining, UTF-8 sanitization, per-sandbox concurrency, independent host/cancellation deadlines and fail-closed destruction on uncertain cleanup. Every accepted exec records timing/counts without output content. HTTP auth/ownership and fake-driver tests, two guard mutations, 36094 fuzz executions, Windows/Linux make check, full integration/escape suites, runsc argv/shell/timeout/cancellation with peer health, static build and serve checks passed. Fixed procfs ESRCH disappearance handling without weakening cleanup proof; the runsc kill attack passed ten repetitions.

### [x] T1.11 Streaming exec (WebSocket)
- **Deps:** T1.10 · **PRD:** FR-EX-7, §10.4
- **Do:** `internal/stream` using `coder/websocket`: protocol messages exactly as PRD §10.4, start-first validation, stdin and stdin_close, TTY + resize, cancel via `sbx-exec kill`, cancel on client disconnect, backpressure with 1 MB send buffer, ping/pong keepalive, close after `exit`.
- **Acceptance:** tests with an in-process server and fake driver for every message type, disconnect handling and backpressure; integration test streams output of a command that prints over time.
- **Verify:** `go test ./internal/stream/... && make test-integration`

> Done: Added strict start-first WebSocket protocol, binary-safe live output/stdin, stdin close, TTY/resize, cancellation/disconnect cleanup, 1 MiB bounded backpressure, frame/admission caps, ping/pong and terminal close. Streaming reuses exec proof/quotas/records without retaining output; shutdown drains accepted streams before driver/store closure. In-process fake-driver tests cover all messages, limits, slow readers, ordering and shutdown; two guard mutations and 77798 fuzz cases passed. Windows/Linux make check, full integration/escape suites, real runsc stdin-gated live output/exit, static build and serve checks passed. Console dimensions are forwarded only for TTY execs.

### [x] T1.12 Files API
- **Deps:** T1.10 · **PRD:** FR-FS-1…5
- **Do:** Path validation (absolute, cleaned, under `allowed_roots`, reject NUL and `..` escape, including URL-encoded forms decoded by the router); operations through exec inside the sandbox: write (stdin → a stdlib-only `sbx-exec write` subcommand via argv, creating parent dirs; shared path validation before use), read (with size check first via `stat`), list (`find` with depth or a small helper subcommand of `sbx-exec` producing JSON — prefer the helper for consistent output), delete, archive upload (`tar -xzf - -C path`) and download (`tar -czf - -C path .`), size caps enforced while streaming.
- **Acceptance:** path validation table tests; integration tests for each operation; symlink in workspace pointing to `/etc/passwd` returns the **sandbox's** file, never the host's.
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

> Done: Added shared lexical path validation, sandbox-local argv-only file utilities, atomic bounded writes, descriptor-sized binary reads, bounded listings, deletion and opaque tar upload/download through supervised exec. Auth/ownership, encoded traversal, binary/limit and malformed metadata tests passed; containment mutation failed as expected and 255703 fuzz inputs passed. Windows/Linux make check, full runsc integration/escape suites, static build and serve probe passed. Coverage: path validator 100%, API 88.4%, orchestrator 83.7%.

### [x] T1.13 Reaper, watchdog and reconciliation
- **Deps:** T1.9, T1.10 · **PRD:** FR-LC-5, FR-LC-7, FR-LC-8, FR-RL-3, NFR-REL-1
- **Do:** Background loops with context cancellation: reaper (TTL, idle), workspace watchdog (`du` via exec for `volume` backend at the configured interval → destroy with `disk_quota`), startup reconciliation by labels (containers, volumes; networks once they exist), marking lost sandboxes `failed/lost`. Use an injectable clock for tests.
- **Acceptance:** unit tests with fake clock and fake driver; integration test: kill Arcana mid-exec, restart, orphans removed (ESC-20).
- **Verify:** `go test ./internal/orchestrator/... && make test-integration`

> Done: Added independent context-owned TTL/idle reaper and supervised bounded volume watchdog without idle activity updates, pre-listener reconciliation with installation-scoped orphan cleanup, interrupted-exec fail-closed recovery, and optional destroy_on_shutdown. Fake-clock/driver boundary, quota, recovery and cleanup-failure tests passed. Actual runsc daemon SIGKILL/restart test (integration and escape) verifies lost cleanup, orphan volume/network removal, NULL unknown exit codes, peer/API/host health and graceful shutdown. Windows/Linux make check, full Linux integration/escape targets and static build passed.

### [x] T1.14 Go client
- **Deps:** T1.11, T1.12 · **PRD:** FR-SD-1
- **Do:** `pkg/client`: typed methods for all v0.1 endpoints, streaming exec helper (`ExecStream` returning readers and an exit result), typed errors carrying API error codes, context support, configurable HTTP client. Godoc examples.
- **Acceptance:** tests against an `httptest` server running the real API with fake driver.
- **Verify:** `go test ./pkg/client/...`

> Done: Added documented public Go client for every v0.1 endpoint, typed API errors, injected HTTP transport, context cancellation, bounded JSON/binary transfers, pagination and streaming exec readers/stdin/TTY resize/cancel/exit. Real API plus fake-driver tests cover lifecycle, keys, files, ownership and stream completion; protocol tests cover controls, invalid terminal frames, disconnects, limits and blocked-reader cleanup. Fixed the HTTP file-body EOF/read-deadline cancellation race discovered by these tests, retaining interruption of unfinished input. Windows/Linux make check, full runsc integration/escape suites and static build passed; client coverage 86.0%.

### [x] T1.15 CLI
- **Deps:** T1.14 · **PRD:** FR-CL-1, FR-CL-2
- **Do:** Client subcommands via `pkg/client`: `sandbox create|list|get|rm`, `exec <id> -- argv` (streams; exits with remote code; `--tty` when stdin is a terminal), `files put|get|ls`. Human-readable tables by default, `--json` for scripting. `--server`/`--api-key` flags and env vars.
- **Acceptance:** CLI tests using the client against an in-process server; `arcana exec` returns the remote exit code.
- **Verify:** `go test ./cmd/arcana/...`

> Done: Added client-backed sandbox create/list/get/rm, streaming argv exec, files put/get/ls, tables/JSON, server/key environment flags, opt-in stdin and Linux TTY raw-mode restoration. Context cancellation joins stream/input readers; named downloads use exclusive 0600 creation. In-process CLI tests, child-process exit-7 assertion and Linux pseudoterminal restoration test passed. Actual daemon/runsc integration verifies CLI exit 7 and binary put/get. Windows/Linux make check, full runsc integration/escape suites and static Linux build passed.

### [x] T1.16 Doctor
- **Deps:** T1.7, T1.3 · **PRD:** FR-DR-1…3
- **Do:** `internal/doctor` with independent checks returning pass/warn/fail plus a fix hint (including gVisor install steps); `arcana doctor [--json]`; `serve` runs blocking checks at startup and refuses to start on failure.
- **Acceptance:** unit tests with injectable system probes for each check; manual run documented in the PR.
- **Verify:** `go test ./internal/doctor/... && go run ./cmd/arcana doctor`

> Done: Added independent injectable doctor probes, table/JSON diagnostics and fix hints, Linux/kernel/cgroups/Docker/storage/data/SQLite/image/KVM checks, hardened runsc/helper proof with bounded cleanup, explicit runc warning/proof policy, and pre-listener startup gating after reconciliation. Unit tests cover each failure class, runtime registration, missing images, cancellation and output caps; actual daemon tests prove doctor failure and serve refusal on missing prerequisites. Windows/Linux make check, full runsc integration/escape suites and static build passed. Manual VPS doctor JSON exited successfully with only optional KVM warning; details in docs/doctor-validation.md. Doctor unit coverage 75.1%.

### [x] T1.17 Escape suite v1 and CI with runsc
- **Deps:** T1.12, T1.13 · **PRD:** §12.4 (v0.1 rows), NFR-SEC-2
- **Do:** `test/escape` (build tag `escape`) implementing ESC-01…14, ESC-20, ESC-21 through the public API. CI job installs gVisor from its apt repository, runs `sudo runsc install`, restarts Docker, then runs `make test-integration test-escape` against runsc.
- **Acceptance:** all v0.1 escape tests pass in CI on runsc; each test asserts host/Arcana health after the attack.
- **Verify:** `make test-escape` locally with runsc; CI green.

> Done: Added public REST/WebSocket ESC-01–14 and ESC-21 with bounded adversarial probes, setuid fixture validation, synchronized CPU/peer timing, PID cgroup-denial evidence without OOM, binary/symlink/path containment, and peer/API/Docker health checks after every attack. Moved actual SIGKILL/restart ESC-20 into test/escape. CI installs the official apt runsc package, registers/restarts Docker, requires runsc, runs integration/escape, and lints security-tagged tests. Fixed cold-cache Docker fixture ID parsing by separating stdout from pull-progress stderr, without changing security assertions. Windows/Linux make check, full runsc integration/escape, tagged lint and static build passed. GitHub check/security jobs passed candidate 36708f9 (https://github.com/brendatamaar/arcana/actions/runs/41578117012); final documentation-only amendment is rechecked in CI. Details: docs/escape-v1.md.

### [x] T1.18 v0.1 docs and release
- **Deps:** T1.15–T1.17
- **Do:** README: what it is, security model summary, quickstart (`go install`, gVisor setup, `doctor`, `serve`, first exec), CLI examples, limitations (PRD §12.5). `CHANGELOG.md` entry. Tag `v0.1.0`.
- **Acceptance:** following the README on a clean Ubuntu VM reaches a first exec.
- **Verify:** `make check test-integration test-escape`

> Done: Replaced the scaffold README with v0.1 security scope, Ubuntu/gVisor setup, go install, Doctor/serve/first-exec flow, CLI/API examples and honest limitations; consolidated CHANGELOG into the dated v0.1.0 release. Added verbatim marked-README execution with isolated private demo state and bounded cleanup. VPS smoke reached first exec and file round trip; fresh Ubuntu 24.04 GitHub VM quickstart and check/security jobs passed candidate f8c400f (https://github.com/brendatamaar/arcana/actions/runs/41587693910). Windows/Linux make check, full runsc integration/escape and static build passed. The final release commit is the target of annotated tag v0.1.0 after final CI verification; published-module installation is checked after tag push. Validation: docs/release-validation.md. Phase 1 ends here; no blocked tasks or unresolved Phase 1 questions remain.

---

## Phase 2 — v0.2 Self-hosting ready

### [x] T2.1 Official templates
- **Deps:** T1.18 · **PRD:** FR-TP-2, FR-TP-4, FR-RL-4
- **Do:** `templates/{base,python,node,go}/Dockerfile`: slim bases, UID 1000 user `sandbox`, `tar`, `du`, `find`, `coreutils`, `git`, `ca-certificates`; env so package managers install into `/workspace` (venv at `/workspace/.venv` created lazily by a profile script or documented usage, `npm_config_cache=/workspace/.npm`, `GOPATH=/workspace/.go`, `GOCACHE=/workspace/.cache/go`). CI workflow builds multi-arch images and pushes to GHCR on tags; smoke test each image under runsc.
- **Acceptance:** each template runs `python -m pip install requests` / `npm i lodash` / `go mod download` successfully into `/workspace` with allowlist networking (after T2.4) or offline smoke tests before it.
- **Verify:** `make templates && make test-integration`

> Done: Added slim base/Python/Node/Go images with UID/GID 1000 and workspace package-manager paths, documented lazy Python venv setup, make templates, offline hardened runsc integration smokes, and multi-arch GHCR release builds gated on smoke tests. make templates, make check, make test-integration and make test-escape passed locally.

### [x] T2.2 Template registry endpoint
- **Deps:** T2.1 · **PRD:** FR-TP-3
- **Do:** Config-driven registry, `GET /v1/templates`, validation that template images exist at startup (warn, not fail).
- **Verify:** `go test ./internal/orchestrator/... ./internal/api/...`

> Done: Added authenticated GET /v1/templates, generated contract and Go client, resolved defaults with environment key names only, independent registry metadata and a tested 1000-template cap. Existing startup Doctor already warns for missing configured images. Task verification and make check passed.

### [x] T2.3 Warm pool
- **Deps:** T2.2 · **PRD:** FR-LC-10
- **Do:** Per (template, network mode) pool with target size; background filler with backoff on errors; acquire applies labels and records `source: pool`; pool members labelled and reconciled; pool drained on shutdown when `destroy_on_shutdown`; metrics hooks.
- **Acceptance:** unit tests for filler/acquire races (`-race`); integration test shows faster acquire than cold create.
- **Verify:** `go test -race ./internal/orchestrator/... && make test-integration`

> Done: Added bounded fresh-instance pools for configured template/network defaults, serialized filler with capped backoff, immutable-setting cold fallback, quota-safe idle-member eviction, source=pool reservations, runtime pool labels, restart orphan reconciliation, shutdown drain and PoolAvailable metrics hook. Race tests cover acquire/fill, capacity, fallback and cleanup. runsc integration measured 54ms acquire versus 2.09s cold; make check, required race tests, full integration and escape suites passed.

### [x] T2.4 Allowlist networking and egress proxy
- **Deps:** T0.5, T2.2 · **PRD:** FR-NW-2…7, FR-CL-3
- **Do:** `internal/netpolicy`: subnet allocator from `subnet_pool` (persisted allocations or derived from Docker state on start), per-sandbox internal network creation and cleanup, proxy container lifecycle (`arcana proxy` using the Arcana image), connecting the proxy to each sandbox network, injecting `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`. Proxy server: `CONNECT` and plain HTTP, per-source-IP allowlist pushed from Arcana (control channel on a local socket or HTTP with a shared secret), DNS resolution then IP range checks, connect to resolved IP, byte counting, decision events back to Arcana. Presets from config. Doctor check for subnet overlap.
- **Acceptance:** unit tests for allocator and IP range checks (IPv4 and IPv6); integration tests for allowed and denied hosts.
- **Verify:** `go test ./internal/netpolicy/... && make test-integration`

> Done: Added config/API/CLI allowlists and presets, Docker-derived bounded subnet allocation, isolated per-sandbox runsc proxies, pinned public-IP HTTP/CONNECT forwarding, exec-bridged authenticated Unix control, bounded decision queues, startup restoration/orphan cleanup and subnet diagnostics. Live tests installed requests/lodash/Go modules, denied non-allowlisted/direct egress, preserved peer health and restored access after reconciliation. Network fixtures use separate ranges to keep the new overlap diagnostic meaningful. make proxy-image, task verification, make check and full integration passed. ESC-03 initially failed during concurrent package execution; it passed in isolation and the full escape suite passed with GOFLAGS=-p=1, with every attack and assertion unchanged. Owner-approved durable audit wiring follows in T2.5.

### [x] T2.5 Audit log
- **Deps:** T2.4 · **PRD:** FR-AD-1, FR-AD-2, FR-FS-6
- **Do:** Migration for `audit_events`; async buffered writer (never blocks request path; drops with a counter metric if the buffer overflows); events from all listed actions; `GET /v1/audit` with filters and cursor pagination; daily pruning.
- **Verify:** `go test ./internal/audit/... ./internal/api/...`

> Done: Added bounded asynchronous SQLite audit persistence, credential redaction, action/egress attribution, admin filters and timestamp/ID cursors, startup/daily pruning, local-key CLI recording and Go client support. File size metadata is produced inside the sandbox. Audit/API verification, make check, tagged security lint and full serial integration/escape suites passed (audit coverage 86.5%). Owner-approved ESC-03 cgroup usage/quota/throttling checks preserve the attack, CPU ceiling and peer-health bounds; no resource defaults changed.

### [x] T2.6 Metrics, dashboard and Compose
- **Deps:** T2.3, T2.5 · **PRD:** §15, NFR-OPS-2
- **Do:** `internal/metrics` with all PRD §15 metrics on `metrics_listen`; `deploy/grafana/arcana.json`; `deploy/compose/docker-compose.yml` with Arcana (host Docker socket mounted), Prometheus and Grafana with provisioning.
- **Acceptance:** `docker compose up` shows populated dashboard after running a few execs.
- **Verify:** `go test ./internal/metrics/...`; manual Compose check noted in PR.

> Done: Added all PRD collectors plus audit-loss counters on the separately owned metrics listener, bounded route/outcome labels, live SQL/pool gauges and middleware preserving WebSocket/shutdown behavior. Added digest-pinned Prometheus/Grafana Compose provisioning and a temporary-stack smoke script; real exec samples reached Prometheus and Grafana datasource/dashboard queries. Metrics verification (87.8% coverage), make check, full integration and escape suites passed. Validation: docs/compose-validation.md.

### [x] T2.7 Quotas and rate limiting
- **Deps:** T2.3 · **PRD:** FR-AU-6
- **Do:** Per-key token bucket for sandbox creation; `rate_limited` errors with `Retry-After`.
- **Verify:** `go test ./internal/api/...`

> Done: Added configurable per-key creation token buckets (default 30/min), continuous refill, bounded fail-closed 10000-key state with idle reclamation, rate_limited/Retry-After responses and Go client retry-delay metadata without mutation retries. Tests cover independent keys, refill/backwards clocks, concurrent admission, state bounds, auth precedence and no DB/runtime reservation on denial. Task verification, make check, full serial integration and escape suites passed. User-requested stopping point reached; T2.8 onward remains unstarted.

### [x] T2.8 Escape suite v2 (network)
- **Deps:** T2.4 · **PRD:** §12.4 ESC-15…19
- **Do:** Implement network escape tests; add to CI.
- **Verify:** `make test-escape`

> Done: Added public REST/WebSocket attacks for ESC-15–19 with real runsc proxies, persisted denial audits, a controlled DNS upstream changing a public A answer to loopback, and a live peer listener verified on its own interface. Each attack checks API, peer and Docker health. CI runs the full v1/v2 suite. make check, tagged escape lint, and full serial integration/escape suites passed.

### [x] T2.9 Releases and installers
- **Deps:** T2.6
- **Do:** `.goreleaser.yaml` (linux amd64/arm64, `CGO_ENABLED=0`, checksums, `.deb`/`.rpm` via nfpm, Arcana container image to GHCR, changelog); release workflow on tags; `scripts/install.sh` (POSIX sh, arch detection, checksum verification, config install, optional systemd unit, runs `doctor`); `deploy/systemd/arcana.service` with hardening directives compatible with Docker socket access. Test `install.sh` with `shellcheck` in CI.
- **Acceptance:** a pre-release tag produces all artifacts; install script works on a fresh Ubuntu VM.
- **Verify:** `goreleaser release --snapshot --clean` locally; `shellcheck scripts/install.sh`

> Done: Added `.goreleaser.yaml` (linux amd64/arm64 static builds, checksums, deb/rpm, GHCR image via `dockers_v2`), tag release workflow, CI shellcheck and snapshot jobs, `scripts/install.sh` and a hardened `arcana.service`. Verified locally: snapshot release produced all artifacts; `shellcheck` clean; install.sh in a fresh Ubuntu 24.04 container installed and rejected a tampered archive; the deb installed the user/data dir. **Not verified:** a real pre-release tag, GHCR push, and `--systemd`/doctor on a real systemd VM; both run in the release workflow (`install-smoke`) on the first tag, which is T2.11.

### [x] T2.10 Docs for self-hosters
- **Deps:** T2.9
- **Do:** `docs/` pages: install (each channel), configuration reference (generated from the config struct or hand-maintained and checked by a test that every key is documented), security model and limitations, networking, operations (upgrades, backups of `data_dir`, logs, metrics).
- **Verify:** docs build (see T3.8) or Markdown lint in CI.

> Done: Added `docs/install.md`, `configuration.md`, `security.md`, `operations.md` (networking already existed). `TestEveryConfigKeyIsDocumented` walks the config struct's koanf tags and fails on any undocumented key. Markdown lint (`markdownlint-cli2`, config in `.markdownlint-cli2.yaml`) is clean locally and runs in CI. The config loader ignores unknown keys; documented as such rather than changed. README still describes allowlist networking, metrics and installers as later work; T2.11 updates it.

### [x] T2.11 v0.2 release
- **Deps:** T2.1–T2.10
- **Do:** Update README and CHANGELOG; tag `v0.2.0`.
- **Verify:** `make check test-integration test-escape`; release workflow green.

> Note: CI first failed in `TestAllowlist_ProxyAllowedDeniedAndIsolation` after `Reconcile`. Cause: Docker 28.0.4 (runner) ignores `MacAddress` on `NetworkConnect`, so the recreated proxy got a new MAC and sandbox neighbour caches went stale (Docker 29 locally honours it). Fixed by setting the stable MAC/IP in the container-create `NetworkingConfig` (with the `bridge` endpoint listed explicitly) and failing closed if Docker does not apply them. README/CHANGELOG updated. The first tag run failed because the release job lacked golangci-lint (fixed in 15a49e8); the unreleased tag was moved to that commit. Release `v0.2.0` published with archives, deb/rpm, checksums and install.sh; Release (including install-smoke), CI and Template images workflows green on the tag. Done.

---

## Phase 3 — v0.3 Agent ecosystem and showcase

### [x] T3.1 Snapshots, restore and fork
- **Deps:** T2.11 · **PRD:** FR-SN-1…5
- **Do:** Migration for `snapshots`; snapshot by streaming `tar -czf - -C /workspace .` through exec into `data_dir/snapshots/<id>.tar.gz` with SHA-256; `from_snapshot` create path (extract inside sandbox); fork N in parallel with `errgroup` and partial-failure reporting; limits; endpoints; Go client and CLI support (`arcana snapshot create|list|rm`, `arcana fork`).
- **Acceptance:** integration test: install a package into `/workspace`, snapshot, fork 3, package present in each, changes in one fork invisible to others.
- **Verify:** `make test-integration`

> Done: Added the `snapshots` migration/queries, `snapshots.*` config, OpenAPI endpoints (create/list/get/delete/fork, `from_snapshot`), orchestrator snapshot/restore/fork, Go client, and `arcana snapshot create|list|rm`, `arcana fork`, `sandbox create --from-snapshot`. Archives are streamed through exec into `data_dir/snapshots` (0600) with SHA-256, verified again on the streamed descriptor before in-sandbox extraction; count/byte budgets are reserved before streaming; failed restores destroy the sandbox (`restore_failed`); startup reconciliation prunes `.part`/unrecorded files. Fork reports per-index failures, charges one creation token per member and cleans up on disconnect. A unit test found a real race (exec transport closing stdin while the copy loop was still reading an `os.File`), fixed in `archiveInput`. Integration test on real runsc installs a package into a `/workspace` venv, snapshots, forks 3, checks the package in each, isolation between forks, corrupt-archive rejection and deletion. Hash-check mutation was caught by tests. make check, full serial make test-integration and make test-escape passed. Fork/list/get request shapes were not specified in PRD §10; defined there and in the Decision log. `web-check` not applicable before T3.7.

### [x] T3.2 MCP server
- **Deps:** T3.1 · **PRD:** FR-MC-1…3, FR-CL-4
- **Do:** `internal/mcp` with the official MCP Go SDK, stdio transport, tools listed in PRD, session labelling and cleanup. Docs with a config snippet for MCP clients (for example Claude Desktop or Claude Code).
- **Acceptance:** tests using the SDK's in-memory client transport for each tool; manual test with a real MCP client noted in PR.
- **Verify:** `go test ./internal/mcp/...`

> Done: Added `internal/mcp` (official Go SDK v1.8.0) with the eight PRD tools, limits stated in descriptions, bounded replies (64 KiB per stream, 1 MiB file reads/writes), Arcana error codes without request internals, and `mcp_session=<id>` labelling with cleanup of unkept sandboxes/forks when the session ends. `arcana mcp` serves stdio (frame cap 4 MiB; logs to stderr). Tests use the SDK's in-memory client transport for every tool, a CLI pipe test, and a runsc integration test drives all tools through the real API (session label, snapshot/fork, cleanup). Docs and client config snippets in docs/mcp.md. **Not done:** a manual run with a real third-party MCP client (Claude Desktop/Code) — none is available in this environment; the SDK's own client was used instead.

### [x] T3.3 Python and TypeScript SDKs
- **Deps:** T3.1 · **PRD:** FR-SD-2
- **Do:** `sdk/python` and `sdk/typescript` generated from `api/openapi.yaml` (choose generators and record in an ADR), plus hand-written streaming exec wrappers and README examples; CI job running each SDK's tests against a live Arcana with runc.
- **Verify:** SDK test commands documented in each SDK README; CI green.

> Done: Added `sdk/typescript` (openapi-typescript + openapi-fetch, `ws` for streaming; Node >=18; `node --test`) and `sdk/python` (openapi-python-client 0.30.0 pinned with ruff, httpx2/attrs at runtime, `websockets` for streaming; Python >=3.11; `unittest`), each a generated layer plus a hand-written wrapper for exec (argv/shell, stdin), WebSocket streaming exec (binary-safe, stdin framing, resize, cancel), raw file/archive transfer, snapshot/fork (partial failures returned), admin and typed errors. Unit tests run against in-process fake HTTP/WebSocket servers and found two real bugs (unhandled rejection on a pre-start error frame; output lost between `started` and handler registration). `scripts/with-arcana.sh` starts a throwaway Arcana; the live tests of both SDKs passed against it on runsc. `make sdk-generate`/`sdk-verify-generated` (idempotent, verified) and `make sdk-test` added; ADR 0005 records the choices; the owner approved the dependencies. **Not verified:** the new CI `sdk` job (runc, Node 22) has not run on GitHub yet, and the SDKs were tested locally on Node 18 and Python 3.12 only. The Python generator omits the `application/gzip` archive endpoints, which the wrapper implements by hand.

### [x] T3.4 Reference agent and fixtures
- **Deps:** T3.2 · **PRD:** FR-SD-3, FR-SD-4
- **Do:** `examples/fix-agent` (Go): OpenAI-compatible chat client (base URL, model, key from env; default `http://localhost:11434/v1` for Ollama), tool loop with `run_command`, `read_file`, `write_file`, `list_files`; step and token budgets; stops when hidden tests pass. `--parallel N` mode using snapshot + fork. `examples/fixtures/` with 10 small buggy projects and hidden tests. `make eval-agent` prints a results table.
- **Acceptance:** agent runs end-to-end on at least one fixture with a local model; results recorded in `docs/agent-results.md` including failures.
- **Verify:** `go test ./examples/fix-agent/...`; manual eval run.

> Done, with one acceptance item **still open**: the agent has not been run with a real local model, and `docs/agent-results.md` records no model results (none exist). The owner chose to build everything and leave that run pending (2026-11-12); see Open questions. Built: `examples/fix-agent` (OpenAI-compatible client with tool calling, tool loop with step/token budgets and bounded tool output, hidden-test judge that places and removes hidden tests between turns, `--parallel N` snapshot+fork mode with first-pass cancellation and guaranteed cleanup, `run`/`eval`/`verify-fixtures`), 10 fixtures (4 Python, 3 Node, 3 Go) and `make eval-agent`. Unit tests use a scripted model and fake Arcana; integration tests on runsc verified all 10 fixtures (visible and hidden fail before, pass with the reference solution) and ran the agent end to end against a *scripted* OpenAI-compatible server (3 fixtures single-attempt, 1 in parallel mode, nothing left behind). Verification found that `/tmp` is noexec, so Go fixtures set `GOTMPDIR` (noted in templates/README.md). Hidden tests are never in the model's view.

### [x] T3.5 Benchmark harness
- **Deps:** T3.1 · **PRD:** §8.2, FR-CL-5
- **Do:** `arcana bench` scenarios: cold create, pool acquire, exec overhead, density (create until limits), snapshot and fork times; outputs JSON and Markdown; `docs/benchmarks.md` with results versus targets and host details; charts generated by a small script.
- **Verify:** `go run ./cmd/arcana bench --scenario all --out bench/results`

> Done: Added `internal/bench` (cold-create, pool-acquire, exec-overhead, density, snapshot/restore/fork; nearest-rank percentiles; PRD §8.2 verdicts; guaranteed cleanup) and `arcana bench` writing results.json and results.md, `scripts/bench_charts.py` (stdlib SVG), and `docs/benchmarks.md` with a real run against a live Arcana on runsc. Unit tests use a fake clock and API. Results on this 2 vCPU/1.9 GB shared VM (not the reference host): cold create p50 206 ms, pool acquire p50 5.6 ms, exec p50 94 ms (narrowly met), all within target. **Density was not measured to its target:** capped at 12 sandboxes to protect the VM, so the 20-sandbox target is neither confirmed nor refuted; the harness reports capped runs as "not measured" rather than a miss. Single run, small samples; a rerun on the reference host is still needed for published claims. A first run's `results.json` was hand-patched and discarded; the recorded one is a clean rerun.

### [x] T3.6 Dashboard backend support
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

> Done: Added the `sessions` migration and queries, cookie sessions in `internal/auth` (hashed tokens, TTL, revoked-key invalidation, 20 per key; pure `decideCookie` policy: GET/HEAD and logout only, same-origin proof via Origin/Sec-Fetch-Site, bearer wins over cookie), `POST /v1/sessions` and `DELETE /v1/sessions/current`, `internal/events` (bus with resume, reset and slow-subscriber drop; bounded in-memory output rings with retention and reader caps), orchestrator publishing of lifecycle, exec and pool events plus ring feeding, SSE `/v1/events` and exec output tail, `/v1/overview` and exec history, OpenAPI/generated code, Go client (`Events`, `ExecOutput`, `ListExecs`, `GetOverview`), live `ui_sessions_active`/`event_subscribers` gauges, `ui.*` and `limits.exec.tail_*` config with docs, and docs/events.md. ESC-22 and ESC-23 pass on runsc through the public API; a mutation that let cookies authorize any method failed both them and the unit tests. Sandbox output is never written to disk. The work also found and fixed a latent bug: `exec.started` audit events for shell execs were silently dropped (the raw shell string was embedded as invalid JSON); regression test added. Verified: make check, and the full serial `make test-integration` and `make test-escape` suites passed. Not done: session create/logout methods in the Go client (browser-only).

### [x] T3.7 Dashboard frontend (React)
- **Deps:** T3.6 · **PRD:** FR-WD-1…3, FR-WD-9…11, ESC-24, §8.2
- **Do:**
  - `web/`: React + TypeScript + Vite app managed with pnpm. Typed API client generated from `api/openapi.yaml` with `openapi-typescript` and called through `openapi-fetch`. TanStack Query for server state, React Router for routes under `/ui`, Tailwind CSS for styling.
  - Pages per FR-WD-2: Login, Overview, Sandboxes, Sandbox detail. Live updates through `EventSource` on `/v1/events`, with reconnect and query-cache updates; full refetch on `reset`.
  - Log viewer: subscribes to the exec output stream, parses ANSI colours into styled spans (never HTML), virtualised rendering for large output, auto-scroll that pauses when the user scrolls up.
  - Vite builds straight into `internal/webui/dist/` (git-ignored except `.gitkeep`). `internal/webui` embeds it, serves `/ui` with the FR-WD-9 headers and SPA fallback, and shows the FR-WD-10 placeholder when the build is absent.
  - Make targets `web`, `web-dev` (Vite dev server proxying `/v1` to a local Arcana), `web-generate`, `web-check`, `web-e2e`, `build-all`. CI job for the web checks; GoReleaser `before` hook runs `make web`.
- **Acceptance:** Vitest + React Testing Library tests for each page against mocked API responses (MSW), including ESC-24; Go tests for `internal/webui` headers, fallback and placeholder; Playwright smoke test against a real Arcana in CI (runc) that logs in, sees a new sandbox appear and watches an exec's output live with zero CSP violations; initial JavaScript ≤ 200 KB gzip.
- **Verify:** `make web-check && make build-all && go test ./internal/webui/... && make web-e2e`

> Done: Added `web/` (React 19, TypeScript strict, Vite 7, Tailwind 4, React Router 7, TanStack Query and Virtual, generated `openapi-fetch` client) with Login, Overview, Sandboxes (filters, pagination, live patching) and Sandbox detail (spec, TTL/idle countdowns, state history, command history, live ANSI log viewer that is virtualised, memory-bounded and pauses auto-scroll when the user scrolls up); `internal/webui` (embedded, FR-WD-9 headers verbatim, SPA fallback, hashed-asset caching, placeholder when built without the frontend); `/ui` mounted outside auth; `GET /v1/sandboxes/{id}/history` (needed by FR-WD-2, see Decision log); Make targets `web`, `web-dev`, `web-generate`, `web-check`, `web-e2e`, `build-all`; a CI `web` job; a GoReleaser `make web` hook with Node in the release and snapshot workflows. 69 Vitest/RTL/MSW tests incl. ESC-24 for the log viewer, every view that shows sandbox data and error messages; a deliberate `dangerouslySetInnerHTML` mutation failed the test. Initial JavaScript is 112 KB gzip (budget 200). The Playwright smoke test (login, a sandbox appearing live, an exec's output arriving while it runs, markup staying inert, zero CSP violations, read-only UI, logout, security headers) passed in real Chromium against a live Arcana on runsc. That run found a real bug the unit tests missed, an `EventSource` reconnect loop caused by an unstable default factory; fixed, with regression tests verified to fail on the old code. **Not verified:** the new CI `web` job (runc, `playwright install --with-deps`) has not run on GitHub; local runs used Node 22.23 installed under the scratchpad with Chromium libraries unpacked from .debs because the host has Node 18 and no root. Light and dark themes and phone width follow Tailwind's responsive and `prefers-color-scheme` utilities but were not visually reviewed in a browser here (screenshots belong to T3.8).

### [x] T3.8 Docs site and recordings
- **Deps:** T3.4, T3.5, T3.7
- **Do:** VitePress site in `docs/` published to GitHub Pages by a workflow; VHS tapes in `docs/tapes/` for quickstart, parallel fork demo and doctor; a short screen recording of the dashboard during the parallel fork demo; README embeds the quickstart GIF and a dashboard screenshot; architecture page with diagrams from the PRD.
- **Verify:** docs workflow green; site renders.

> Done: Added a VitePress site in `docs/` (home, quickstart, SDKs, architecture, decision index, curated sidebar, local search; PRD/PLAN/AGENTS stay out of it; dead links fail the build), a Pages workflow (`.github/workflows/docs.yml`) and a CI `docs` build job. Three VHS tapes (`docs/tapes/`: doctor, quickstart, snapshot-and-fork) were recorded against a real Arcana on runsc and the GIFs are committed, as are a dashboard screenshot and a recording of the dashboard while three parallel forks work (`scripts/record-dashboard.mjs`, Playwright); `scripts/record-tapes.sh` regenerates the GIFs. The README embeds the quickstart GIF and the dashboard screenshot; the site embeds all recordings. The site built locally and its pages were checked in a browser. **Not verified:** the Pages deployment itself (needs the push and Pages enabled in the repository settings), so "docs workflow green" is untested. **Deviation:** the dashboard recording shows the snapshot-and-fork demo driven through the API, not the LLM fix-agent's `--parallel` mode, because no real model was available (see T3.4's open item). Recordings used a headless Chromium with its sandbox disabled, on this host only (unprivileged user namespaces are off), to render our own local terminal page.

### [x] T3.9 Devcontainer
- **Deps:** T3.8 · **PRD:** §14
- **Do:** `.devcontainer/` with Docker-in-Docker, Node LTS and pnpm for dashboard work, and an attempt to configure runsc; fall back to runc with `allow_insecure_runtime` and a loud banner if runsc fails. Document results.
- **Verify:** manual Codespaces check noted in PR.

> Done: Added `.devcontainer/` (devcontainer.json with Docker-in-Docker, Go, Node LTS, Python 3.12; pnpm via corepack in `post-create.sh`; ports 7070/9090 forwarded) and `setup-runtime.sh`, which installs and registers runsc, proves it with `docker run --runtime=runsc`, and otherwise writes a dev-only config with `oci_runtime: runc` + `allow_insecure_runtime: true` and a banner printed on creation, every start and every new terminal. Documented in `docs/devcontainer.md` (added to the docs sidebar). Checked locally: script syntax, JSON validity, forced-runc and detect-only paths, and that `arcana serve` accepts the generated config and logs the insecure-runtime warning. **Not verified:** a real Codespaces creation (the Verify step needs one, and whether runsc works there is unknown), the apt-based gVisor install path (this host already has runsc), and shellcheck (not installed here).

### [x] T3.10 v0.3 release
- **Deps:** T3.1–T3.9
- **Do:** README polish (recording, dashboard screenshot, benchmark highlights, security summary), CHANGELOG, tag `v0.3.0`; confirm release binaries include the dashboard.
- **Verify:** all CI workflows green; `/ui` loads from a downloaded release binary.

> Done: README (v0.3 summary, performance highlights with the untested density target stated plainly, dashboard security summary, devcontainer link), CHANGELOG `[0.3.0] - 2026-11-12`, and the `v0.3.0` tag pushed with owner approval (2026-11-12). The first CI run on the release commit failed for three real reasons, all fixed before tagging: (1) CI's corepack resolved pnpm 12, whose minimum-release-age policy rejected lockfile entries published within the last day — pnpm is now pinned to 10.34.9 via `packageManager` in `web/`, `docs/` and `sdk/typescript/` (lockfiles unchanged, no policy disabled); (2) the committed SDK layers were stale after T3.5–T3.7 added endpoints, which `make check` does not catch — regenerated; the TypeScript `test` script also passed a directory to `node --test`, which Node 22 does not expand — now a glob; (3) gosec G124 in the escape-test lint job (escape build tag) — suppressed with a reason on the test's request cookie. CI on `d65b1ee` is green in all eight jobs (check, shellcheck, markdown, sdk, docs, web, goreleaser, security with runsc). The Release workflow succeeded and published archives, deb/rpm for amd64 and arm64, checksums and `install.sh`; the downloaded amd64 archive passed its checksum, reports `arcana 0.3.0 (commit d65b1ee)`, and served the dashboard at `/ui/` with the FR-WD-9 CSP. **Not verified or not working:** GitHub Pages deployment — the Docs workflow's build passes but `deploy-pages` returns 404 because Pages is not enabled in the repository settings (an owner action: Settings → Pages → Source: GitHub Actions; re-run the Docs workflow afterwards). The GHCR image and the arm64 artifacts were not pulled or run; the `Template images` and tag-triggered `CI` runs for the tag were not inspected.

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

- 2026-11-01 — Owner approved an argv-only sbx-exec write subcommand for file uploads — shell redirection is unavailable through argv and internal shell construction remains prohibited — T1.12, PRD §9.4

- 2026-11-01 — Owner approved requiring web-check from T3.7, when the dashboard and target exist — Phase 1 still runs every Go, generation and applicable security check — AGENTS workflow step 5, T1.5

- 2026-11-01 — Add github.com/oapi-codegen/runtime v1.8.0 (Apache-2.0), required by the generated standard HTTP server — owner approved; owner also gave standing approval for Phase 1 implementation decisions, while security requirements and escape assertions remain mandatory — T1.5

- 2026-11-01 — Use the generator companion github.com/getkin/kin-openapi (MIT) to validate the API contract in tests — reuses the generator dependency and enforces spec validity under the owner's standing approval — T1.5

- 2026-11-01 — Pin the official Moby client v0.6.2 and API v1.56.1 (Apache-2.0), with containerd/errdefs (Apache-2.0) for typed daemon errors — reuse the Phase 0 validated successor modules under standing owner approval — T1.7, ADR 0001
- 2026-11-01 — Ship a locally built v0.1 base image with UID/GID 1000 workspace ownership and reject unexpected image volumes — standard Docker copy-up provisions named volumes while retaining every non-root/readonly/host-file boundary; official multi-arch templates remain T2.1 — T1.7, PRD FR-TP-4, ADR 0003
- 2026-11-01 — Docker instance handles use deterministic managed container names — enables idempotent volume cleanup after a container has already disappeared, with install/sandbox labels checked on each resource — T1.7

- 2026-11-01 — Use child subreapers, non-dumpable controllers, pidfd identity signalling and atomic bounded metadata for helper cleanup — ordinary process groups do not cover setsid/double-fork descendants; verified kernel facilities under hardened runsc/runc — T1.8, PRD §9.4, ADR 0002
- 2026-11-01 — Separate helper transport exits (0 completed success / 124 completed nonzero / 125 helper error) from user command exit metadata — prevents helper signals/runtime faults being accepted as ordinary command failure after status tampering; public API and arcana exec retain the real exit code; owner standing approval applied — T1.8, PRD §9.4
- 2026-11-01 — Commit reproducible static linux/amd64 and linux/arm64 helper assets plus checksum manifest — enables go install without an external compiler at serve time; generation/freshness checks rebuild them with the pinned Go toolchain, CGO disabled and build paths/IDs removed — T1.8

- 2026-11-01 — Explicit exec mount option for tmpfs /workspace — Docker defaults tmpfs to noexec, preventing sandbox build artifacts from running; nosuid/nodev, resource limits and all container hardening remain enforced — T1.8, PRD FR-RL-3

- 2026-11-01 — Add an append-only sandbox transition table, committed atomically with state changes — preserves timestamps/reasons for intermediate lifecycle stages required by FR-LC-3; quota reservation occurs in the creating-row transaction — T1.9, PRD §11

- 2026-11-01 — Bound cancellation proof to 10 seconds and retain valid completed-run proof when kill/status consumption races — prevents orphaned long-lived execs while preserving confirmed cleanup; uncertain status/transport destroys the sandbox — T1.10, PRD §9.4

- 2026-11-01 — Account separately for the canonical helper argv prefix while retaining the raw driver cap — supports the existing OpenAPI limit of 256 user arguments without removing any bound; env/label entry limits align with the contract limit of 100 — T1.10

- 2026-11-02 — Use coder/websocket v1.8.15 (ISC) already required by the pinned Goose graph — approved WebSocket dependency with concurrency-safe reads/writes and default same-origin checks; retain Goose v3.29.0 unchanged — T1.11

- 2026-11-02 — Reuse supervised exec for interactive streams, with bounded transport queues/deadlines and 1000 connection admission slots — preserves cleanup proof, quotas and records while preventing pending-start/slow-reader growth; streaming output is forwarded without retaining a sync capture buffer — T1.11, PRD §10.4

- 2026-11-02 — Reuse supervised exec for bounded argv-only file utilities and opaque tar streams — shares cleanup/quota/deadline enforcement while preserving the host filesystem boundary; atomic writes, descriptor-sized reads and bounded listings prevent partial replacements and unbounded output — T1.12, PRD FR-FS-1…5, ADR 0003

- 2026-11-02 — Reconcile before listener admission, preserve healthy instances, and fail closed on unfinished exec records after daemon crashes — lost supervision cannot prove process cleanup; close records only after resource removal and leave unknown exit codes NULL. Run independent joined reaper/watchdog loops; watchdog uses supervised bounded du without idle activity updates — T1.13, PRD FR-LC-8 and NFR-REL-1.

- 2026-11-02 — Public Go client uses generated wire-model aliases, finite configurable response/transfer limits, no redirects or automatic mutation retries, and context-owned WebSocket readers/control methods — preserves API compatibility and bounded memory; real-HTTP client tests also found that file-body EOF must suppress a cancellation deadline so a successful upload can finish its response — T1.14, PRD FR-SD-1.

- 2026-11-02 — CLI exec requires explicit argv separation and uses context-owned stream readers; stdin is opt-in except for TTY, and JSON exec emits terminal metadata after draining output — prevents leaked stdin readers and mixed JSON/binary output. Linux TTY uses stdlib ioctl/raw-mode handling without a new dependency; named downloads use exclusive 0600 creation — T1.15, PRD FR-CL-1/2.

- 2026-11-02 — Doctor probes the selected runtime with a hardened tmpfs container and helper completion proof, after startup reconciliation and before listener admission — avoids orphan-related resource starvation and checks required subreaper/pidfd facilities. Missing individual images warn without automatic pulls; at least one installed template is needed for runtime proof. Explicit runc opt-in preserves advisory runsc checks and mandatory runc proof — T1.16, FR-DR-1…3.

- 2026-11-03 — Escape suite uses public API attacks, test-only setuid fixtures and container-scoped kernel counters — observed runsc returning ENOMEM when PID cgroup denials increment, so ESC-01 requires PID-counter evidence with no OOM kills and either helper completion or fail-closed destruction. CPU latency waits for an active-spin marker; volume assertions wait for terminal disk_quota cleanup. CI requires runsc and fails on missing registration — T1.17, PRD §12.4.

- 2026-11-03 — Validate the marked README quickstart blocks verbatim on a fresh Ubuntu 24.04 GitHub-hosted VM with Go/Docker prerequisites, plus the authorized VPS — avoids documentation drift while exercising go install, runsc Doctor, daemon startup, first exec, file round trip and cleanup with new private state. The demo explicitly opts into destroy_on_shutdown; production defaults remain unchanged — T1.18, README, scripts/check_quickstart.py.

- 2026-11-08 — Use one isolated proxy per allowlist sandbox and exec-bridged private Unix control; preserve internal IP/MAC on recreation — keeps each complete runsc network set fixed, avoids host Unix-socket visibility assumptions and preserves healthy sandbox neighbour caches; owner-approved audit sequencing emits bounded events in T2.4 and persists them in T2.5 — PRD FR-NW-2/7, docs/networking.md.

- 2026-11-08 — Persist audit metadata through a bounded asynchronous queue, with API-key redaction, timestamp/ID pagination and startup/daily retention — completes the approved T2.4/T2.5 audit sequence without blocking request paths; deletion sizes are observed inside the sandbox — PRD FR-AD-1/2, docs/audit.md.

- 2026-11-09 — Owner approved ESC-03 host cgroup CPU accounting with the unchanged 1500 ms ceiling, explicit 0.25 CPU quota and throttling proof, and unchanged attack/peer checks — guest getrusage diverges from controller accounting under runsc; no resource policy or ceiling changed — docs/spikes/005-cpu-accounting.md, PRD §12.4.

- 2026-11-09 — Use the approved Apache-2.0 Prometheus client_golang v1.25.0 with an isolated registry, bounded labels and a separate listener; pin monitoring images by digest — reproducible self-hosting with explicit lifecycle ownership and no credentials in labels — T2.6, docs/compose-validation.md.

- 2026-11-12 — Snapshots store opaque archives under `data_dir/snapshots` with a size/SHA-256 record; a restore re-hashes the archive on the descriptor it streams and extracts only inside the sandbox; env values are never stored; a failed restore destroys the new sandbox with reason `restore_failed`; restores bypass the warm pool — keeps AGENTS rule 4 (host never extracts), makes tampering detectable, and honours "env values are never persisted" — T3.1, PRD FR-SN-1…5.

- 2026-11-12 — Fork returns `201` with `items` and per-index `failures` when at least one member was created, else the first failure's error; each member consumes one creation token and runs through the normal quota path; created members are destroyed if the client disconnects — PRD §10 only named the endpoint, so this defines its shape and keeps partial failure visible without leaking orphans — T3.1, PRD §10.2.

- 2026-11-12 — Snapshot byte and count budgets are reserved before streaming (in-flight snapshots count) — concurrent requests cannot overshoot `snapshots.max_per_key` or `snapshots.max_total_bytes`; no new dependency was needed — T3.1.

- 2026-11-12 — Use the approved MCP Go SDK v1.8.0 (Apache-2.0/MIT) and accept its transitive modules: google/jsonschema-go (MIT), segmentio/encoding and segmentio/asm (MIT), yosida95/uritemplate/v3 (BSD-3), golang.org/x/oauth2 and x/time (BSD-3) — they are required to build the SDK, all permissive and Apache-2.0 compatible; no other dependency added — T3.2.

- 2026-11-12 — SDK dependencies approved by the owner: openapi-typescript, openapi-fetch and `ws` (TypeScript); openapi-python-client (pinned with ruff), httpx2, attrs and `websockets` (Python); Node built-in test runner and `unittest` instead of Vitest/pytest — generated layers committed, wrappers hand-written for streaming, raw bodies and errors — T3.3, docs/decisions/0005-sdk-generation.md.

## Open questions


Append entries as `YYYY-MM-DD — question — context — blocking task (if any)`.

- 2026-10-31 — Resolved: owner supplied and authorized an experimental VPS; SSH, Docker, runsc and GitHub push access verified. CI passed for T0.1.
- 2026-10-31 — Resolved by owner: replace T0.4(d) docker cp/docker commit experiments with byte-identical read/archive round trips through exec; no security exception is introduced.

- 2026-11-01 — Resolved by owner approval: use a stdlib-only sbx-exec write subcommand with shared path validation, preserving the internal-shell ban. Original question: T1.12 specifies file writes as `stdin → cat > path` via argv, but shell redirection cannot execute through argv alone; introducing an internal shell string conflicts with AGENTS security rule 3. May T1.12 use a stdlib-only sbx-exec write subcommand (with the shared path validation) instead? Owner approved 2026-11-01; Phase 1 resumed.

- 2026-11-01 — Resolved by owner approval: defer web-check until T3.7; retain all Go, generation and security checks. Original question: May the AGENTS workflow require make web-check for OpenAPI changes only from T3.7, when the dashboard/target exist? T1.5 cannot satisfy the current unconditional check: make web-check fails because no target exists. All Go, generation and security checks would remain required. Owner approved 2026-11-01; T1.5 resumed.

- 2026-11-01 — Resolved by owner approval: add the Apache-2.0 companion runtime and continue. Original question: Approve github.com/oapi-codegen/runtime v1.8.0, the companion dependency imported by the approved oapi-codegen std-http-server generator? It is absent from the approved list; cannot compile or verify T1.5 without it. Owner approved 2026-11-01; T1.5 resumed.

- 2026-11-08 — Resolved: owner supplied Git author brendatamaar <brendatama10@gmail.com>; configured repository-local identity and resumed T2.1 commit.

- 2026-11-08 — Resolved by owner approval: T2.4 may implement bounded proxy decision events, with durable audit storage/wiring in T2.5; both tasks are complete and FR-NW-7 is implemented.

- 2026-11-09 — Resolved by owner approval and successful verification: the proposed ESC-03 cgroup CPU accounting/throttling checks while retaining the attack, 1500 ms ceiling and peer-health assertions? The existing guest timing assertion repeatedly failed; controlled measurements differ from cgroup-backed CPU usage, but the failing suite run was not instrumented. No escape test was weakened — docs/spikes/005-cpu-accounting.md; T2.5 is complete.

- 2026-11-09 — Owner approved the proposed ESC-03 cgroup accounting check; retain the CPU attack, 1500 ms ceiling and peer-health assertions, and additionally assert configured quota and throttling — docs/spikes/005-cpu-accounting.md.

- 2026-11-09 — Resolved: approved cgroup-based ESC-03 checks passed in isolation and the full escape suite; T2.5 verification is complete.

- 2026-11-12 — Docs site uses VitePress 1 (MIT), named in the T3.8 task, as the only docs dependency (`docs/package.json`, lockfile committed); recordings use VHS, ttyd and ffmpeg as local tools that are not repository dependencies — T3.8.

- 2026-11-12 — Dashboard frontend dependencies (beyond the AGENTS list, all MIT/Apache-2.0/ISC): `@tailwindcss/vite` (Tailwind 4's official integration), `jsdom` (the test DOM for Vitest and Testing Library), `typescript-eslint`, `@eslint/js` and `globals` (infrastructure for the approved ESLint), and the type packages `@types/react`, `@types/react-dom`, `@types/node`. No React Vite plugin is used (esbuild's automatic JSX runtime), no `jest-dom`, no component or state library. Majors are pinned conservatively (Vite 7, TypeScript 5.9, ESLint 9, Vitest 3, MSW 2, React Router 7) rather than the newest available — T3.7.

- 2026-11-12 — Add `GET /v1/sandboxes/{id}/history` (state transitions, owner-filtered, readable with a cookie) — FR-WD-2 requires a state history on the detail page but T3.6 exposed no endpoint for the `sandbox_transitions` data; the addition is read-only and additive, and is listed in PRD §10.1 and FR-WD-8 — T3.7.

- 2026-11-12 — Dashboard backend: cookie sessions authorize only GET/HEAD and logout and require same-origin proof (Origin match or Sec-Fetch-Site same-origin); Authorization always wins over the cookie; events are metadata-only and owner-filtered with pool events admin-only; output tails are in-memory rings with retention and caps; an unknown exec returns sandbox_not_found rather than adding an error code — keeps FR-WD-5 and FR-EX-8 intact without extending the PRD §10.3 code list — T3.6, docs/events.md.

- 2026-11-12 — OPEN (T3.4): run `make eval-agent` with a real local model (FIXAGENT_BASE_URL/FIXAGENT_MODEL, e.g. Ollama) and record the table and failures in docs/agent-results.md, for single-attempt and `--parallel 4`. The build environment had no model endpoint and ~1 GB RAM, so the acceptance item "runs end-to-end on at least one fixture with a local model; results recorded including failures" is unmet; no results were invented. Owner chose to leave this pending. Does not block T3.5–T3.7; T3.8 needs the real results for its demo recording.

- 2026-11-12 — Resolved by owner: T3.3 SDK dependencies (generated clients plus `ws`/`websockets` for streaming) approved; see ADR 0005.
- 2026-11-12 — Resolved by owner approval (push and tag done) — May I push `main` and tag `v0.3.0`? CI for Phase 3 (sdk, web, docs, goreleaser, Pages) has never run, and the T3.10 Verify step needs it; the T3.9 Codespaces check and real-model fix-agent run (T3.4) also remain unverified. — blocking T3.10.
- 2026-11-12 — Open — Enable GitHub Pages (Settings → Pages → Source: GitHub Actions) so the Docs workflow can deploy the site; no real-model fix-agent run (owner has no endpoint) and no Codespaces check (owner cannot) — T3.8/T3.4/T3.9 caveats.
