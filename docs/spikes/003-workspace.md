# Workspace backends and exec transfer

Task: T0.4. Same runsc VPS as [T0.2](001-runsc-exec.md).

`go test -race -v -tags=integration -run TestWorkspace ./test/integration`
passed, as did `make check test-integration test-escape`.

| Experiment under runsc | Result |
|---|---|
| Named volume mounted at `/workspace`, UID 1000 | writable with image-seeded ownership |
| Binary file containing NUL read through exec | byte-identical |
| tar downloaded, streamed into second directory, downloaded again | byte-identical compressed archive |
| Restored binary file read through exec | byte-identical |
| Rootfs write to `/etc` | denied (EACCES as non-root) |
| Writes to tmpfs `/tmp` and `/run/sbx` | successful |
| 12 MiB write into 8 MiB workspace tmpfs | ENOSPC; both sandboxes and Docker healthy |

Archive extraction occurs only inside the sandbox with `tar -xzf -`; the test
holds opaque archive bytes in memory and never extracts them on the host.
The owner approved replacing the original docker cp/docker commit comparison
with these supported-path checks. Neither forbidden mechanism was invoked.

## Ownership and reproducibility

The test builds a fixture from `debian:stable-slim` whose `/workspace` is owned
by UID/GID 1000. Docker initializes the fresh named volume from that image
directory. Do not assume a fresh volume over a missing or root-owned image
directory is writable by UID 1000. Production templates/runtime provisioning
must establish ownership while preserving the non-root sandbox requirement;
the spike did not read/chown Docker volume paths on the host or run the
sandbox as root. All temporary volumes and containers are removed by exact ID.

## Watchdog cost

10,000 files containing one byte each were created inside the volume through
exec. Five in-sandbox timings surround `du -sb /workspace/tree` with
`date +%s%N`; this includes the small date-process overhead, not Docker exec
round-trip latency. CPU quota: 0.5 CPU. Reported apparent content: 10,000 bytes.

| Sample | Duration |
|---|---:|
| 1 | 641.621 ms |
| 2 | 547.412 ms |
| 3 | 644.198 ms |
| 4 | 613.055 ms |
| 5 | 553.989 ms |

Keep the configurable 15-second watchdog interval initially. This small-tree
scan already costs about 0.6 seconds, so scans need bounded concurrency and
monitoring on denser hosts. Volume quota remains a soft polling limit; writes
can exceed it between checks. Tmpfs supplies the hard ENOSPC boundary but uses
host memory. No performance target or security default was changed.
