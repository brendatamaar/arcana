# PRD — Arcana

> **Arcana** is a self-hosted, open-source sandbox orchestrator that lets AI agents run untrusted code safely on their own infrastructure.

| Field | Value |
|---|---|
| Status | Draft 1.0 |
| Owner | @brendatamaar |
| Last updated | 2026-11-01 |
| License | Apache-2.0 |
| Go module | `github.com/brendatamaar/arcana` |
| Companion docs | `PLAN.md` (execution order and status), `AGENTS.md` (how to work in this repo) |

---

## 1. Summary

AI agents increasingly need to *execute* code, not just write it: running tests, installing packages, reproducing bugs, analysing data. Running that code directly on a host is dangerous, because the code is produced by a model that can be wrong or manipulated through prompt injection.

Arcana is a single Go binary that turns any Linux machine with Docker into a sandbox service. Agents call a REST/WebSocket API (or MCP tools) to create isolated sandboxes, run commands with streamed output, move files in and out, snapshot state, and fork sandboxes to try several solutions in parallel. Isolation defaults to **gVisor**, with strict resource limits, no network access by default, and an optional egress allowlist.

The project is distributed for **self-hosting**: users install and operate it themselves. There is no hosted service.

## 2. Problem

- Agent builders need a safe place to execute model-generated code. Plain Docker shares the host kernel and is not a reliable security boundary for hostile code.
- Hosted sandbox products exist, but some teams cannot or will not send code to a third party (compliance, cost, data residency, offline environments).
- Building isolation correctly is hard: process cleanup, resource limits, network egress control, path-traversal-safe file transfer, leaked containers after crashes, and fast startup. Each team re-solves these badly.

## 3. Goals and non-goals

### 3.1 Goals

1. **Safe by default.** Untrusted code cannot reach the host, other sandboxes, the host network, or cloud metadata endpoints with the default configuration.
2. **Agent-friendly API.** A small, predictable API for lifecycle, exec, files, snapshots and forks, plus an MCP server so MCP-capable agents can use it with no glue code.
3. **Easy to self-host.** One static binary, one config file, a `doctor` command that diagnoses the host, and install paths for curl, Docker Compose and systemd.
4. **Fast enough for agent loops.** A warm pool makes sandbox acquisition feel instant; exec overhead is low.
5. **Observable and auditable.** Prometheus metrics, structured logs, and an audit trail of every command, file write and egress decision.
6. **Pluggable runtimes.** A driver interface so Docker+gVisor, Docker+runc and (later) Firecracker can be swapped and benchmarked.
7. **Zero cost to develop and run.** Only open-source dependencies; CI on free GitHub Actions; no paid services required.

### 3.2 Non-goals

- A hosted or multi-region service.
- Multi-node clustering or scheduling (single node only through v0.3).
- Native Windows or macOS runtimes (they are supported only through a Linux VM or WSL2).
- GPU workloads.
- Long-lived development environments (sandboxes are ephemeral; maximum TTL is capped).
- Being a coding agent. Arcana is infrastructure; the reference agent exists only as a demo.
- Defending against hardware side channels or kernel 0-days beyond what gVisor provides.

## 4. Users

| Persona | Description | Needs |
|---|---|---|
| **Agent builder** (primary) | Developer building a coding or data agent | Simple SDK/MCP, fast exec, streaming output, parallel attempts |
| **Self-hoster / platform engineer** | Runs Arcana for a team on a VM or bare metal | Easy install, safe defaults, metrics, clear docs, upgrades |
| **Security reviewer** | Evaluates whether Arcana is safe to deploy | Threat model, escape test suite, audit log, honest limitations |
| **Portfolio reviewer** | Recruiter or engineer skimming the repo | README recording, live dashboard demo, benchmarks, architecture write-up |

## 5. User stories

- **US-1** As an agent builder, I create a Python sandbox and run `pytest` with live output in under a second when the warm pool has capacity.
- **US-2** As an agent builder, I upload a repository archive, run commands, read changed files, and download results.
- **US-3** As an agent builder, I snapshot a sandbox after installing dependencies and fork five copies to try five fixes in parallel.
- **US-4** As an agent builder, I connect Claude (or any MCP client) to Arcana and it can run code with no custom integration code.
- **US-5** As a self-hoster, I run one install command, then `arcana doctor` tells me exactly what is missing on my host and how to fix it.
- **US-6** As a self-hoster, I allow sandboxes to reach `pypi.org` but nothing else, and I can see every outbound request in the audit log.
- **US-7** As a self-hoster, I am confident that a crash or restart of Arcana never leaves orphaned containers running.
- **US-8** As a security reviewer, I run the escape test suite on my own host and see every attack contained.
- **US-9** As a self-hoster, I see active sandboxes, exec latency and pool status in Grafana.
- **US-10** As an agent builder or self-hoster, I open `/ui` in a browser and watch sandboxes and their commands live while an agent works, with nothing extra to install.

## 6. Release scope

| Release | Theme | Contents |
|---|---|---|
| **v0.1** | Core MVP | Config, SQLite store, API keys, Docker driver (runsc/runc), sandbox lifecycle, sync and streaming exec, files API, resource limits, workspace backends, TTL/idle reaper, startup reconciliation, CLI, `doctor`, Go client, escape suite v1 |
| **v0.2** | Self-hosting ready | Official templates, template registry, warm pool, network allowlist with egress proxy, audit log, metrics and Grafana dashboard, quotas and rate limits, escape suite v2 (network), GoReleaser, install script, Compose, systemd, docs |
| **v0.3** | Agent ecosystem and showcase | Snapshots, restore and fork, MCP server, Python and TypeScript SDKs, reference fix agent, benchmark harness, minimal read-only React web dashboard, docs site, terminal recordings, devcontainer |
| **Stretch** | Depth | In-sandbox daemon (`envd`), Firecracker driver, runtime comparison benchmarks, full web dashboard (write actions, key management, audit view), containerd driver, Helm chart |

Requirement tags below (`[v0.1]`, `[v0.2]`, …) state the release in which each requirement must be met.

## 7. Functional requirements

### 7.1 Sandbox lifecycle (LC)

- **FR-LC-1 [v0.1]** Create a sandbox from a template with optional overrides: TTL, idle timeout, resources (CPUs, memory, disk, PIDs), environment variables, labels, network policy.
- **FR-LC-2 [v0.1]** Get, list (filter by state and label) and destroy sandboxes. A key sees only its own sandboxes unless it is an admin key.
- **FR-LC-3 [v0.1]** Sandbox states: `creating → running → destroying → destroyed`, plus terminal states `failed` and `expired`. Every transition is persisted with a timestamp and, where relevant, a reason.
- **FR-LC-4 [v0.1]** TTL: each sandbox has an absolute `expires_at`. Requested TTL is capped by `limits.max_ttl_seconds`.
- **FR-LC-5 [v0.1]** Idle timeout: a sandbox with no API activity (exec, files, keepalive) for `idle_timeout_seconds` is destroyed.
- **FR-LC-6 [v0.1]** Keepalive endpoint extends idle time (never beyond `expires_at`).
- **FR-LC-7 [v0.1]** Reaper destroys expired and idle sandboxes; reason recorded (`ttl`, `idle`, `exec_deadline`, `disk_quota`).
- **FR-LC-8 [v0.1]** Startup reconciliation: on start, Arcana lists all containers, networks and volumes carrying the label `arcana.managed=true`, compares them with the database, destroys orphans, and marks database records whose containers are gone as `failed` with reason `lost`.
- **FR-LC-9 [v0.1]** Global and per-key concurrency limits (`max_sandboxes`, `max_sandboxes_per_key`); creation beyond the limit fails with `quota_exceeded`.
- **FR-LC-10 [v0.2]** Warm pool: per template (and network mode), keep N pre-started sandboxes. Acquisition from the pool must apply the requested env, labels and limits that can be changed after start; requests needing incompatible settings fall back to a cold start.

### 7.2 Command execution (EX)

- **FR-EX-1 [v0.1]** Sync exec: run a command and return `exit_code`, `stdout`, `stderr`, `duration_ms`, `timed_out`, `truncated`.
- **FR-EX-2 [v0.1]** Commands are given either as an argv array (`cmd`) or as a shell string (`shell`, executed with `/bin/sh -c`). Exactly one must be set. Arcana never builds shell strings by concatenation.
- **FR-EX-3 [v0.1]** Options: `cwd` (default `/workspace`), `env`, `timeout_seconds` (default and max from config), optional `stdin` (base64) for sync exec.
- **FR-EX-4 [v0.1]** Timeout and cancellation terminate the **entire process tree** of the command, not just the top process.
- **FR-EX-5 [v0.1]** Output capture is capped per exec (`limits.exec.max_output_bytes`, combined stdout+stderr). Beyond the cap, output is discarded, the command keeps running until it exits or times out, and the result has `truncated: true`.
- **FR-EX-6 [v0.1]** Host-side hard deadline: if an exec has not ended `timeout + 10s` after start, Arcana destroys the sandbox with reason `exec_deadline`.
- **FR-EX-7 [v0.1]** Streaming exec over WebSocket (§10.4): live stdout/stderr, stdin, stdin close, cancel, optional TTY with resize.
- **FR-EX-8 [v0.1]** Every exec is recorded (`execs` table): argv or shell string, timing, exit code, timed_out, truncated, output byte counts. Output content is not stored.
- **FR-EX-9 [v0.1]** Concurrent execs in one sandbox are allowed, capped by `limits.exec.max_concurrent_per_sandbox`.

### 7.3 Files (FS)

- **FR-FS-1 [v0.1]** Write a file (raw body), read a file (raw response), list a directory (optionally recursive with a depth limit), delete a path.
- **FR-FS-2 [v0.1]** Upload a `.tar.gz` archive and extract it into a directory; download a directory as `.tar.gz`.
- **FR-FS-3 [v0.1]** Paths must be absolute, are lexically cleaned, and must be under an allowed root (`/workspace`, `/tmp` by default). `..` escapes are rejected with `invalid_path`.
- **FR-FS-4 [v0.1]** All file operations execute **inside** the sandbox (via exec of tools in the sandbox), never by the host opening files from the workspace on the host filesystem. This makes symlink tricks harmless: they can only resolve to paths inside the sandbox.
- **FR-FS-5 [v0.1]** Size caps: `limits.files.max_file_bytes` per file, `limits.files.max_archive_bytes` per archive.
- **FR-FS-6 [v0.1]** File writes and deletes are recorded in the audit log (path and size, not contents) from v0.2.

### 7.4 Resource limits and workspace (RL)

- **FR-RL-1 [v0.1]** Enforce per sandbox: CPU quota, memory (swap disabled: memory-swap equals memory), PID limit, open-file limit.
- **FR-RL-2 [v0.1]** Hardening flags on every sandbox container: drop all capabilities, `no-new-privileges`, read-only root filesystem, non-root user (UID 1000), tmpfs `/tmp` and `/run/sbx`, no Docker socket or host paths mounted (except the read-only helper binary, §9.4).
- **FR-RL-3 [v0.1]** Workspace backends (`workspace.backend`):
  - `volume` (default): a Docker named volume per sandbox mounted at `/workspace`. Disk use is checked by a watchdog every `workspace.check_interval_seconds`; exceeding `disk_mb` destroys the sandbox with reason `disk_quota`. This is a soft limit and is documented as such.
  - `tmpfs`: a size-capped tmpfs at `/workspace`. This is a hard limit, but it is memory-backed, so its size counts against host RAM.
- **FR-RL-4 [v0.1]** Templates configure package managers to install into `/workspace` (for example a Python virtualenv at `/workspace/.venv`, `GOPATH=/workspace/.go`), because the root filesystem is read-only.

### 7.5 Network (NW)

- **FR-NW-1 [v0.1]** Mode `none` (default): the sandbox has no network interfaces besides loopback.
- **FR-NW-2 [v0.2]** Mode `allowlist`: the sandbox is attached to its own internal Docker network (no route out). An egress proxy (Arcana running in `proxy` mode in a dedicated container) is attached to that network and to an outbound network. Sandboxes receive `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` variables. Direct connections fail because there is no route. For a runsc proxy, attach its complete network set before starting it; recreate it when that set changes. The proxy uses explicit operator-configured DNS upstreams on its outbound network rather than relying on Docker embedded DNS (validated in T0.5).
- **FR-NW-3 [v0.2]** The proxy allows only hostnames on the sandbox's allowlist. HTTPS is handled through `CONNECT` without TLS interception; plain HTTP by `Host` header.
- **FR-NW-4 [v0.2]** SSRF protection: after DNS resolution the proxy rejects private, loopback, link-local (including `169.254.169.254`), multicast and unique-local addresses, and connects to the already-resolved IP to prevent DNS rebinding.
- **FR-NW-5 [v0.2]** Per-sandbox networks get subnets from Arcana's own pool (`network.subnet_pool`, `network.subnet_prefix`) to avoid exhausting Docker's default address pools.
- **FR-NW-6 [v0.2]** Allowlist presets per ecosystem (python, node, go) defined in config; requests can combine presets and explicit hostnames, capped by `network.max_allow_entries`.
- **FR-NW-7 [v0.2]** Every proxy decision (allow or deny, host, port, bytes, sandbox ID) is written to the audit log.
- **FR-NW-8** Mode `open` (unrestricted egress) is **not supported** in any release. This is deliberate.

### 7.6 Templates (TP)

- **FR-TP-1 [v0.1]** A template maps a name to an image plus defaults (resources, TTL, env, workdir, network preset, warm pool size). v0.1 ships a minimal `base` template defined in the default config.
- **FR-TP-2 [v0.2]** Official templates `base`, `python`, `node`, `go`, built from Dockerfiles in `templates/` and published to `ghcr.io/brendatamaar/arcana-<name>` by CI on release tags.
- **FR-TP-3 [v0.2]** `GET /v1/templates` lists configured templates and their defaults.
- **FR-TP-4 [v0.2]** Users can add their own templates in config; any image works if it has `/bin/sh`, `tar`, `du` and a UID 1000 user (or Arcana can run as UID 1000 without a named user).

### 7.7 Snapshots and fork (SN)

- **FR-SN-1 [v0.3]** Create a snapshot of a running sandbox: a compressed tar of `/workspace` streamed out through exec, stored opaquely under `data_dir/snapshots/`, plus metadata (template, env, resources, network policy, source sandbox).
- **FR-SN-2 [v0.3]** Snapshots capture the **workspace only**, not running processes or changes outside `/workspace`. This is documented clearly.
- **FR-SN-3 [v0.3]** Create a sandbox `from_snapshot`: create from the snapshot's template, then stream the archive into `/workspace` and extract it inside the sandbox. The host never extracts snapshot archives.
- **FR-SN-4 [v0.3]** Fork: create N sandboxes (capped by `snapshots.max_fork`) from one snapshot in one request, in parallel, with optional overrides.
- **FR-SN-5 [v0.3]** List, get and delete snapshots; per-key count limit and global storage limit.

### 7.8 Authentication and authorization (AU)

- **FR-AU-1 [v0.1]** All `/v1` endpoints except `/v1/health` require `Authorization: Bearer <api_key>`. From v0.3, safe methods may instead use a dashboard session cookie (FR-WD-4, FR-WD-5).
- **FR-AU-2 [v0.1]** API keys have the form `arc_live_<32 random base62 chars>`. Only the SHA-256 hash and an 8-character display prefix are stored.
- **FR-AU-3 [v0.1]** On first start with an empty database, Arcana creates an admin key, prints it once to the terminal, and writes it to `data_dir/admin.key` with mode `0600`.
- **FR-AU-4 [v0.1]** Admin keys can create, list and revoke keys and see all sandboxes. Non-admin keys see and operate only on resources they created.
- **FR-AU-5 [v0.1]** CLI `arcana keys create|list|revoke` works directly against the local database (for operators on the host).
- **FR-AU-6 [v0.2]** Per-key rate limit on sandbox creation (`limits.create_per_minute_per_key`).

### 7.9 CLI (CL)

- **FR-CL-1 [v0.1]** Subcommands: `serve`, `doctor`, `keys`, `sandbox create|list|get|rm`, `exec`, `files put|get|ls`, `version`. Client subcommands use `--server` and `--api-key` (or `ARCANA_URL`, `ARCANA_API_KEY`).
- **FR-CL-2 [v0.1]** `arcana exec <id> -- <argv...>` streams output and exits with the remote command's exit code.
- **FR-CL-3 [v0.2]** `arcana proxy` runs the egress proxy (used inside the proxy container).
- **FR-CL-4 [v0.3]** `arcana mcp` runs the MCP server over stdio.
- **FR-CL-5 [v0.3]** `arcana bench` runs the benchmark scenarios and writes JSON and Markdown results.

### 7.10 Doctor (DR)

- **FR-DR-1 [v0.1]** `arcana doctor` checks and reports, each as pass, warn or fail with a fix hint:
  - OS is Linux; kernel version; cgroups v2 mounted
  - Docker reachable; Docker version; storage driver
  - `runsc` registered as a Docker runtime and able to run a test container
  - `/dev/kvm` present (informational, for future Firecracker support)
  - data directory exists and is writable; database opens; migrations current
  - configured template images present or pullable
  - (v0.2) subnet pool does not overlap existing host routes or Docker networks
- **FR-DR-2 [v0.1]** Exit code is non-zero if any check that blocks `serve` fails. `--json` prints machine-readable output.
- **FR-DR-3 [v0.1]** `serve` refuses to start if the configured OCI runtime is `runc` unless `runtime.docker.allow_insecure_runtime: true`, and logs a prominent warning when it is.

### 7.11 Audit (AD)

- **FR-AD-1 [v0.2]** Audit events: key created or revoked, sandbox created or destroyed (with reason), exec started (argv or shell string, cwd), file written or deleted (path, size), archive uploaded, snapshot created, egress decision.
- **FR-AD-2 [v0.2]** Stored in SQLite, queryable by admin keys via `GET /v1/audit` (filter by sandbox, key, action, time range), pruned after `audit.retention_days`.

### 7.12 MCP server (MC)

- **FR-MC-1 [v0.3]** Built on the official MCP Go SDK; stdio transport; acts as a client of a running Arcana API (`--server`, `--api-key`).
- **FR-MC-2 [v0.3]** Tools: `create_sandbox`, `run_command`, `read_file`, `write_file`, `list_files`, `snapshot_sandbox`, `fork_sandbox`, `destroy_sandbox`. Tool descriptions state limits clearly (timeouts, output caps, workspace-only snapshots).
- **FR-MC-3 [v0.3]** Sandboxes created by an MCP session are labelled `mcp_session=<id>` and destroyed when the session ends, unless the tool call asked to keep them.

### 7.13 SDKs and reference agent (SD)

- **FR-SD-1 [v0.1]** Go client in `pkg/client` (public, documented, used by the CLI).
- **FR-SD-2 [v0.3]** Python and TypeScript clients generated from `api/openapi.yaml`, with a small hand-written streaming exec wrapper.
- **FR-SD-3 [v0.3]** Reference agent `examples/fix-agent`: given a repository with a failing test, it creates a sandbox, explores and fixes the code using an LLM through any OpenAI-compatible endpoint (Ollama by default), and verifies the fix by running tests. `--parallel N` mode snapshots after setup and forks N attempts, keeping the first passing one.
- **FR-SD-4 [v0.3]** A fixtures set of 10 small buggy projects (Python, Node, Go) with hidden tests for evaluating the agent.

### 7.14 Web dashboard (WD)

- **FR-WD-1 [v0.3]** A read-only web dashboard built with React and TypeScript, compiled to static files, embedded in the binary and served at `/ui` on the API listener. It can be disabled with `ui.enabled: false`.
- **FR-WD-2 [v0.3]** Pages:
  - **Login:** paste an API key; it is exchanged for a browser session (FR-WD-4) and never stored by the browser.
  - **Overview:** version, runtime, active sandboxes by template, warm pool fill per template, recent terminations with reasons.
  - **Sandboxes:** list filterable by state, template and label, updated live.
  - **Sandbox detail:** spec, state history, TTL and idle countdowns, exec history, and a live log viewer that tails the output of running execs.
- **FR-WD-3 [v0.3]** Read-only by design: apart from login and logout, the dashboard performs no mutations. Destroying sandboxes, key management and the audit view belong to the stretch dashboard (PLAN T4.4).
- **FR-WD-4 [v0.3]** Browser sessions: `POST /v1/sessions` (authenticated with an API key) creates a session and sets its token as a cookie with `HttpOnly`, `SameSite=Strict`, `Path=/`, and `Secure` unless the request is plain HTTP to a loopback address. Only the SHA-256 hash of the token is stored. Sessions expire after `ui.session_ttl_seconds` and stop working as soon as the underlying key is revoked. `DELETE /v1/sessions/current` logs out.
- **FR-WD-5 [v0.3]** Session cookies authenticate **only safe methods** (`GET`, `HEAD`) plus `DELETE /v1/sessions/current`. Any other request authenticated only by a cookie is rejected with `403 forbidden`. Cookie-authenticated requests must carry an `Origin` (or, for `GET`, `Sec-Fetch-Site`) matching the server's own origin or `ui.allowed_origins`.
- **FR-WD-6 [v0.3]** Event stream `GET /v1/events` (Server-Sent Events): sandbox created, state changed and destroyed (with reason), exec started and ended (metadata only), and pool level changes, filtered to what the caller may see. Events carry increasing IDs; the server keeps the last `ui.event_buffer` events so a reconnecting client resumes with `Last-Event-ID`. A client too slow to keep up is sent a `reset` event and disconnected, and must refetch state.
- **FR-WD-7 [v0.3]** Exec output tail `GET /v1/sandboxes/{id}/execs/{exec_id}/output` (SSE): replays buffered output, then streams live output until the exec ends. Both sync and streaming execs feed a per-exec in-memory ring buffer (`limits.exec.tail_buffer_bytes`) that is dropped `limits.exec.tail_retention_seconds` after the exec ends, so output is still never persisted (FR-EX-8). Subscribers per exec are capped by `limits.exec.max_tail_subscribers`.
- **FR-WD-8 [v0.3]** Supporting read endpoints: `GET /v1/sandboxes/{id}/execs` (exec history from the `execs` table, paginated) and `GET /v1/overview` (Overview page data; admin keys see global numbers, other keys see their own).
- **FR-WD-9 [v0.3]** Hardened static serving under `/ui`: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. Client routes fall back to `index.html`; hashed assets get long-lived cache headers, `index.html` gets `no-cache`.
- **FR-WD-10 [v0.3]** The dashboard is optional at build time. A binary built without the frontend (for example with `go install`) serves a short page at `/ui` explaining that release binaries include the dashboard. Release builds always include it.
- **FR-WD-11 [v0.3]** Works at desktop width and stays readable on a phone; light and dark themes follow the OS setting; keyboard navigable, with labelled controls and WCAG AA contrast.

## 8. Non-functional requirements

### 8.1 Security

- **NFR-SEC-1** Default configuration uses gVisor (`runsc`), network mode `none`, and the hardening in FR-RL-2.
- **NFR-SEC-2** The escape test suite (§12.4) runs in CI on every pull request against gVisor and must pass 100%.
- **NFR-SEC-3** The API listens on `127.0.0.1` by default. Binding to other addresses logs a warning recommending a TLS-terminating reverse proxy.
- **NFR-SEC-4** No secrets in logs: API keys are never logged; env values passed to sandboxes are not logged (keys only).
- **NFR-SEC-5** Docker socket access is root-equivalent. Docs must say so and recommend a dedicated host or VM.

### 8.2 Performance (initial targets, validated in Phase 0 and adjusted with evidence)

Reference host: Linux, 4 vCPU, 8 GB RAM, SSD, Docker with `runsc`, `base` template.

| Metric | Target |
|---|---|
| Sandbox acquire from warm pool, p50 / p99 | ≤ 150 ms / ≤ 500 ms |
| Cold sandbox create (image cached), p50 | ≤ 2 s |
| Sync exec overhead for `true`, p50 | ≤ 100 ms |
| Idle sandboxes per 8 GB host (256 MB limit each) | ≥ 20 |
| Arcana RSS at idle | ≤ 80 MB |
| Dashboard initial JavaScript, gzip | ≤ 200 KB |

### 8.3 Reliability

- **NFR-REL-1** Arcana restart never leaves orphaned containers, volumes or networks (FR-LC-8).
- **NFR-REL-2** Graceful shutdown on SIGTERM: stop accepting requests, finish or cancel in-flight execs within `server.shutdown_timeout_seconds`, keep sandboxes running (they are reconciled on next start) unless `server.destroy_on_shutdown: true`.
- **NFR-REL-3** SQLite in WAL mode with a busy timeout; a single writer connection pool to avoid lock errors.

### 8.4 Portability and packaging

- **NFR-POR-1** Pure Go with `CGO_ENABLED=0`; static binaries for `linux/amd64` and `linux/arm64`. Node is needed only to build the dashboard at release time, never to run Arcana.
- **NFR-POR-2** Runs on any Linux with Docker ≥ 24 and cgroups v2. WSL2 is supported as best effort. macOS works only through a Linux VM (documented, not tested in CI).
- **NFR-POR-3** Only open-source dependencies with permissive or weak-copyleft licenses compatible with Apache-2.0 distribution.

### 8.5 Operability

- **NFR-OPS-1** Structured JSON logs (`log/slog`) with request IDs, sandbox IDs and exec IDs.
- **NFR-OPS-2** Prometheus metrics on a separate listener (`server.metrics_listen`).
- **NFR-OPS-3** All configuration in one YAML file, every key documented, every key overridable by environment variable.

### 8.6 Quality

- **NFR-QA-1** Unit test coverage ≥ 70% for `internal/` packages excluding generated code.
- **NFR-QA-2** `golangci-lint` clean; `go vet` clean; generated code verified up to date in CI.

## 9. Architecture

### 9.1 Overview

```
 Agents / CLI / MCP / SDKs / Web dashboard (browser)
            │  HTTPS (via user's reverse proxy) or localhost HTTP + WebSocket + SSE
            ▼
┌─────────────────────────────────────────────────────────────┐
│ arcana (single Go binary)                                   │
│                                                             │
│  api ── auth ── ratelimit          (net/http, oapi-codegen) │
│   │                                                         │
│  orchestrator                                               │
│   ├─ lifecycle / state machine                              │
│   ├─ exec manager (sync + stream)                           │
│   ├─ files (exec-based, tar)                                │
│   ├─ warm pool          (v0.2)                              │
│   ├─ reaper + reconciler                                    │
│   ├─ workspace watchdog                                     │
│   └─ snapshots          (v0.3)                              │
│   │                                                         │
│  runtime.Driver interface                                   │
│   ├─ docker (runsc | runc)                                  │
│   └─ firecracker        (stretch)                           │
│                                                             │
│  netpolicy: subnet allocator + proxy manager   (v0.2)       │
│  store: SQLite (modernc) + goose + sqlc                     │
│  metrics (Prometheus)   audit (v0.2)                        │
│  events (SSE) + exec tail buffers              (v0.3)       │
│  webui: embedded React build at /ui            (v0.3)       │
└─────────────────────────────────────────────────────────────┘
            │ Docker Engine API (unix socket)
            ▼
┌──────────────────────┐   ┌──────────────────────┐
│ sandbox container    │   │ egress proxy         │  (v0.2)
│ runsc, UID 1000      │──▶│ `arcana proxy`       │──▶ allowlisted hosts
│ /workspace  (vol)    │   │ per-sandbox networks │
│ /opt/arcana/sbx-exec (ro)                        │
└──────────────────────┘   └──────────────────────┘
```

### 9.2 Components

| Package | Responsibility |
|---|---|
| `cmd/arcana` | Main binary and Cobra commands |
| `cmd/sbx-exec` | In-sandbox exec helper (static binary, embedded into Arcana) |
| `internal/config` | Load, default, validate, env-override configuration |
| `internal/api` | HTTP handlers, middleware, error mapping; `internal/api/gen` holds generated code |
| `internal/auth` | API key generation, hashing, verification, middleware |
| `internal/store` | SQLite connection, migrations, sqlc queries |
| `internal/orchestrator` | Lifecycle, exec, files, pool, reaper, reconcile, watchdog, snapshots |
| `internal/runtime` | `Driver` interface and shared types; `runtime/docker`, `runtime/fake` |
| `internal/netpolicy` | Subnet allocation, network creation, proxy container management, proxy server |
| `internal/stream` | WebSocket exec protocol |
| `internal/doctor` | Host checks |
| `internal/metrics` | Prometheus collectors |
| `internal/audit` | Audit event writer and pruning |
| `internal/mcp` | MCP server |
| `internal/events` | In-process event bus, SSE handlers, per-exec output ring buffers |
| `internal/webui` | Embedded dashboard build, static serving, security headers |
| `web/` | React dashboard source (TypeScript, Vite) |
| `pkg/client` | Public Go client |

### 9.3 Runtime driver interface (shape, not final)

```go
type Driver interface {
    Name() string
    Capabilities() Capabilities // e.g. SupportsTTY, SupportsMemorySnapshot

    Create(ctx context.Context, spec Spec) (Instance, error) // create and start
    Destroy(ctx context.Context, id string) error            // idempotent
    Exec(ctx context.Context, id string, req ExecRequest) (ExecSession, error)
    Stats(ctx context.Context, id string) (Stats, error)
    List(ctx context.Context) ([]Instance, error)            // managed instances only
}

type ExecSession interface {
    Stdin() io.WriteCloser
    Stdout() io.Reader
    Stderr() io.Reader          // nil when TTY
    Resize(cols, rows uint16) error
    Wait(ctx context.Context) (ExitInfo, error)
}
```

Files, snapshots and cancellation are implemented in the orchestrator on top of `Exec`, so every driver gets them for free.

### 9.4 Exec helper (`sbx-exec`)

Docker's exec API cannot signal or kill an exec'd process. To satisfy FR-EX-4, Arcana embeds a tiny static helper, writes it to `data_dir/bin/sbx-exec`, and bind-mounts it read-only at `/opt/arcana/sbx-exec` in every sandbox.

- `sbx-exec run --id <exec_id> --timeout <s> [--cwd ..] [--tty] -- <argv...>` starts the command in a new process group (for TTY: new process group set as the terminal's foreground group), writes `/run/sbx/<exec_id>.pid`, kills the whole group with SIGKILL on timeout, and writes `/run/sbx/<exec_id>.json` with `{exit_code, timed_out, signal}`.
- `sbx-exec kill --id <exec_id> [--grace 2s]` sends SIGTERM to the group, then SIGKILL after the grace period.
- `sbx-exec status --id <exec_id>` prints the JSON status.
- `sbx-exec ls --path <dir> [--depth N]` prints a JSON directory listing, so the files API gets consistent output regardless of the image's `find`/`ls` variants.

The helper is the seed of the stretch-goal `envd` daemon used by the Firecracker driver.

### 9.5 Labels and naming

All Docker objects created by Arcana carry `arcana.managed=true`, `arcana.instance=<install id>`, and, where applicable, `arcana.sandbox_id=<id>`, `arcana.pool=<template>`. Names: `sbx-<id>` (container), `sbx-<id>-ws` (volume), `sbx-<id>-net` (network).

### 9.6 IDs

Prefixed random IDs: `sbx_` (sandbox), `exe_` (exec), `snp_` (snapshot), `key_` (API key), `aud_` (audit event), `ses_` (dashboard session), followed by 20 lowercase base32 characters from `crypto/rand`.

## 10. API

Base path `/v1`. JSON request and response bodies unless noted. The source of truth is `api/openapi.yaml`; this section is the design summary.

### 10.1 Endpoints

| Method | Path | Release | Description |
|---|---|---|---|
| GET | `/v1/health` | v0.1 | Liveness; no auth |
| POST | `/v1/sandboxes` | v0.1 | Create |
| GET | `/v1/sandboxes` | v0.1 | List (`state`, `label=k:v`, `limit`, `cursor`) |
| GET | `/v1/sandboxes/{id}` | v0.1 | Get |
| DELETE | `/v1/sandboxes/{id}` | v0.1 | Destroy (idempotent) |
| POST | `/v1/sandboxes/{id}/keepalive` | v0.1 | Reset idle timer |
| POST | `/v1/sandboxes/{id}/exec` | v0.1 | Sync exec |
| GET | `/v1/sandboxes/{id}/exec/stream` | v0.1 | WebSocket exec |
| GET | `/v1/sandboxes/{id}/files?path=` | v0.1 | Read file (raw bytes) |
| PUT | `/v1/sandboxes/{id}/files?path=` | v0.1 | Write file (raw body) |
| DELETE | `/v1/sandboxes/{id}/files?path=&recursive=` | v0.1 | Delete |
| GET | `/v1/sandboxes/{id}/files/list?path=&depth=` | v0.1 | List directory |
| GET | `/v1/sandboxes/{id}/files/archive?path=` | v0.1 | Download `.tar.gz` |
| PUT | `/v1/sandboxes/{id}/files/archive?path=` | v0.1 | Upload and extract `.tar.gz` |
| POST | `/v1/keys` | v0.1 | Create key (admin) |
| GET | `/v1/keys` | v0.1 | List keys (admin) |
| DELETE | `/v1/keys/{id}` | v0.1 | Revoke key (admin) |
| GET | `/v1/templates` | v0.2 | List templates |
| GET | `/v1/audit` | v0.2 | Query audit log (admin) |
| POST | `/v1/sandboxes/{id}/snapshots` | v0.3 | Create snapshot |
| GET | `/v1/snapshots` | v0.3 | List snapshots |
| GET | `/v1/snapshots/{id}` | v0.3 | Get snapshot |
| DELETE | `/v1/snapshots/{id}` | v0.3 | Delete snapshot |
| POST | `/v1/snapshots/{id}/fork` | v0.3 | Create N sandboxes from snapshot |
| POST | `/v1/sessions` | v0.3 | Exchange an API key for a dashboard session cookie |
| DELETE | `/v1/sessions/current` | v0.3 | Log out of the dashboard |
| GET | `/v1/events` | v0.3 | Server-Sent Events stream of lifecycle, exec and pool events |
| GET | `/v1/overview` | v0.3 | Overview data for the dashboard |
| GET | `/v1/sandboxes/{id}/execs` | v0.3 | Exec history (paginated) |
| GET | `/v1/sandboxes/{id}/execs/{exec_id}/output` | v0.3 | Server-Sent Events tail of buffered and live exec output |

The dashboard itself is served outside the API namespace at `/ui` (FR-WD-1, FR-WD-9).

### 10.2 Examples

Create:

```http
POST /v1/sandboxes
{
  "template": "python",
  "ttl_seconds": 1800,
  "idle_timeout_seconds": 600,
  "resources": { "cpus": 1, "memory_mb": 1024, "disk_mb": 2048, "pids": 256 },
  "network": { "mode": "allowlist", "presets": ["python"], "allow": ["github.com"] },
  "env": { "CI": "1" },
  "labels": { "task": "fix-123" },
  "from_snapshot": null
}
```

```json
{
  "id": "sbx_k3j9w2m4p8q1r7t5v6x0",
  "state": "running",
  "template": "python",
  "runtime": "docker/runsc",
  "source": "pool",
  "resources": { "cpus": 1, "memory_mb": 1024, "disk_mb": 2048, "pids": 256 },
  "network": { "mode": "allowlist", "allow": ["pypi.org", "files.pythonhosted.org", "github.com"] },
  "labels": { "task": "fix-123" },
  "created_at": "2026-10-13T10:00:00Z",
  "expires_at": "2026-10-13T10:30:00Z"
}
```

Sync exec:

```http
POST /v1/sandboxes/{id}/exec
{ "cmd": ["pytest", "-q"], "cwd": "/workspace", "env": {}, "timeout_seconds": 120 }
```

```json
{
  "exec_id": "exe_a1b2c3d4e5f6g7h8i9j0",
  "exit_code": 1,
  "stdout": "F.\n1 failed, 1 passed in 0.12s\n",
  "stderr": "",
  "timed_out": false,
  "truncated": false,
  "duration_ms": 842
}
```

`stdout` and `stderr` are UTF-8 strings with invalid bytes replaced; clients needing exact bytes use the streaming endpoint (base64 frames).

### 10.3 Error model

```json
{ "error": { "code": "sandbox_not_found", "message": "sandbox sbx_... not found", "request_id": "req_..." } }
```

| HTTP | Codes |
|---|---|
| 400 | `invalid_request`, `invalid_path`, `invalid_template` |
| 401 | `unauthorized` |
| 403 | `forbidden` |
| 404 | `sandbox_not_found`, `snapshot_not_found`, `file_not_found`, `key_not_found` |
| 409 | `sandbox_not_running`, `exec_limit_reached` |
| 413 | `payload_too_large` |
| 429 | `quota_exceeded`, `rate_limited` |
| 500 | `internal` |
| 503 | `runtime_unavailable`, `shutting_down` |

### 10.4 WebSocket exec protocol

`GET /v1/sandboxes/{id}/exec/stream` with `Authorization: Bearer` header. Text frames, one JSON object each. Binary data is base64 in `data`.

Client → server:

| `type` | Fields | Notes |
|---|---|---|
| `start` | same as sync exec body, plus `tty` (bool), `cols`, `rows` | Must be the first message |
| `stdin` | `data` | |
| `stdin_close` | — | Closes the command's stdin |
| `resize` | `cols`, `rows` | TTY only |
| `cancel` | — | Kills the process group (§9.4) |

Server → client:

| `type` | Fields |
|---|---|
| `started` | `exec_id` |
| `stdout` / `stderr` | `data` (in TTY mode only `stdout`) |
| `exit` | `exit_code`, `timed_out`, `truncated`, `cancelled`, `duration_ms` |
| `error` | `code`, `message` |

The server closes the socket after `exit` or `error`. Client disconnect without `exit` cancels the command. Server applies backpressure: if the client stops reading and the send buffer exceeds 1 MB, output is dropped and `truncated` is set.

## 11. Data model (SQLite)

```sql
api_keys(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, prefix TEXT NOT NULL,
  hash BLOB NOT NULL UNIQUE, is_admin INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL, last_used_at TIMESTAMP, revoked_at TIMESTAMP)

sandboxes(
  id TEXT PRIMARY KEY, owner_key_id TEXT NOT NULL REFERENCES api_keys(id),
  template TEXT NOT NULL, runtime TEXT NOT NULL, state TEXT NOT NULL,
  container_id TEXT, source TEXT NOT NULL,              -- pool | cold | snapshot
  cpus REAL, memory_mb INTEGER, disk_mb INTEGER, pids INTEGER,
  network_mode TEXT NOT NULL, network_allow TEXT,        -- JSON array
  env_keys TEXT, labels TEXT,                            -- JSON
  from_snapshot_id TEXT,
  created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL,
  idle_timeout_seconds INTEGER NOT NULL, last_activity_at TIMESTAMP NOT NULL,
  ended_at TIMESTAMP, end_reason TEXT)

execs(
  id TEXT PRIMARY KEY, sandbox_id TEXT NOT NULL REFERENCES sandboxes(id),
  command TEXT NOT NULL,                                 -- JSON argv or shell string
  is_shell INTEGER NOT NULL, cwd TEXT, tty INTEGER NOT NULL DEFAULT 0,
  started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP,
  exit_code INTEGER, timed_out INTEGER, cancelled INTEGER, truncated INTEGER,
  stdout_bytes INTEGER, stderr_bytes INTEGER)

snapshots(                                               -- v0.3
  id TEXT PRIMARY KEY, owner_key_id TEXT NOT NULL, source_sandbox_id TEXT NOT NULL,
  template TEXT NOT NULL, spec TEXT NOT NULL,            -- JSON (resources, env keys, network)
  path TEXT NOT NULL, size_bytes INTEGER NOT NULL, sha256 TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL)

audit_events(                                            -- v0.2
  id TEXT PRIMARY KEY, ts TIMESTAMP NOT NULL, key_id TEXT, sandbox_id TEXT,
  action TEXT NOT NULL, details TEXT)                    -- JSON

sessions(                                                -- v0.3
  id TEXT PRIMARY KEY, key_id TEXT NOT NULL REFERENCES api_keys(id),
  token_hash BLOB NOT NULL UNIQUE, created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL, last_seen_at TIMESTAMP)

meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)          -- install id, schema info
```

Environment variable **values** are never persisted; only keys (`env_keys`). Warm pool members are not stored in the database; they are tracked in memory and identified by Docker labels.

## 12. Security and threat model

### 12.1 Assets

Host OS and kernel; other sandboxes; host network and services; cloud metadata endpoints; Arcana database and API keys; dashboard sessions; Docker socket.

### 12.2 Adversaries

1. **Malicious code inside a sandbox** (primary): model-generated code, prompt-injected instructions, or malicious dependencies.
2. **Authenticated but untrusted API client**: holds a non-admin key and tries to reach other keys' sandboxes or exhaust resources.
3. **Network attacker** who can reach the API port.
4. **Malicious website** visited by a dashboard user, attempting CSRF or cross-site reads of the event and output streams.

### 12.3 Threats and mitigations

| Threat | Mitigation |
|---|---|
| Kernel exploit / container escape | gVisor user-space kernel by default; runc requires explicit opt-in |
| Privilege escalation in sandbox | Non-root UID, all caps dropped, `no-new-privileges`, read-only rootfs |
| Fork bomb, memory bomb, CPU hog | PID limit, memory limit without swap, CPU quota |
| Disk filling | Workspace quota (volume watchdog or tmpfs size); read-only rootfs; tmpfs size limits on `/tmp`, `/run/sbx` |
| Runaway process | Exec timeout via `sbx-exec`; host hard deadline destroys sandbox; TTL and idle reaper |
| Output flood / memory exhaustion of Arcana | Per-exec output cap; WebSocket backpressure; file and archive size caps |
| Path traversal via files API | Lexical validation against allowed roots; all file ops execute inside the sandbox |
| Symlink attack on host | Host never opens workspace files; snapshots extracted only inside sandboxes |
| Data exfiltration over network | Mode `none` by default; allowlist proxy only; no route for direct connections; DNS not available in `none` mode |
| SSRF / cloud metadata access | Proxy blocks private and link-local ranges after resolution; connects to resolved IP |
| Lateral movement between sandboxes | Per-sandbox networks; no shared network in `none` mode |
| Cross-tenant access via API | Ownership checks on every resource; IDs are unguessable; admin-only endpoints |
| Key theft from DB | Keys stored as SHA-256 hashes; admin key file `0600` |
| Network eavesdropping on API | Localhost bind by default; docs require TLS reverse proxy for remote access |
| Orphaned resources after crash | Labels plus startup reconciliation |
| CSRF through the dashboard session | Cookies authorize safe methods only; `SameSite=Strict`; `Origin` checks |
| Cross-site reading of event or output streams | `Origin` / `Sec-Fetch-Site` checks on cookie requests; `SameSite=Strict` |
| XSS from sandbox output, file names or labels shown in the dashboard | Rendered as text by React; ANSI colours parsed into styled spans, never HTML; no `dangerouslySetInnerHTML`; strict CSP |
| Session token theft | `HttpOnly` cookie; token stored hashed; short TTL; revoked with its key |
| Memory growth from event or output buffers | Fixed-size rings, subscriber caps, slow-client disconnect |

### 12.4 Escape test suite

Located in `test/escape`, run with `make test-escape` and in CI. Each test asserts the attack is contained **and** the host and other sandboxes remain healthy.

| ID | Attack | Expected | Release |
|---|---|---|---|
| ESC-01 | Fork bomb | PID limit hit; Arcana and other sandboxes responsive | v0.1 |
| ESC-02 | Memory bomb | OOM inside sandbox only | v0.1 |
| ESC-03 | CPU spin ×N | Throttled to quota; a second sandbox's exec latency within bound | v0.1 |
| ESC-04 | Disk fill in workspace | Sandbox destroyed with `disk_quota` (volume) or ENOSPC (tmpfs) | v0.1 |
| ESC-05 | Write outside workspace (`/etc`, `/usr`) | EROFS / EACCES | v0.1 |
| ESC-06 | Files API path traversal (`../`, encoded variants) | `invalid_path` | v0.1 |
| ESC-07 | Symlink in workspace to host path, then read via files API | Resolves inside sandbox only; host file never returned | v0.1 |
| ESC-08 | Command ignoring SIGTERM past timeout, with child processes | Whole process group killed; `timed_out: true` | v0.1 |
| ESC-09 | Output flood (`yes`) | `truncated: true`; Arcana memory stable | v0.1 |
| ESC-10 | Raw socket / ping | Fails (no `CAP_NET_RAW`) | v0.1 |
| ESC-11 | setuid binary escalation | Fails (`no-new-privileges`) | v0.1 |
| ESC-12 | `mount`, `ptrace` of other process, kernel module load | Fails | v0.1 |
| ESC-13 | Docker socket access | Not present | v0.1 |
| ESC-14 | Any network in `none` mode (TCP, UDP, DNS) | Fails | v0.1 |
| ESC-15 | Direct TCP to internet in `allowlist` mode | Fails (no route) | v0.2 |
| ESC-16 | Proxy request to non-allowlisted host | 403 from proxy; audit event | v0.2 |
| ESC-17 | Proxy to `169.254.169.254`, `127.0.0.1`, RFC1918, IPv6 ULA | Denied | v0.2 |
| ESC-18 | DNS rebinding via allowlisted-looking hostname | Denied after resolution | v0.2 |
| ESC-19 | Reach another sandbox's IP | Fails | v0.2 |
| ESC-20 | Kill Arcana mid-exec, restart | Reconciliation destroys orphans | v0.1 |
| ESC-21 | Non-admin key accessing other key's sandbox | 404 | v0.1 |
| ESC-22 | Mutating request (`POST /v1/sandboxes`, exec) authenticated only by a session cookie | 403 | v0.3 |
| ESC-23 | Cross-origin request to `/v1/events` and an exec output stream carrying a session cookie | Rejected | v0.3 |
| ESC-24 | Exec output containing `<script>`, HTML and ANSI escape tricks shown in the log viewer | Rendered as inert text (frontend test) | v0.3 |

### 12.5 Known limitations (documented honestly)

- Docker socket access makes Arcana root-equivalent on the host.
- With `runc`, isolation relies on the shared host kernel and is not a strong boundary.
- The `volume` workspace quota is enforced by polling, so short bursts can exceed it between checks.
- Snapshots capture `/workspace` only.
- Resource contention between sandboxes on one host (noisy neighbours) is reduced, not eliminated.
- Side-channel attacks are out of scope.

## 13. Configuration

Default path `/etc/arcana/config.yaml`, overridable with `--config`. Environment overrides use `ARCANA_` plus the key path with `__` as separator (for example `ARCANA_SERVER__LISTEN=0.0.0.0:7070`).

```yaml
server:
  listen: 127.0.0.1:7070
  metrics_listen: 127.0.0.1:9090
  shutdown_timeout_seconds: 30
  destroy_on_shutdown: false

data_dir: /var/lib/arcana

runtime:
  driver: docker
  docker:
    host: unix:///var/run/docker.sock
    oci_runtime: runsc            # runsc | runc
    allow_insecure_runtime: false # must be true to use runc

limits:
  max_sandboxes: 20
  max_sandboxes_per_key: 10
  max_ttl_seconds: 86400
  create_per_minute_per_key: 30   # v0.2
  defaults:
    cpus: 1
    memory_mb: 1024
    disk_mb: 2048
    pids: 256
    ttl_seconds: 1800
    idle_timeout_seconds: 600
  maximums:
    cpus: 4
    memory_mb: 8192
    disk_mb: 20480
    pids: 4096
  exec:
    default_timeout_seconds: 120
    max_timeout_seconds: 3600
    max_output_bytes: 10485760
    max_concurrent_per_sandbox: 8
    tail_buffer_bytes: 262144      # v0.3, dashboard output tail per exec
    tail_retention_seconds: 300    # v0.3
    max_tail_subscribers: 10       # v0.3
  files:
    max_file_bytes: 52428800
    max_archive_bytes: 524288000
    allowed_roots: [/workspace, /tmp]

workspace:
  backend: volume                 # volume | tmpfs
  check_interval_seconds: 15

network:                          # allowlist mode is v0.2
  default_mode: none              # none | allowlist
  subnet_pool: 10.200.0.0/16
  subnet_prefix: 28
  max_allow_entries: 50
  proxy_image: ghcr.io/brendatamaar/arcana:latest
  proxy_dns: [1.1.1.1, 1.0.0.1]   # outbound proxy only; operator configurable
  presets:
    python: [pypi.org, files.pythonhosted.org]
    node: [registry.npmjs.org]
    go: [proxy.golang.org, sum.golang.org, storage.googleapis.com]

templates:
  - name: base
    image: ghcr.io/brendatamaar/arcana-base:latest
    warm_pool: 0
  # v0.2 adds python, node, go with presets and warm_pool sizes

snapshots:                        # v0.3
  max_per_key: 20
  max_total_bytes: 10737418240
  max_fork: 10

audit:                            # v0.2
  retention_days: 30

ui:                               # v0.3
  enabled: true
  session_ttl_seconds: 43200
  allowed_origins: []             # extra origins if served behind another hostname
  event_buffer: 1000

log:
  level: info                     # debug | info | warn | error
  format: json                    # json | text
```

Until official images are published (v0.2), the `base` template may point at a public image such as `debian:stable-slim`; Arcana runs it as UID 1000.

## 14. Distribution and installation

| Channel | Release | Details |
|---|---|---|
| `go install` | v0.1 | `go install github.com/brendatamaar/arcana/cmd/arcana@latest` (no web dashboard, see FR-WD-10) |
| GitHub Releases | v0.2 | GoReleaser: binaries (amd64, arm64), checksums, `.deb`, `.rpm`, changelog; from v0.3 the dashboard is built and embedded first |
| Install script | v0.2 | `curl -fsSL https://raw.githubusercontent.com/brendatamaar/arcana/main/scripts/install.sh \| sh` — detects arch, downloads, verifies checksum, installs binary, writes default config, optionally installs systemd unit, runs `doctor` |
| Container image | v0.2 | `ghcr.io/brendatamaar/arcana` (also used as the proxy image) |
| Docker Compose | v0.2 | `deploy/compose/`: Arcana + Prometheus + Grafana with provisioned dashboard |
| systemd | v0.2 | `deploy/systemd/arcana.service` with a dedicated `arcana` user in the `docker` group |
| Devcontainer | v0.3 | Best-effort Codespaces support; falls back to runc with a clear warning if gVisor cannot run |
| Helm | Stretch | Single-node DaemonSet-style chart |

gVisor installation is documented (apt repository, then `sudo runsc install` and a Docker restart) and checked by `doctor`.

## 15. Observability

Metrics (prefix `arcana_`):

| Metric | Type | Labels |
|---|---|---|
| `sandboxes_active` | gauge | `template`, `runtime` |
| `sandbox_create_seconds` | histogram | `source` (pool, cold, snapshot) |
| `sandboxes_destroyed_total` | counter | `reason` |
| `exec_duration_seconds` | histogram | `template` |
| `execs_total` | counter | `result` (ok, nonzero, timeout, cancelled, error) |
| `pool_available` | gauge | `template` |
| `egress_requests_total` | counter | `decision` |
| `api_requests_total` | counter | `route`, `code` |
| `api_request_seconds` | histogram | `route` |
| `reconcile_orphans_total` | counter | `kind` (container, volume, network) |
| `ui_sessions_active` | gauge | — |
| `event_subscribers` | gauge | `stream` (events, exec_output) |

A Grafana dashboard JSON lives in `deploy/grafana/` and is provisioned by the Compose setup.

## 16. Success metrics

| Area | Metric |
|---|---|
| Security | 100% of escape suite passing in CI on gVisor for every merged PR |
| Performance | Published benchmark results versus §8.2 targets, including misses with explanation |
| Usability | Fresh Ubuntu 24.04 VM to first successful exec in ≤ 10 minutes following the README |
| Diagnostics | `doctor` identifies every missing prerequisite in the install test matrix |
| Agent demo | Reference agent pass rate reported per model on the 10-fixture set; parallel mode shows improvement over single attempt |
| Showcase | README with recording, dashboard shown in the demo video, docs site, benchmark page, at least three technical blog posts |

## 17. Risks and open questions

| # | Risk / question | Mitigation / plan |
|---|---|---|
| R1 | gVisor's rootfs overlay may hide in-sandbox writes from Docker (`docker cp`, `docker commit`) | Design avoids both: files and snapshots go through exec; verify byte-identical exec read/archive round trips in spike T0.4 |
| R2 | Docker exec cannot signal processes | `sbx-exec` helper (§9.4); verify under runsc and runc in spike T0.3 |
| R3 | Volume disk quotas not portable | Watchdog plus optional tmpfs backend; documented as soft limit |
| R4 | Docker network address pool exhaustion | Own subnet allocator with small prefixes (FR-NW-5) |
| R5 | Bind-mounting a host binary into gVisor sandboxes may behave differently | Verify in spike T0.3; fallback: copy helper into the sandbox at create via exec + tar |
| R6 | Warm pool sandboxes cannot change network mode after start | Pool keyed by template and network mode; incompatible requests cold-start |
| R7 | GitHub Actions runners' support for runsc or KVM may change | CI installs runsc each run; Firecracker tests optional and skipped when `/dev/kvm` is absent |
| R8 | Codespaces may not support runsc | Best effort with clear fallback messaging |
| R9 | Embedding the frontend could make `go build` depend on Node | Dashboard optional at build time with a placeholder page (FR-WD-10); release builds include it |
| Q1 | Final project name | Resolved 2026-10-13: **Arcana** |
| Q2 | Need for scoped keys (read-only, per-template)? | Defer; revisit after v0.3 feedback |
| Q3 | Remote snapshot storage (S3-compatible) | Out of scope for v0.3; interface left open |

## 18. Glossary

| Term | Meaning |
|---|---|
| Sandbox | An isolated, ephemeral environment (one container or microVM) owned by an API key |
| Template | Named image plus defaults used to create sandboxes |
| Warm pool | Pre-started sandboxes waiting to be handed out |
| Exec | One command run inside a sandbox |
| Snapshot | Saved copy of a sandbox's `/workspace` plus its creation spec |
| Fork | Creating several sandboxes from one snapshot |
| runsc | gVisor's OCI runtime |
| Egress proxy | The component that enforces the outbound allowlist |
| Reconciliation | Matching real Docker objects to database state at startup and removing orphans |
| Dashboard | The read-only React web UI served at `/ui` |
| Session | A browser login for the dashboard, created from an API key and carried in a cookie |
