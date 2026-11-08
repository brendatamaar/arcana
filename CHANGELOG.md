# Changelog

## [Unreleased]

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

[Unreleased]: https://github.com/brendatamaar/arcana/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/brendatamaar/arcana/tree/v0.1.0
