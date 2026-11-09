# CPU accounting verification blocker

Date: 2026-11-09. Blocking task: T2.5. Runtime: runsc release-20261026.0.

## Observed failure

The unchanged full escape suite failed ESC-03 twice during T2.4/T2.5 verification.
The latest T2.5 run reported guest CPU time 1510 ms over 3002 ms wall time,
exceeding the existing 1500 ms ceiling. The attack, ceiling and peer-health
assertions have not been modified. T2.5 make check and full integration passed.

## Controlled diagnostic

A temporary runsc container used network=none, UID 1000, readonly root, all
capabilities dropped, no-new-privileges, PID/memory/swap bounds, and --cpus=0.25.
The existing escape CPU probe was bind-mounted readonly. Docker reported
NanoCpus=250000000 in all trials. Container CPU usage was measured from Docker
cgroup-backed stats immediately before and after the probe. The diagnostic
container and its temporary files were removed. No runtime configuration changed.

| Trial | Guest CPU ms | Guest wall ms | Container CPU ms |
|---|---:|---:|---:|
| 1 | 1340 | 3000 | 765.32 |
| 2 | 1200 | 3029 | 775.34 |
| 3 | 1160 | 3020 | 775.45 |

These measurements suggest guest getrusage is not a reliable substitute for
physical cgroup CPU accounting in this runtime. They do **not** establish the
physical usage of the earlier failing suite run, which was not instrumented.

## Verification change — owner approved 2026-11-09

Use the existing test-container cgroup-path validator and bounded kernel-file
reader to collect cpu.stat before starting the same public API CPU attack and
after completion. Inspect NanoCpus/cpu.max to verify the requested quota. Assert:

- The same CPU attack completes successfully and reports positive guest CPU time
  and at least 2500 ms of wall time.
- The original peer command remains healthy within its existing two-second bound.
- The container's cgroup CPU usage delta stays within the existing 1500 ms ceiling.
- Its configured quota remains 0.25 CPU and its throttling counter increases.

Keep guest CPU timing as a diagnostic alongside the authoritative counters. Do
not raise a ceiling, disable an attack, skip an assertion of quota containment,
or change the sandbox's default resource policy. If the cgroup checks fail, fix
the runtime/implementation before proceeding.

The owner approved this replacement, now applied to the escape test. AGENTS.md
requires a blocked task when verification cannot be completed without a safe
implementation fix; verification must pass before work continues to T2.6/T2.7.
