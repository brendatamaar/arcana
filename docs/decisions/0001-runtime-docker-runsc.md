# 0001: Docker Engine with runsc as the default

Status: accepted. Date: 2026-11-01. Tasks: T0.2, T0.6.

Use Docker Engine and gVisor runsc for sandbox isolation. runc remains an
explicitly opted-in comparison/development runtime, never automatic fallback.
Use the official successor Go client `github.com/moby/moby/client`; T0.2
validated v0.6.2 with API types v1.56.1 and version negotiation against Docker
29.8.1. Pin production dependencies when T1.7 introduces them.

[Lifecycle and exec evidence](../spikes/001-runsc-exec.md) covers inspect-verified
hardening, demultiplexing, stdin, TTY resize and 20 cached-image create/start
samples per runtime. Measured p50 was about 168 ms on both runtimes. These
measurements are not Arcana API latency or proof of production capacity.

Defaults remain runsc, network none, UID 1000, dropped capabilities,
no-new-privileges, read-only rootfs and bounded resource limits. Docker access is
root-equivalent; use a dedicated experimental host. Do not expose its socket.

The 2 vCPU / 2 GB VPS is adequate for sequential small probes, not the PRD's
4 vCPU / 8 GB reference benchmark or its 20-sandbox density target. Retain
targets until the later benchmark measures the implemented orchestrator.
