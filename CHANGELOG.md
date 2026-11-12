# Changelog

## [Unreleased]

## [0.3.0] - 2026-11-12

Workspace snapshots and fork, the MCP server, SDKs, the read-only dashboard, documentation site, reference agent, benchmarks and a development container. Not yet measured or verified: real-model fix-agent results (docs/agent-results.md), the density target on an 8 GB host, and a Codespaces creation.

- Add a development container (`.devcontainer/`) with Docker-in-Docker, Go, Node LTS with pnpm and Python. It tries to register gVisor (runsc) and otherwise falls back to runc with `allow_insecure_runtime` in a generated dev config and a loud banner on every start. Not yet verified in a real Codespace (docs/devcontainer.md).

- Add the documentation site (VitePress, published to GitHub Pages), an architecture page, and VHS terminal recordings of the CLI quickstart, `arcana doctor` and snapshot/fork plus a dashboard screenshot and recording, all recorded against a real Arcana; `scripts/record-tapes.sh` and `scripts/record-dashboard.mjs` regenerate them.

- Add the read-only web dashboard at `/ui` (React, TypeScript, Tailwind; overview, sandboxes with live updates, sandbox detail with state history, countdowns, command history and a live ANSI-aware log viewer), embedded in release binaries, with `make web`, `web-check`, `web-e2e` and `build-all`, a strict Content-Security-Policy, and `GET /v1/sandboxes/{id}/history`. Binaries built without it serve an explanatory page.

- Add the dashboard backend: cookie sessions (`POST /v1/sessions`, read-only by design), the `/v1/events` Server-Sent Events stream with resume, `/v1/overview`, exec history, and an in-memory exec output tail, with `ui.*` and `limits.exec.tail_*` settings, live metrics, Go client support and escape tests ESC-22/ESC-23.
- Fix `exec.started` audit events being silently dropped for shell commands.

- Add `arcana bench` (cold create, warm-pool acquire, exec overhead, density, snapshot/restore/fork) with JSON and Markdown output and PRD target verdicts, a chart script, and docs/benchmarks.md with a first measured run.

- Add `examples/fix-agent`, a reference coding agent (OpenAI-compatible endpoint, Ollama by default) with run, eval and verify-fixtures commands, step/token budgets, hidden-test judging and a `--parallel N` snapshot-and-fork mode, plus ten verified buggy fixtures and `make eval-agent`. No real-model results are recorded yet (docs/agent-results.md).

- Add Python (`sdk/python`, `arcana-sdk`) and TypeScript (`sdk/typescript`, `@brendatamaar/arcana`) SDKs generated from `api/openapi.yaml`, with hand-written streaming exec, binary file/archive transfer, typed errors and snapshot/fork helpers, `make sdk-generate`/`sdk-test`, and a CI job running each against a live Arcana (ADR 0005).

- Add `arcana mcp`, an MCP stdio server (official Go SDK) with `create_sandbox`, `run_command`, `read_file`, `write_file`, `list_files`, `snapshot_sandbox`, `fork_sandbox` and `destroy_sandbox` tools. Sandboxes are labelled `mcp_session=<id>` and destroyed when the session ends unless kept. See `docs/mcp.md`.

- Add workspace snapshots, restore and fork: `POST /v1/sandboxes/{id}/snapshots`, `GET/DELETE /v1/snapshots`, `POST /v1/snapshots/{id}/fork` and `from_snapshot` on sandbox creation, with Go client methods and `arcana snapshot create|list|rm`, `arcana fork` and `sandbox create --from-snapshot`. Archives are streamed out and extracted only inside sandboxes, verified by SHA-256 on restore, and bounded by `snapshots.max_per_key`, `snapshots.max_total_bytes` and `snapshots.max_fork`. Environment values are never stored.

## [0.2.0] - 2026-11-09

- Fix allowlist proxies recreated on restart keeping a stable MAC/IP on Docker 28 by setting them at container creation, and fail closed if Docker does not apply them.

- Add self-hoster docs (install, configuration reference, security model, operations), a test that every config key is documented, and Markdown lint in CI.

- Add GoReleaser archives, checksums, deb/rpm packages and multi-arch GHCR image, a tag-triggered release workflow, a checksum-verifying `install.sh` and a hardened systemd unit.

- Add public-API network escape tests for direct egress, forbidden proxy hosts, reserved addresses, DNS rebinding and peer isolation, with audit and health assertions.

- Add per-key sandbox-creation token buckets, Retry-After responses and Go client retry-delay metadata without automatic retries.

- Add separate Prometheus metrics, provisioned Grafana panels and a loopback-only Compose stack with a repeatable live-data smoke check.

- Add asynchronous metadata audit storage, owner-attributed egress events, admin filtering/pagination, retention pruning and Go client queries.

- Add isolated allowlist networking, a bounded HTTP/CONNECT proxy with pinned public-IP dialing, explicit DNS, source policies, reconciliation and subnet diagnostics.

- Add bounded per-template warm pools, compatible acquisition, cold fallback, backoff and shutdown cleanup.

- Add an authenticated template registry endpoint and Go client method with resolved defaults and environment key names.

- Add base, Python, Node and Go workspace-ready templates, offline runsc smoke tests and multi-architecture GHCR release builds.

## [0.1.0] - 2026-11-03

### Added

- Self-hosted Linux sandbox orchestration using Docker and gVisor by default, with explicit opt-in for runc.
- Authenticated v0.1 REST and WebSocket API, SHA-256 API key storage, local key management, ownership filtering and quota reservations.
- Sandbox lifecycle, persisted transition history, template/resource overrides, keepalive, TTL/idle reaping, startup reconciliation and optional destruction on shutdown.
- Hardened non-root containers with read-only rootfs, dropped capabilities, no-new-privileges, network-none and CPU/memory/PID limits.
- Named-volume workspaces with a soft quota watchdog and executable size-capped tmpfs workspaces.
- Supervised synchronous/streaming exec with process-tree cleanup, real exit codes, bounded output, stdin/TTY controls, cancellation and host deadlines.
- Reproducible embedded Linux amd64/arm64 helpers with checksum-verified installation.
- Binary files API and opaque archive transfers performed entirely inside sandboxes, including atomic bounded writes and path validation.
- Public Go client and CLI sandbox/exec/files commands with contexts, typed errors, finite transfer limits, pagination, JSON output and real remote exit codes.
- Doctor diagnostics, fix hints and startup gating with actual hardened runtime/helper cleanup proof.
- Public-API escape suite v1 (ESC-01–14, ESC-20, ESC-21) and required-runsc CI with peer/API/host health assertions.
- Ubuntu quickstart, CLI examples, security model and limitations, with executable README validation in CI.

### Fixed

- Runsc controller-disappearance races without relaxing process cleanup proof.
- Successful HTTP file uploads being cancelled by a read deadline after EOF.
- Cold-cache test fixture container-ID parsing when Docker prints image-pull progress to stderr.

[Unreleased]: https://github.com/brendatamaar/arcana/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/brendatamaar/arcana/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/brendatamaar/arcana/tree/v0.1.0
