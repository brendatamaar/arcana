# 0009: the containerd driver

Status: accepted; **run against a real containerd, nested in a container** (see Verification). Date:
2026-11-15. Task: T4.5. The owner approved the containerd client dependency and, later, the effort to
verify the driver for real.

`internal/runtime/containerd` implements `runtime.Driver` directly on containerd, without the Docker
Engine in between, using the gVisor shim (`io.containerd.runsc.v1`) by default. It exists to take
`dockerd` out of the path for operators who already run containerd (Kubernetes nodes, for example) and
to make the driver interface prove it is not Docker-shaped. It was first written without any containerd to
talk to (the host's socket is root-only and `sudo` needs a password). It was then run for real against a
containerd started inside a privileged container with its socket in a directory the test user owns
(`scripts/containerd-test.sh`), which found six defects that compile-checking could not.

## What was built

- **Engine seam.** The driver logic (validation, naming, labels, limits, list filtering, cleanup after a
  failed create) is separate from a narrow `engine` interface. The production engine is about 250 lines
  over the containerd v2 client; tests use a fake engine. That split is why the unit coverage is 43 percent:
  the engine, the one part that cannot be exercised without containerd, is the uncovered part.
- **Same guarantees as the Docker driver, as OCI spec edits.** UID/GID 1000, `no_new_privs`, an empty
  capability set, a read-only root filesystem, memory with swap equal to memory, CPU quota, a PID limit,
  `RLIMIT_NOFILE` 1024, tmpfs `/tmp` and `/run/sbx` (`/run/sbx` also `noexec`) and a tmpfs `/workspace`, and
  the host's `sbx-exec` bind-mounted read-only, the only host path. A test applies the options to a default
  spec and asserts each of these, so a changed or dropped option is caught.
- **`runc` needs the opt-in** (`allow_insecure_runtime`), as with Docker. It does not get a seccomp profile
  here; with Docker it would get the engine's default one. That is another reason to treat runc as
  development-only.
- **Labels and names** mirror the Docker driver (`arcana.managed`, `arcana.instance`, `arcana.sandbox_id`,
  container `sbx-<id>`), so reconciliation logic can be shared. `List` ignores other installations.
- **Exec** runs a process in the sandbox's task with in-process pipes, supports a terminal and resize, drains
  the IO before reporting the exit, and kills the process tree on a cancelled context.
- **Namespace.** Containers and images live in a containerd namespace (default `arcana`). Template images must
  be present in it (`ctr -n arcana images pull ...`); the driver does not pull.

## What it does not do

- **Network mode `none` only.** `allowlist` returns `runtime.Unsupported`: the per-sandbox networks and egress
  proxy (ADR 0004) are built on Docker networks.
- **Volume workspaces need a privileged Arcana** (see "Volume workspaces" below); a non-root, non-1000
  Arcana must use `workspace.backend: tmpfs`.
- **Template images** must already be in the containerd namespace; the driver does not pull.
- **Verified only nested.** containerd ran inside a privileged container, not on a bare host, and the
  server was containerd 2.2 (the client library is 2.4.2).

## Verification

`scripts/containerd-test.sh` (build tag `integration`) starts containerd in a throwaway privileged container,
mounts the host's gVisor files and the helper at the same paths, imports the template image, and runs
`TestContainerd_Lifecycle` for **runsc and runc**. It creates a sandbox and checks, from inside it: separate
stdout and stderr with the real exit code, the image environment and per-sandbox environment, UID 1000, an
empty capability set, `no_new_privs`, `RLIMIT_NOFILE` 1024, a read-only root and a read-only helper directory,
a writable tmpfs `/workspace` and `/tmp`, a 320 KB stdin round trip, a terminal with the requested size, a
cancelled context killing a `sleep 300`, stats, and idempotent destroy. It passes on both runtimes.

Running it found defects that the fake engine could not:

1. `oci.WithImageConfig` resolves a named image `USER` by mounting the rootfs in the client process, which a
   non-root client cannot do. The driver now applies only the image's environment and sets the numeric
   1000:1000 itself.
2. The container had no command, so it would not stay up: it now runs `/bin/sleep infinity`, as the Docker
   driver does. There is no tini-style init, so orphans of PID 1 are not reaped (the per-exec controller
   adopts its own descendants).
3. Exec uses named pipes in `/run/containerd/fifo`, which is root-only: `fifo_dir` is now configurable and
   created `0700`.
4. Closing a session's stdin only closed the local pipe; commands that read to EOF hung. It now also calls
   `CloseIO`.
5. The gVisor shim ignores the spec's console size, so a resize is sent after start.
6. The gVisor shim reports cgroup v1-shaped metrics, so stats decode both formats. Short Docker-style image
   names (`arcana-base:local`) are normalised to containerd's qualified names.
7. Found through the serve end-to-end run below: with the default cgroup path `/<namespace>/<id>`, destroying
   one gVisor sandbox failed with `removing cgroup path /sys/fs/cgroup/arcana: device or resource busy` while
   another sandbox ran, because gVisor tries to remove the shared parent. Each sandbox now gets a flat cgroup
   (`/arcana-<container id>`), and a test destroys one of two running sandboxes under both runtimes.

## Wired into `arcana serve`

`runtime.driver: containerd` is selectable (see the
[configuration reference](../configuration.md#drivers)). `internal/runtime/factory` builds the driver for both
the server and the doctor, and `arcana doctor` runs a driver-specific set of checks: the shared host checks, a
connection to containerd and one real hardened sandbox proving the helper and its process cleanup. A request
for `allowlist` networking is refused with `invalid_request`. `scripts/containerd-serve-e2e.sh` starts a real
Arcana on the throwaway containerd with gVisor and checks doctor, create, exec with a remote exit code, file
round trip, snapshot, fork, the refusal, and that sandboxes survive a restart and can then be destroyed.

## Volume workspaces

containerd has no named volumes, so the driver owns a host directory per sandbox:
`<data_dir>/containerd/workspaces/<sandbox id>`, created before the container, bind-mounted read-write with
`nosuid,nodev` at `/workspace`, and removed after the container is deleted. Size is a soft limit enforced by
the same `du` watchdog as Docker volumes (the orchestrator is backend-agnostic).

**Why it needs a privileged Arcana.** The sandbox user (UID 1000) must own `/workspace` and cannot chown
anything, so whoever creates the directory must be able to give it to UID 1000: root (`chown`) or UID 1000
itself. Any other user would also be unable to delete what the sandbox writes, so the driver refuses at create
time with that explanation and `arcana doctor` reports it (the check creates, chowns and removes a probe
directory). A snapshotter-backed directory would be root-owned and unwritable by UID 1000, and an
overlay-with-image-parent mount cannot mount only the image's `/workspace`; both were considered and dropped.
The parent directory stays `0700`, so no other host user can reach any workspace.

**Lifecycle.** `Create` refuses an existing directory (a sandbox never inherits another's files) and removes
its directory when container creation fails. `Destroy` deletes the container first and only then the
directory, so a running sandbox never loses it and nothing writes during removal; a failed removal is
returned, and the reaper retries. Removal handles directories the sandbox chmod'ed to 000 and never follows
symlinks. `PruneOrphans` (the same hook the Docker driver implements, run at startup before admission) removes
directories no kept sandbox owns, which covers a crash between creating the directory and the container.

**Verification.** Unit tests cover the plan, the lifecycle, refusal, orphan pruning and the doctor check.
`TestContainerd_VolumeWorkspace` runs on a real containerd with **runsc and runc**, as root (the script runs
that test binary inside the containerd container): the sandbox user writes, the file appears on the host owned
by UID 1000, the root filesystem stays read-only, `Destroy` removes a workspace with a 000 directory, and
pruning removes an orphan. Not covered: a quota-enforcing filesystem (the limit stays soft), and a host where
Arcana runs as UID 1000 rather than root (the ownership rule is unit-tested only).

## Comparison with the Docker driver

Design comparison first, then measurements.

| | Docker driver | containerd driver |
|---|---|---|
| Layers between Arcana and the runtime | Docker Engine, then containerd, then the shim | containerd, then the shim |
| Root-equivalent access | The Docker socket | The containerd socket |
| Isolation | Same runtimes (runsc by default); the engine applies a default seccomp profile to runc | Same runtimes; hardening comes only from the OCI spec this driver writes |
| Workspace | Docker volume (default) or tmpfs | Host directory (default; Arcana must be root or UID 1000) or tmpfs |
| Networking | `none` or allowlist with the egress proxy | `none` only |
| Images | Pulled and checked by the engine | Must already be in the containerd namespace |
| Operational fit | Anywhere Docker runs | Hosts that run containerd without Docker |
| Speed (runc, nested harness) | Baseline | About 2 to 3 times faster to create, exec and destroy |

### Measurements

`TestCompareWithDocker` (`ARCANA_COMPARE=1`) times the same lifecycle through both drivers with a tmpfs
workspace and no network: 15 recorded iterations after one warm-up, p50 / p95. Host: a shared 2 vCPU VM that
was busy with other workloads; **containerd ran nested in a privileged container, Docker on the host**, so
this favours Docker if anything.

| driver / runtime | create + start | exec of `true` | destroy |
|---|---|---|---|
| docker / runc | 220 ms / 282 ms | 72 ms / 88 ms | 130 ms / 135 ms |
| containerd / runc | 81 ms / 96 ms | 32 ms / 37 ms | 57 ms / 68 ms |
| docker / runsc | 214 ms / 246 ms | 43 ms / 47 ms | 106 ms / 138 ms |
| containerd / runsc | **5122 ms** / 5150 ms | 37 ms / 134 ms | 85 ms / 274 ms |

Two earlier runs with a different cgroup setup gave the same shape (Docker create 330 to 410 ms, containerd
runc create 130 to 170 ms), so the runc result is repeatable here: the containerd driver is about two to three
times faster than the Docker driver for create, exec and destroy on runc, plausibly because the Docker Engine
sits between Arcana and containerd.

**The 5.1 s for containerd with gVisor is not a driver result.** It appears with plain `ctr run -d` in the same
harness, so it comes from running containerd nested under Docker's cgroup delegation (it disappeared when the
cgroup controllers were not delegated, but then limits could not be applied). It could not be explained within
the time spent, so containerd's gVisor create latency is **not measured**; exec and destroy with gVisor are
comparable to Docker's. Repeat on a host that runs containerd directly before drawing any conclusion about
gVisor.

## Dependency

`github.com/containerd/containerd/v2` (Apache-2.0) grows the module graph from 314 to 455 entries
(`go list -m all`). Only `internal/runtime/containerd` imports it, and nothing imports that package yet, so the
`arcana` binary is unchanged until the driver is wired in.
