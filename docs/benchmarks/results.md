# Arcana benchmark results

- Started: 2026-11-12 08:09:26 UTC (took 118 s)
- Server: http://127.0.0.1:43467
- Runtime: runsc
- Template: base
- Benchmark client: linux/amd64, go1.27.3, 2 CPUs
- Host: 2 vCPU (AMD EPYC 7K62, shared VM), 1.9 GB RAM + swap, Docker 29.8.1, gVisor runsc, kernel 6.8; developer VM, not the PRD reference host
- Samples: 20 create/pool/snapshot, 100 exec; density cap 12 at 256 MB

| Metric | Samples | Failures | p50 | p95 | p99 | max | Target | Verdict |
|---|---|---|---|---|---|---|---|---|
| cold_create | 20 | 0 | 205.9 ms | 345.9 ms | 1233.5 ms | 1233.5 ms | cold create p50 ≤ 2 s (image cached) | met |
| pool_acquire | 20 | 0 | 5.6 ms | 8.4 ms | 10.4 ms | 10.4 ms | pool acquire p50 ≤ 150 ms and p99 ≤ 500 ms | met |
| exec_true | 100 | 0 | 94.3 ms | 108.1 ms | 130.9 ms | 135.7 ms | exec overhead for `true` p50 ≤ 100 ms | met |
| density | 12 | 0 | count 12 | | | | ≥ 20 idle 256 MB sandboxes per 8 GB host | not measured |
| snapshot_create | 20 | 0 | 569.1 ms | 599.8 ms | 602.0 ms | 602.0 ms | - | - |
| snapshot_restore | 20 | 0 | 811.1 ms | 978.3 ms | 979.1 ms | 979.1 ms | - | - |
| fork_2 | 5 | 0 | 1439.5 ms | 1564.5 ms | 1564.5 ms | 1564.5 ms | - | - |
| fork_4 | 5 | 0 | 2503.2 ms | 3067.0 ms | 3067.0 ms | 3067.0 ms | - | - |

- **density**: 12 idle sandboxes of 256 MB held at once; stopped: reached the --density-max cap of 12. Create latency is while filling.
- **snapshot_create**: 10 MiB of incompressible data in /workspace
- **snapshot_restore**: 10 MiB of incompressible data in /workspace
- **fork_2**: 10 MiB of incompressible data in /workspace; time until all members are running
- **fork_4**: 10 MiB of incompressible data in /workspace; time until all members are running

Not measured by this harness: Arcana idle RSS (≤ 80 MB target) and dashboard JavaScript size (≤ 200 KB gzip target).
