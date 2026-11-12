# Security model and limitations

This page summarises what Arcana does and does not protect against. The full
threat model and requirement IDs are in the
[PRD §12](PRD.md#12-security-and-threat-model); the executed attack suite is
described in [escape v1](escape-v1.md) and runs on every commit with gVisor.

## Isolation defaults

- gVisor (`runsc`) runtime; `runc` needs `allow_insecure_runtime: true` and is
  not a strong boundary for hostile code.
- Every sandbox runs as UID 1000 with a read-only root filesystem, all
  capabilities dropped, `no-new-privileges`, and memory, CPU, PID and disk limits.
- No network by default (`network.default_mode: none`).
- File operations and archive extraction run **inside** the sandbox; the host
  never reads workspace paths, uses `docker cp` or extracts sandbox archives.
- Per-key ownership: non-admin keys cannot see other keys' sandboxes (404).
- Egress allowlists use a per-sandbox proxy that validates every DNS answer and
  rejects private and reserved addresses; see [networking](networking.md).
- Metadata-only audit log; credentials and output are never recorded.

## Hardening the deployment

- **Docker socket access is root-equivalent on the host.** Run Arcana on a
  dedicated host or VM with nothing else of value.
- Keep the API on `127.0.0.1`. For remote access, terminate TLS in a reverse
  proxy and protect API keys; Arcana itself does not serve TLS.
- Keep the metrics listener on loopback or a private network.
- The systemd unit drops all capabilities, makes the filesystem read-only
  except `/var/lib/arcana`, and filters syscalls and address families.
- Keep `data_dir` at `0700`; it holds the key database.
- Run `arcana doctor` after Docker, kernel or gVisor upgrades.

## Known limitations

- Hostile code still shares a host kernel with Docker; gVisor shrinks but does
  not remove that attack surface. Zero-days beyond gVisor's boundary and
  hardware side channels are out of scope.
- The `volume` disk quota is enforced by polling, so short bursts can exceed it.
  `tmpfs` is a hard limit but consumes RAM.
- Commands in one sandbox share a UID, filesystem and process visibility.
  Background work must stay attached to its exec; detached descendants are
  reaped when it ends.
- Noisy-neighbour effects between sandboxes are reduced, not eliminated.
- Failed archive extraction can leave partial files in the sandbox.
- Snapshots contain whatever the sandbox wrote to `/workspace`, including any
  secrets it held there, and are stored unencrypted in `data_dir/snapshots`
  (`0600`). Protect and back up `data_dir` accordingly. They are visible only to
  the creating key and admin keys, and an archive is hash-verified before it is
  restored. Snapshots capture the workspace only; they do not capture running
  processes, files outside `/workspace`, or environment values.
- Audit events are queued and can be lost in a crash before they are persisted.
- Only ports 80 and 443 are supported for allowlisted egress, without TLS
  interception.

Report suspected vulnerabilities to the maintainers privately instead of opening a public issue.
