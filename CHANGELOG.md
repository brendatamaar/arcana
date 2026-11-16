# Changelog

## [Unreleased]

- The containerd driver supports network mode `allowlist` when `runtime.containerd.allowlist` is true: a CNI network per sandbox with no route out, and a dedicated egress proxy container, as the Docker driver does ([ADR 0010](docs/decisions/0010-containerd-allowlist-networking.md)). New keys `runtime.containerd.allowlist`, `cni_bin_dir`, `egress_subnet` and `state_dir`. It needs Arcana to run as root, the CNI plugins `bridge`, `host-local` and `static`, and the proxy image in the containerd namespace; `arcana doctor` checks all of it. ESC-15 to ESC-19 pass on it (`scripts/containerd-escape.sh`) under gVisor and runc, nested in a container; not run on a bare host, and a host with a default-deny `FORWARD` policy needs a forwarding rule for the egress bridge. Four defects found by running it for real are fixed, among them a shim that resolves container IDs by prefix.
- The containerd driver supports the default `volume` workspace: a host directory per sandbox, bind-mounted at `/workspace`, removed with the sandbox and pruned at startup. It needs Arcana to run as root or UID 1000 (the sandbox user must own the directory); `arcana doctor` checks that. Verified on a real containerd under gVisor and runc. The size limit is soft, as with Docker volumes.
- `runtime.driver` now accepts `containerd` and `firecracker`, with `runtime.containerd.*` and `runtime.firecracker.*` settings (docs/configuration.md). Both support network mode `none` only: a configuration asking otherwise is rejected at start and an `allowlist` request is refused with `invalid_request`. `arcana doctor` runs a driver-specific check set for them. The containerd driver was verified end to end against a real containerd with gVisor (`scripts/containerd-serve-e2e.sh`); the Firecracker driver still has not run on KVM. Fix found on the way: destroying one gVisor sandbox failed while others ran, because they shared a cgroup parent.

- The containerd driver now runs against a real containerd (`scripts/containerd-test.sh`, nested in a privileged container) for both runsc and runc, and six defects found that way are fixed: named image users, the container command, the exec pipe directory (new `fifo_dir`), closing stdin, the gVisor terminal size, and gVisor metrics. On runc it was about 2 to 3 times faster than the Docker driver to create, exec and destroy; gVisor create latency under containerd was not measurable in the nested harness (ADR 0009).

- Add `scripts/kind-helm-e2e.sh` (`make helm-e2e`), which installs the Helm chart on a throwaway kind cluster against the host Docker and checks startup, a working sandbox, `helm test`, the pod's security context, upgrade and uninstall survival and the wrong-socket-group error. docs/kubernetes.md gains a troubleshooting section and the verified scope.

- Fix: every log line carried `"level":"[REDACTED]"` (the redaction of arbitrary objects also swallowed the built-in level), so logs could not be filtered by level. The level is visible again; other arbitrary objects are still redacted.

- Fix: the exec history (`GET /v1/sandboxes/{id}/execs`, and so the dashboard) no longer lists the server's own workspace scans or file transfers. They are still recorded in the `execs` table (new `internal` column, migration 00006) but are not commands anyone ran, which matches the live events.

- Add a Helm chart (`deploy/helm/arcana`, `make helm-check`, docs/kubernetes.md) that runs Arcana as one Deployment pinned to one node and drives that node's Docker Engine. It is root-equivalent on the node, needs Docker Engine and gVisor on that node, mounts its data directory by `hostPath` at the same path, and has never been installed on a real cluster (it is linted, rendered, schema-validated and asserted on, not run).

- Add a containerd driver (`internal/runtime/containerd`, ADR 0009) that runs sandboxes directly on containerd with the runsc shim and the same hardening as the Docker driver, written as OCI spec edits that a test asserts. It supports network mode `none` and tmpfs workspaces only, is not wired into `arcana serve`, and has **not been run against a real containerd**. New dependency (owner-approved): the containerd v2 client.

- **Dashboard write actions (T4.4, ADR 0008).** The dashboard can now create sandboxes from a template, keep them alive, snapshot and destroy them, fork and delete snapshots, and, for admin keys, create and revoke API keys and browse the audit log. Cookie sessions now authorize writes only with a matching `Origin` and the session's CSRF token (`X-CSRF-Token`, returned by `POST /v1/sessions` and the new `GET /v1/sessions/current`, which also reports `admin`); key management also needs the admin key typed again (`X-Arcana-Reauth`). Escape tests ESC-25 to ESC-27 were added.
- **Security fix:** a session cookie could reach the WebSocket exec stream and the file read endpoints from a same-origin request, because they are `GET`s. Cookies now reach only an allowlist of the dashboard's routes; exec, the exec stream, files and archives need an API key. Cross-site pages were never able to use this (`SameSite=Strict`, Origin checks).
- Fix: the dashboard's log viewer jumped to a newer entry of the exec history (the server's own workspace scans are listed there) instead of staying on the command being watched.

- Add a runtime comparison (docs/runtime-comparison.md): `arcana bench` on runc and runsc, twice each in opposite order on a loaded 2 vCPU host, with raw results under `docs/benchmarks/runtime-comparison/`, and the escape suite run on runc through the new comparison-only `ARCANA_ESCAPE_RUNTIME=runc` (default and CI unchanged). It passes on runc too, because the suite tests Arcana's hardening, not kernel exploits. No Firecracker numbers: no KVM host was available.

- Add a Firecracker microVM driver (`internal/runtime/firecracker`, ADR 0007), the guest init `cmd/arcana-init` and `scripts/build-rootfs.sh`. Each sandbox is a VM with a read-only rootfs and its own workspace disk, reached through `envd` over vsock; the VMM runs under the jailer by default. It supports network mode `none` only, has no memory snapshots, is **not wired into `arcana serve`** and has **not been run on a real KVM host** (tests use a fake VMM with a real `envd`; the real-VM test skips without `/dev/kvm`).

- Add `envd`, an in-sandbox gRPC daemon (`cmd/envd`, `make envd`, ADR 0006) for the planned Firecracker driver: streaming exec with stdin, TTY and resize, cancellation and a `Signal` RPC, and file read/write/list/remove/mkdir, over a private Unix socket or host-only vsock. It runs every command under the existing `sbx-exec` controller, so process trees (including `setsid` descendants) are still reaped and completion is still proven. Nothing in Arcana uses it yet. The helper's code moved to `internal/sbxexec`; `sbx-exec` behaves the same, its embedded binaries were rebuilt, and its `write` now removes its temporary file on SIGTERM. New dependencies (owner-approved): gRPC, protobuf, `golang.org/x/sys`, and protocompile as a code-generation tool.

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
