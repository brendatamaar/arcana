# Architecture

Arcana is one Go binary that turns a Linux host with Docker into a sandbox service. This page explains the
parts, how a request flows through them, and where the security boundaries are. The reasons behind the main
choices are in the [decision records](decisions.md).

## Overview

```text
 Agents / CLI / MCP / SDKs / dashboard (browser)
            |  HTTP + WebSocket + SSE   (loopback by default; TLS via a proxy)
            v
+-------------------------------------------------------------+
| arcana (single Go binary)                                   |
|                                                             |
|  api ---- auth ---- rate limit         (net/http, OpenAPI)  |
|   |                                                         |
|  orchestrator                                               |
|   |- lifecycle / state machine                              |
|   |- exec manager (sync + stream)                           |
|   |- files and snapshots (all through exec)                 |
|   |- warm pool                                              |
|   |- reaper, reconciler, workspace watchdog                 |
|   |                                                         |
|  runtime.Driver  ---  docker (runsc | runc)                 |
|  netpolicy: subnet allocator + egress proxy manager         |
|  store: SQLite    audit    metrics    events + output tails |
|  webui: embedded dashboard at /ui                           |
+-------------------------------------------------------------+
            | Docker Engine API (unix socket)
            v
+----------------------+     +----------------------+
| sandbox container    |     | egress proxy         |  one per allowlist sandbox
| gVisor, UID 1000     |---->| `arcana proxy`       |----> allowlisted hosts only
| /workspace (volume)  |     | internal + outbound  |
| /opt/arcana/sbx-exec |     | networks             |
+----------------------+     +----------------------+
```

The orchestrator talks to a `runtime.Driver` interface (create, destroy, exec, stats, list). The Docker driver
is the one implementation today; the interface keeps room for others. Everything else (files, snapshots,
cancellation) is built on exec, so any driver gets it.

## Life of a sandbox

A sandbox moves `creating` to `running`, then `destroying` to `destroyed`, or ends as `failed` or `expired`.
Every transition is stored with a timestamp and a reason (`ttl`, `idle`, `exec_deadline`, `disk_quota`,
`requested`, `lost`, ...), and the dashboard shows that history. Creating a sandbox reserves quota and a
database row first, then starts the container; if the daemon crashes between the two, startup reconciliation
finds the labelled container, volume or network and removes or fails it. Arcana never leaves orphans behind.

A background reaper destroys sandboxes past their absolute TTL or idle timeout (TTL wins), and a watchdog
measures each volume workspace and destroys sandboxes that exceed their disk quota.

## Running a command

1. The request is authenticated, the sandbox's ownership is checked (other keys get `404`, never `403`), and a
   per-sandbox concurrency slot is taken.
2. Arcana runs `sbx-exec run ...` inside the container. This small static helper starts the command in its own
   process group, becomes a child subreaper so detached descendants are adopted, and enforces the timeout by
   signalling the whole tree through pidfds. Docker's own exec API cannot signal processes, which is why the
   helper exists ([ADR 0002](decisions/0002-exec-helper.md)).
3. Output is captured up to a cap (extra output is discarded while the command keeps running, and the result is
   marked `truncated`). The same bytes feed an in-memory ring for the live dashboard, never the database.
4. The helper writes a status file with the real exit code and a cleanup marker. Arcana trusts a completion only
   if the exit code, marker and helper status agree; otherwise it destroys the sandbox. An independent host-side
   deadline, `timeout + 10 s`, destroys a sandbox whose command cannot be proven finished.

## Files and snapshots

All file operations execute **inside** the sandbox through exec: writes go through a bounded atomic helper,
reads stat the opened descriptor first, and archives are produced and extracted by `tar` in the sandbox. The host
never opens a workspace file and never extracts an archive, so a symlink in a workspace can only resolve to
paths inside that sandbox ([ADR 0003](decisions/0003-workspace-and-files.md)).

A snapshot is a gzip tar of `/workspace` streamed out through exec and stored with its size and SHA-256. A
restore re-hashes the archive, streams it into a fresh sandbox and extracts it there. A fork does this for N
sandboxes in parallel. Only the workspace is captured: not processes, not files outside it, not environment values.

## Networking

The default mode, `none`, gives a sandbox only a loopback interface. In `allowlist` mode each sandbox gets its
own internal Docker network with no route out, and one dedicated egress proxy attached to that network and to an
outbound one. The proxy allows only the sandbox's hostnames, resolves them itself, rejects private, loopback and
link-local addresses after resolution (blocking cloud metadata and DNS rebinding), and dials the address it
checked ([ADR 0004](decisions/0004-network-egress.md)). There is no unrestricted mode, and TLS is never intercepted.

## State and observability

SQLite (pure Go, WAL mode) stores API keys as hashes, sandboxes and their transitions, exec metadata (never
output), snapshots, sessions and the audit log. Prometheus metrics are served on a separate listener. Live views
use two in-process structures, both bounded: an event bus with a ring of recent events for resume, and per-exec
output rings that are dropped shortly after a command ends.

## Trust boundaries

| Boundary | What enforces it |
|---|---|
| Sandbox to host | gVisor user-space kernel, non-root user, no capabilities, read-only root, no Docker socket |
| Sandbox to network | no interface (`none`) or an internal network reachable only through the egress proxy |
| Sandbox to sandbox | per-sandbox networks and volumes; ownership checks on every API call |
| API caller to sandbox | API keys (hashed), admin and owner scoping, request and output limits |
| Browser to API | cookie sessions that can only read and only from same-origin pages, strict CSP |

Arcana needs the Docker socket, which is root-equivalent on the host: run it on a dedicated host or VM. The
[security model](security.md) lists the known limitations.
