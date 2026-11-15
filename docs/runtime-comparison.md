# Runtime comparison: runc, runsc and Firecracker

Arcana can put a sandbox behind three isolation layers. This page compares what we could measure and
says plainly what we could not. **Firecracker has no numbers here**: the host has no `/dev/kvm`, and the
Firecracker driver ([ADR 0007](decisions/0007-firecracker-driver.md)) has never booted a real VM.

## Short version

- **runc** shares the host kernel. It is the fastest and, for many operations, the closest to a plain
  container, but it is not a security boundary for hostile code. Arcana refuses it unless you opt in.
- **runsc (gVisor)** is Arcana's default. In these runs pool acquire was the same, cold create was equal in
  one run and slower on runsc in the other (unresolved, see below), starting a command was somewhat *faster*,
  and restoring a snapshot and forking were **two to five times slower**.
- **The escape suite passes on runc as well as runsc.** That is a finding, not a reassurance: the suite
  checks Arcana's hardening, limits and cleanup, which apply to both runtimes. It does not attack the
  kernel, which is where the runtimes differ. Do not read a passing runc run as "runc is safe".
- **Firecracker** would give each sandbox its own kernel behind hardware virtualization. Whether the cost is
  worth it for your threat model, and what it costs in startup and density, is unmeasured.

## What was compared

| | |
|---|---|
| Host | 2 vCPU shared VM (Intel Xeon Platinum 8255C), 3.7 GB RAM, kernel 6.8 |
| Software | Docker 29.8.1, runc 1.5.1, runsc release-20261026.0 (systrap platform), Arcana at commit 1e5ef5a |
| Template | `base`, warm pool of 5, hardening flags and limits identical for both runtimes |
| Client | `arcana bench` on the same machine, 20 samples per scenario (100 for exec, 5 per fork) |
| Load | **Heavy and uncontrolled.** Other processes ran on the VM; the load average was about 5 before run 1 and 5 to 8 during run 2 on a 2 vCPU host |

Each runtime was run twice, in opposite order (runsc then runc, then runc then runsc), to see how much
the order and the background load matter. Raw data:
[run1-runsc](https://github.com/brendatamaar/arcana/blob/main/docs/benchmarks/runtime-comparison/run1-runsc.json),
[run1-runc](https://github.com/brendatamaar/arcana/blob/main/docs/benchmarks/runtime-comparison/run1-runc.json),
[run2-runc](https://github.com/brendatamaar/arcana/blob/main/docs/benchmarks/runtime-comparison/run2-runc.json),
[run2-runsc](https://github.com/brendatamaar/arcana/blob/main/docs/benchmarks/runtime-comparison/run2-runsc.json).

## Results

p50 / p95 per run. Lower is better. The density scenario was capped at 12 sandboxes in all four runs (the
host is small), so it says nothing about density.

| Metric | runc run 1 | runc run 2 | runsc run 1 | runsc run 2 |
|---|---|---|---|---|
| Cold create | 244 ms / 366 ms | 324 ms / 579 ms | 250 ms / 444 ms | 464 ms / 630 ms |
| Pool acquire | 8 ms / 25 ms | 9 ms / 14 ms | 8 ms / 14 ms | 9 ms / 14 ms |
| Exec of `true` | 246 ms / 344 ms | 319 ms / 429 ms | 194 ms / 257 ms | 244 ms / 334 ms |
| Snapshot create (10 MiB) | 881 ms / 1.01 s | 1.04 s / 1.12 s | 1.12 s / 1.37 s | 1.11 s / 1.27 s |
| Restore into a new sandbox | 854 ms / 1.18 s | 1.13 s / 1.33 s | 2.32 s / 2.84 s | 2.18 s / 2.72 s |
| Fork of 2 | 1.65 s / 1.79 s | 1.90 s / 1.93 s | 4.90 s / 6.69 s | 5.31 s / 6.94 s |
| Fork of 4 | 2.92 s / 3.35 s | 3.63 s / 3.80 s | 9.27 s / 16.68 s | 16.88 s / 18.55 s |

### Reading the numbers

- **Robust across both orders:** restore and forks are clearly slower on runsc (restore 2.2 to 2.3 s against
  0.85 to 1.1 s; fork of 4 about 9 to 17 s against 3 to 3.6 s), and exec of `true` is about 20 to 25 percent
  faster on runsc in both orders. These differences are larger than the run-to-run drift.
- **Not distinguishable here:** pool acquire (single-digit milliseconds on both, because acquisition only
  hands over an already running instance) and snapshot creation (about 0.1 to 0.2 s in runc's favour, within
  the spread between runs).
- **Unresolved:** cold create. It was equal in run 1 (244 against 250 ms) but 43 percent slower on runsc in
  run 2 (324 against 464 ms), which ran second on a more loaded host. Both runtimes were slower in run 2
  than in run 1, so load and order explain at least part of it; this data cannot say how much.
- **Exec missed its target on both runtimes** (PRD §8.2: p50 at most 100 ms). The previous run on a quieter,
  different host ([benchmarks](benchmarks.md)) measured 94 ms on runsc. This host was busy, so treat the
  miss as a statement about this machine under load, not about either runtime.
- **Why restore and fork are slower on runsc is not established.** Restore streams a compressed archive
  through exec into `tar` inside the sandbox, so file I/O and process start-up inside gVisor are the
  likely cause, but nothing here profiled it.
- Absolute numbers shift by tens of percent between runs; compare shapes, not digits. Repeat on a quiet
  reference host before quoting figures.

## Security

### The escape suite on each runtime

`make test-escape` runs the attacks of PRD §12.4 (ESC-01 to ESC-23; ESC-24 is a dashboard frontend test) through the public API and checks that Arcana, a peer
sandbox and Docker stay healthy. It passes on **runsc** (the supported configuration, in CI). For this
comparison it was also run on **runc** with `ARCANA_ESCAPE_RUNTIME=runc`, a mode that logs
"COMPARISON RUN" and that security CI refuses. Every test passed on runc too (101 s).

That is expected once you look at what the attacks are: a fork bomb, memory bomb, CPU spin, disk fill,
writes outside the workspace, path traversal, a symlink to a host path, a SIGTERM-ignoring tree, an output
flood, raw sockets, a setuid binary, `mount`/`ptrace`, the Docker socket, network in `none` mode, the proxy
and DNS tricks, a daemon crash, and cross-key access. Arcana's flags (all capabilities dropped,
`no-new-privileges`, a read-only root, PID, memory and CPU limits, internal networks) and its own checks
contain these on either runtime. What the suite does **not** contain is a kernel exploit, which is the
attack gVisor and a microVM exist to stop. No test in the suite tells the two runtimes apart, so
none of them should be read as evidence that runc is safe against hostile code.

### Isolation model

| | runc | runsc (gVisor) | Firecracker |
|---|---|---|---|
| What stands between sandbox code and the host kernel | Namespaces, capabilities, seccomp; the code's system calls reach the host kernel | A user-space kernel (the Sentry) handles system calls and talks to the host through a much smaller interface | Hardware virtualization: the code runs in a guest with its own kernel |
| A guest or kernel bug yields | Host kernel compromise | A Sentry compromise, which is then confined by the Sentry's own sandbox | A VMM or KVM escape, confined by the jailer |
| Needs | Docker | Docker with runsc installed | `/dev/kvm`, a Firecracker kernel and rootfs, root for the jailer |
| In Arcana today | Opt-in (`allow_insecure_runtime`), development and CI only | Default | A driver exists; not wired into `arcana serve`; unverified on KVM |

This table states the design of each layer, not a measured result. The Firecracker column describes
what the driver is built to do, and none of it has been observed.

## Firecracker: what is missing

Nothing in the table above for Firecracker was measured, and nothing about its startup time, memory
footprint or density should be inferred from this page. To change that, someone needs a Linux host with
`/dev/kvm` (a bare-metal machine or a VM with nested virtualization) and these steps:

1. Install Firecracker and its jailer, and provide a Firecracker-compatible kernel (`vmlinux` with
   virtio-blk, vsock and ext4 built in).
2. `make envd arcana-init`, then `sudo scripts/build-rootfs.sh arcana-base:local <rootfs_dir>/arcana-base_local.ext4`.
3. Run `TestRealFirecracker` (`go test -tags integration -run TestRealFirecracker ./internal/runtime/firecracker/`)
   with `ARCANA_FC_FIRECRACKER`, `ARCANA_FC_JAILER`, `ARCANA_FC_KERNEL`, `ARCANA_FC_ROOTFS_DIR`,
   `ARCANA_FC_STATE_DIR` and `ARCANA_FC_IMAGE` set. It has never passed or failed; it only skips.
4. Wire the driver into `arcana serve` (not done), then repeat the benchmark run above and extend the
   escape suite with the cases a microVM should add (for example a guest-kernel attack surface test).

Until step 4, no Firecracker benchmark through the Arcana API is possible, because the driver is not
selectable.

## Recommendation

Keep runsc as the default: on this evidence it costs little for acquire and exec, its cold-create cost
is unresolved, and its extra cost for snapshot restore and fork is a known trade. Use runc only for development. Treat Firecracker as
promising but unproven in this repository, and decide on it only after the steps above produce numbers.

## Reproduce

```sh
make build && make templates
for rt in runsc runc; do
  ARCANA_OCI_RUNTIME=$rt ARCANA_LIVE_WARM_POOL=5 \
  ARCANA_LIMITS__CREATE_PER_MINUTE_PER_KEY=10000 ARCANA_LIMITS__MAX_SANDBOXES=40 ARCANA_LIMITS__MAX_SANDBOXES_PER_KEY=40 \
  scripts/with-arcana.sh bin/arcana bench --scenario all --density-max 12 --out bench/$rt --host-note "<your host and $rt>"
done
ARCANA_ESCAPE_RUNTIME=runc make test-escape   # comparison only
```
