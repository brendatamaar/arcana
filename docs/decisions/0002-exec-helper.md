# 0002: In-sandbox helper for process-group termination

Status: accepted; production helper verified by T1.8.
Date: 2026-11-01. Tasks: T0.3, T0.6, T1.8.

Docker cannot signal an exec process directly. A stdlib-only static `sbx-exec`
helper runs commands in a new process group, handles timeout and explicit kill,
and stores bounded status metadata under `/run/sbx`. Mount it read-only rather
than accessing the sandbox workspace on the host.

[Probe results](../spikes/002-process-termination.md) show timeout and TERM/KILL
escalation stopping a SIGTERM-ignoring shell and child under runc and runsc.
Both sandboxes and the Docker daemon remained responsive after each attack.
The helper bind mount worked; the conditional copy-through-exec fallback was
not needed.

For TTY commands use Setpgid, Foreground and Ctty. Validate foreground ownership
with TIOCGPGRP: runsc's `/proc/self/stat` reported zero for terminal foreground
group even though the actual terminal ioctl showed the correct group. Do not
remove the foreground assertion or accept a background command instead.

These tests cover ordinary descendant process groups. A process can deliberately
create another session, so the prototype does not prove the full hostile-code
process-tree guarantee. T1.8/T1.10/T1.17 must address escaped descendants and
retain the host hard deadline that destroys a stuck sandbox. FR-EX-4 remains
mandatory; this ADR does not narrow it to process groups. Production also needs
race-safe PID identity, atomic status and clock/lifecycle handling.

Production T1.8 mechanism: each controller becomes non-dumpable and a child
subreaper before starting the command. It adopts detached descendants, signals
start-time-verified pidfds, and requires kernel ECHILD after reaping before
writing final status. Metadata is bounded and atomically replaced. Cancellation
is a controller-identity request, not a mutable-file instruction to signal a PID.

The helper uses transport exit 0 for completed success and 124 for completed
nonzero results; 125 and other exits are helper failures. Command exit codes
remain in metadata and pass unchanged through the public API and client CLI.
This distinction prevents ordinary helper signals/runtime failures from being
accepted as a completed command after metadata tampering. T1.10 must verify the
ID, cleanup marker and transport mapping and destroy the sandbox on uncertainty,
retaining the independent hard deadline. Execs inside a sandbox share its trust
domain; separate sandboxes provide isolation.

Static helpers for both release architectures are bundled in the module so
`go install` can serve immediately. Their source-derived checksums are verified
at install; atomic replacement preserves existing bind-mounted inodes.
