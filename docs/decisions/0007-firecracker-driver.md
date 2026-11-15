# 0007: the Firecracker driver

Status: accepted, **not yet run on a real KVM host**. Date: 2026-11-15. Tasks: T4.2.

`internal/runtime/firecracker` implements `runtime.Driver` on Firecracker microVMs, with
[`envd`](0006-envd.md) inside each guest. A microVM gives each sandbox its own kernel, a
stronger boundary than gVisor's user-space kernel, at the cost of needing `/dev/kvm`.
Everything below was written and tested without KVM: the plan, API calls, vsock handshake,
staging and the whole driver run against a fake VMM that serves Firecracker's API and
proxies vsock to a real `envd`; booting a real guest is the one thing unproven.

## What a VM looks like

- **Drives.** A shared, read-only ext4 root (`/dev/vda`, one image per template, built by
  `scripts/build-rootfs.sh`) and a per-VM workspace image (`/dev/vdb`): a sparse ext4 file
  of `disk_mb`, created empty by `mke2fs` and owned by UID 1000. With the `tmpfs`
  workspace backend there is no second drive and the guest mounts a size-capped tmpfs.
  Nothing writable is ever shared between sandboxes, and the host never reads workspace
  files: it only creates the empty filesystem.
- **Guest init** (`cmd/arcana-init`, PID 1): mounts `/proc`, `/sys`, `/dev`, `/dev/pts`,
  tmpfs `/tmp` and `/run` and `/workspace` (all `nosuid`; `/run` also `noexec`), brings up
  loopback, applies `RLIMIT_NPROC` (per UID, which is the one sandbox user, so it plays the
  role of the container PID limit) and `RLIMIT_NOFILE`, then runs `envd serve --listen
  vsock:1024 --user 1000:1000`. When `envd` ends it powers the VM off. The template
  image's `ENV` is captured into `/etc/arcana/env` at build time, because a filesystem
  export drops it and the templates rely on it (FR-RL-4).
- **Resources.** vCPUs are `ceil(cpus)`; a fractional or non-integral quota is enforced by
  the jailer's `cpu.max`. Guest RAM is `memory_mb`; `memory.max` is that plus 128 MiB of
  VMM overhead, with swap off. `pids.max` bounds the VMM's own threads. Kernel arguments are
  fixed (`ro`, `nomodules`, `init=/sbin/arcana-init`, the `arcana.*` settings); operator
  arguments are appended but cannot supply an `arcana.*` setting.
- **Network.** Only mode `none` (no network device at all). `allowlist` returns
  `runtime.Unsupported`; it needs the egress proxy design adapted to tap devices (below).
- **Exec.** `Driver.Exec` opens an `envd` `Exec` stream and adapts it to `ExecSession`
  (binary-safe, chunked stdin, TTY and resize; a cancelled context disconnects the stream,
  which makes `envd` kill the whole tree). The orchestrator is unchanged: it still wraps
  commands with `/opt/arcana/sbx-exec`, which the rootfs links to `envd` (which contains all
  of `sbx-exec`). The nested controller is redundant; using `envd`'s native exec from the
  orchestrator is a follow-up.

## Confinement of the VMM

The default is the **jailer**: a chroot per VM, a dropped unprivileged UID/GID (root is
refused), `cgroup v2` limits, and the file-descriptor limit. Jailed VMs get hard links to
the kernel and the shared rootfs after the API socket appears, so the jail directory must
be on the same filesystem as both. Running Firecracker without the jailer needs
`allow_unjailed` and is named `firecracker/unjailed`, mirroring `allow_insecure_runtime`.
The driver must run as root to use the jailer; Firecracker's default seccomp filter is left
on. The vsock socket is the host's only channel to the guest and `envd` accepts only the
host CID.

## Why no firecracker-go-sdk

The plan names the SDK and AGENTS approves it. The driver instead uses six JSON `PUT`s
over the API socket with the standard library. The SDK is v1.0.0 (2022), grows the
module graph from 314 to 569 entries (`go list -m all`; go-openapi, the MongoDB driver,
OpenTracing, CNI among them), and its jailer chroot handling is exactly the part that
cannot be checked here. The
API calls are covered by tests against a fake that asserts their order and bodies. If a
later feature (snapshots, balloon, rate limiters) needs more of the API, adding the SDK is
still open.

## Lifecycle and recovery

Each VM has a directory under `state_dir` with a `meta.json` (install ID, sandbox ID,
labels, and the VMM's PID with its start time, so a recycled PID is never signalled). `List`
reads those, `Destroy` is idempotent and kills the exact process, and a restarted driver
adopts running VMs (`Exec` reconnects over vsock). Environment values are held in memory
only, so an adopted VM runs without the sandbox-level `env`. A failed boot kills the VMM and
removes its files.

## Not done

- **Not verified on KVM.** A real boot, the jailer layout and its cgroup names, hard-link
  staging, the guest init, privilege drop with `envd` running as root, and vsock traffic
  from the host have never run. `TestRealFirecracker` (build tag `integration`) boots a VM
  when `/dev/kvm` and `ARCANA_FC_*` variables are set and otherwise skips with a message.
- **Allowlist networking** (tap devices, per-VM subnets, the proxy) and **memory
  snapshots** are not implemented. Memory snapshots have no consumer: snapshots capture the
  workspace only (FR-SN-2), and restoring a VM needs its own design for vsock paths and
  identity.
- **Not wired into `arcana serve`.** `runtime.driver` still accepts only `docker`; serving,
  reconciliation, the doctor and template images are Docker-shaped. The driver is a
  package for now.
- **Kernel and rootfs provisioning** are the operator's job: a Firecracker-compatible
  `vmlinux` with `virtio-blk`, `vsock` and ext4 built in, and rootfs images from
  `make envd arcana-init && sudo scripts/build-rootfs.sh <image> <rootfs_dir>/<name>.ext4`,
  where `<name>` is the image reference with every character outside `a-z0-9.-` replaced by
  `_` (`arcana-python:local` becomes `arcana-python_local.ext4`).
