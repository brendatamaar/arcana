# Changelog

## [Unreleased]

- Add public-API escape suite v1 and CI with required runsc, resource-denial evidence and peer/host health checks.

- Add doctor diagnostics with JSON/fix hints and startup gating, including actual hardened runtime/helper cleanup proof.

- Add remote sandbox, exec and files CLI commands with tables/JSON, environment credentials, TTY/stdin and real remote exit codes.

- Add the public Go client with typed v0.1 methods, bounded file transfers, API errors and context-owned streaming exec.
- Fix successful HTTP file uploads being cancelled by an expired read deadline after EOF.

- Add TTL/idle reaping, supervised volume watchdog, startup crash reconciliation and optional sandbox destruction on shutdown.

- Add binary-safe files API with atomic bounded writes, reads, directory listings, deletion and sandbox-only archive transfer.

- Add WebSocket exec with binary-safe output, interactive stdin and TTY resize, bounded queues, keepalives and cancellation on disconnect.

- Fix runsc controller-disappearance races while retaining required process cleanup proof.

- Add supervised sync exec with real exit codes, combined output caps, UTF-8 sanitization, concurrency limits and host-side cleanup deadlines.

- Add sandbox lifecycle endpoints with template/resource overrides, atomic quotas, ownership filtering, keepalive and persisted transition history.

- Add the production exec helper with detached-process cleanup, bounded atomic status and checksum-verified embedded static binaries.

- Add the hardened Docker/runsc driver, bounded exec streaming, volume/tmpfs workspaces and a minimal locally built base image.

- Add the runtime driver contract and a bounded, scripted fake for deterministic orchestration tests.

- Add the v0.1 OpenAPI contract, authenticated HTTP server and serve command with bounded requests and graceful shutdown.

- Add hashed API keys, one-time private admin bootstrap, bearer/admin middleware and local key-management commands.

- Add persistent SQLite state with embedded migrations, bounded generated queries and a stable installation ID.

- Add structured logging with redaction, cryptographic resource IDs and consistent API error responses.

- Add validated YAML configuration, nested environment overrides and the `--config` flag.

- Add the repository scaffold and `arcana version` command.
