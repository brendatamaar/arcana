# 0009: the containerd driver

Status: accepted, **not yet run against a real containerd**. Date: 2026-11-15. Task: T4.5. The owner
approved the containerd client dependency on 2026-11-15 and chose to build the driver unverified.

`internal/runtime/containerd` implements `runtime.Driver` directly on containerd, without the Docker
Engine in between, using the gVisor shim (`io.containerd.runsc.v1`) by default. It exists to take
`dockerd` out of the path for operators who already run containerd (Kubernetes nodes, for example) and
to make the driver interface prove it is not Docker-shaped. The host it was written on has a
containerd, but its socket is root-only and `sudo` needs a password, so nothing here has talked to one.

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
- **tmpfs workspaces only.** The default `volume` backend returns `Unsupported`. containerd has no volumes;
  a snapshotter-backed directory would be owned by root and unwritable by UID 1000 without a privileged
  step, and the volume watchdog assumes Docker volumes. The practical effect: `workspace.backend: tmpfs`
  (memory-backed, hard size limit) is required.
- **Not wired into `arcana serve`**, for the same reason as the Firecracker driver: the config, doctor,
  reconciliation and template checks are Docker-shaped.
- **Never run.** Container creation, the shim options, IO draining, metrics decoding (cgroup v2 only) and
  snapshot cleanup are compile-checked and reviewed, not exercised.

## Comparison with the Docker driver

This is a design comparison, not a measurement; no benchmark was run.

| | Docker driver | containerd driver |
|---|---|---|
| Layers between Arcana and the runtime | Docker Engine, then containerd, then the shim | containerd, then the shim |
| Root-equivalent access | The Docker socket | The containerd socket |
| Isolation | Same runtimes (runsc by default); the engine applies a default seccomp profile to runc | Same runtimes; hardening comes only from the OCI spec this driver writes |
| Workspace | Docker volume (default) or tmpfs | tmpfs only |
| Networking | `none` or allowlist with the egress proxy | `none` only |
| Images | Pulled and checked by the engine | Must already be in the containerd namespace |
| Operational fit | Anywhere Docker runs | Hosts that run containerd without Docker |

Removing `dockerd` should save a process and a hop per operation. Whether that is measurable next to the
runtime's own start-up cost (see the [runtime comparison](../runtime-comparison.md) for runc and runsc on the
Docker path) is unknown.

## Dependency

`github.com/containerd/containerd/v2` (Apache-2.0) grows the module graph from 314 to 455 entries
(`go list -m all`). Only `internal/runtime/containerd` imports it, and nothing imports that package yet, so the
`arcana` binary is unchanged until the driver is wired in.
