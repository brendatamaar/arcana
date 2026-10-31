# 0003: Exec-only files and workspace backends

Status: accepted. Date: 2026-11-01. Tasks: T0.4, T0.6.

Keep Docker named volumes as the default workspace backend, checked by an
in-sandbox `du` watchdog. Offer size-capped tmpfs for a hard memory-backed disk
boundary. All file operations and archive extraction execute inside the sandbox;
the host handles only opaque transfer streams. Never use docker cp, docker commit
or host volume paths for files or snapshots.

[Evidence](../spikes/003-workspace.md) includes binary NUL-containing file and
byte-identical compressed-archive round trips, denied rootfs writes, writable
temporary directories and ENOSPC at an 8 MiB tmpfs limit. The owner approved
replacing the forbidden-mechanism comparison with supported-path verification.

A 10k-file apparent-size scan cost 547–644 ms at 0.5 CPU on the experimental
host. Retain the configurable 15-second watchdog initially, with bounded scan
concurrency. This is still a soft volume quota: a burst can exceed the limit
before the next scan. Do not present it as a hard disk quota.

Fresh volume ownership must support UID 1000. The spike seeded ownership from
the image's `/workspace`; production provisioning must establish writable
ownership without changing the non-root sandbox default or touching host volume
contents. Snapshots remain workspace-only and are never extracted on the host.
