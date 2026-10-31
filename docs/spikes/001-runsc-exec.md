# Docker lifecycle and exec streaming

Task: T0.2. Measured 2026-10-31 on the owner's experimental VPS: Ubuntu
24.04, kernel 6.8.0-138-generic, x86_64, 2 vCPU, 1967 MiB RAM, 40 GB SSD,
Docker 29.8.1, cgroups v2, runsc release-20261026.0. Other applications remained
running. This is smaller than the PRD's 4 vCPU / 8 GB reference host.

## Method and results

`go -C spikes test -race -v ./001-runsc-exec` passed. The program uses the
official successor client `github.com/moby/moby/client v0.6.2` with API version
negotiation, and `github.com/moby/moby/api v1.56.1` for types and demultiplexing.
Image: `debian:stable-slim`, pulled before measurement. Each sample creates and
starts a fresh container; image download, exec and removal are excluded. Samples
run sequentially, 20 per runtime; percentiles use nearest rank.

| Runtime | Samples | Create + start p50 | p95 |
|---|---:|---:|---:|
| runc (explicit comparison only) | 20 | 168.030 ms | 220.749 ms |
| runsc | 20 | 167.481 ms | 209.808 ms |

Raw milliseconds, in execution order:

```text
runc: 305.806 170.197 197.740 166.113 174.641 196.352 161.780 167.003 172.357 157.169 181.770 220.749 167.985 147.656 157.225 168.082 183.725 151.713 141.844 168.030
runsc: 440.492 147.752 176.404 160.439 169.666 172.805 162.342 207.811 174.810 178.716 161.993 171.700 146.190 175.499 152.143 158.215 209.808 158.978 167.481 141.023
```

Both runtimes passed stdin round-trip, exact stdout/stderr demultiplexing,
zero exit code, and TTY resize verified by `stty size` returning `35 101`.
Container inspect verified UID/GID 1000, read-only rootfs, all capabilities
dropped, no-new-privileges, network none, tmpfs at `/tmp` and `/run/sbx`,
256 MiB memory with memory-swap equal to memory, 0.5 CPU and 64 PIDs. No host
paths or workspace mounts were used. Cleanup force-removes only the container
ID returned by create, including after a failed start or exec.

## API observations

- `ExecAttach` starts an attached exec; do not call `ExecStart` again.
- Non-TTY streams have Docker framing: use `stdcopy.StdCopy`; TTY has one raw
  stream and terminal echo/CRLF handling.
- Non-TTY stdin supports `CloseWrite`; keep the TTY connection open until exit.
- Resize is a separate API request after attach, before the test sends input.
- Runtime selection is explicit; no fallback from runsc to runc is allowed.
- Docker reload can return before a newly added runtime becomes visible. After
  installing runsc, poll daemon runtime registration before the first container.

## Implications

The PRD cold-create p50 target of 2 seconds looks plausible for cached images.
These are Docker create/start measurements, not full Arcana API latency. The
results do not validate pool acquire, exec overhead, density or Arcana RSS;
those require their later implementations and benchmarks. PRD targets remain
unchanged. No security requirement was relaxed.
