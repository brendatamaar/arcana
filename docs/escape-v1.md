# Escape suite v1

Run `make test-escape` on a Linux Docker host with runsc. The suite issues attacks
through public REST/WebSocket endpoints. It checks API liveness, a separate
sandbox's exec, and Docker host responsiveness after each attack, including when
an attack assertion fails. Containers are scoped to a fresh installation ID and
always scheduled for cleanup.

| IDs | Evidence required |
|---|---|
| ESC-01 | Container PID-denial counter increases with no observed OOM kills; helper completion or fail-closed sandbox destruction; peer/API/host remain healthy |
| ESC-02 | Docker OOM event or runtime allocation failure, without a successful unbounded allocation |
| ESC-03 | CPU usage stays below 1.5 CPU seconds during a 3-second spin at 0.25 CPU; peer exec completes within 2 seconds after the spin-start marker |
| ESC-04 | Explicit ENOSPC on tmpfs; terminal `destroyed/disk_quota` for a named volume |
| ESC-05 | Writes to `/etc` and `/usr` fail with filesystem/permission denial |
| ESC-06 | Literal and URL-encoded traversal returns `400 invalid_path` |
| ESC-07 | A host-only sentinel cannot be read through a symlink; `/etc/passwd` resolves to the sandbox copy |
| ESC-08 | Timeout reports exit 137 and `timed_out`; both recorded parent and child PIDs are absent afterward |
| ESC-09 | Flood output is truncated at 4096 captured bytes, times out, and retained heap growth stays within 16 MiB |
| ESC-10 | Raw ICMP socket creation is denied |
| ESC-11 | Root-owned setuid fixture remains UID 1000 with `NoNewPrivs: 1` |
| ESC-12 | Mount, ptrace of the supervisor and module loading are denied or unavailable |
| ESC-13 | Both conventional Docker socket paths are absent |
| ESC-14 | Direct TCP, UDP and DNS egress fail |
| ESC-20 | Actual daemon SIGKILL/restart removes interrupted work and orphan resources, closes unknown exec status without inventing an exit code, and preserves a healthy peer |
| ESC-21 | Every sandbox/files/stream operation hides another key's resource with 404; foreign resources do not appear in lists |

The root-owned setuid executable exists only in the test image. The runtime still
enforces UID 1000, read-only rootfs, dropped capabilities, no-new-privileges and
network-none. Attacks have finite attempt, allocation, output and time bounds.
Host reads of container-specific cgroup counters are read-only; the host never
opens workspace or Docker volume files.

During VPS validation, runsc returned ENOMEM after 14 forked children while the
container PID-denial counter increased and its OOM-kill counter remained zero.
The PID test therefore requires kernel-counter evidence instead of treating a
particular errno as sufficient proof. If supervision becomes uncertain during
PID exhaustion, the existing fail-closed destruction policy must complete.

CI installs runsc using the [official apt repository instructions](https://gvisor.dev/docs/user_guide/install/),
registers the runtime, restarts Docker, and runs integration and escape targets.
`ARCANA_REQUIRE_RUNSC=1` makes missing runsc a failure in CI. Outside CI, tests
that require runsc explicitly skip when it is unavailable; runc results are not
presented as gVisor security evidence.

Cold-cache CI exposed a legacy fixture parsing bug: Docker image-pull progress
from stderr had been combined with container IDs. Docker helpers now parse
stdout only and retain stderr separately for failure diagnostics. No attack
assertions were removed or relaxed to accommodate that fixture correction.

Validation on 2026-11-03: Windows/Linux make check, full runsc integration and
escape targets, security-tagged lint and static Linux build passed. GitHub
[check and security jobs passed candidate 36708f9](https://github.com/brendatamaar/arcana/actions/runs/41578117012).
