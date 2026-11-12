# Decision records

Architecture decision records explain why Arcana is built the way it is. Each is short and states the
evidence behind the choice.

- [0001: Docker and gVisor as the default runtime](decisions/0001-runtime-docker-runsc.md)
- [0002: the `sbx-exec` helper for process-tree termination](decisions/0002-exec-helper.md)
- [0003: workspace backends and exec-based file transfer](decisions/0003-workspace-and-files.md)
- [0004: internal networks and an outbound CONNECT proxy](decisions/0004-network-egress.md)
- [0005: SDKs generated from the OpenAPI contract](decisions/0005-sdk-generation.md)

The measurements behind the early decisions are in the spike reports:
[runsc exec](spikes/001-runsc-exec.md), [process termination](spikes/002-process-termination.md),
[workspace](spikes/003-workspace.md), [networking](spikes/004-network.md) and
[CPU accounting](spikes/005-cpu-accounting.md).
