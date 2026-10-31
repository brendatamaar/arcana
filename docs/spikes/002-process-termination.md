# Process-group termination and foreground TTY

Task: T0.3. Same experimental VPS and versions as [T0.2](001-runsc-exec.md).

## Results

The stdlib-only Linux prototype in `cmd/sbx-exec` builds with `CGO_ENABLED=0`.
The integration test builds it on the Linux host and bind-mounts the binary
read-only at `/opt/arcana/sbx-exec`. All sandbox hardening flags stay enabled.

| Check | runc | runsc release-20261026.0 |
|---|---|---|
| Read-only helper bind mount executes as UID 1000 | pass | pass |
| Timeout kills shell and child sleep ignoring SIGTERM | pass | pass |
| Explicit kill escalates TERM to KILL after grace | pass | pass |
| Status reports exit 137 and timeout true only for timeout | pass | pass |
| Foreground terminal group equals child process group via TIOCGPGRP | pass | pass |
| Attacked container, second container and Docker remain responsive | pass | pass |

Commands: `make check test-integration test-escape`, plus
`go test -race -v -tags=integration ./test/integration`. Tests own cleanup of
their exact container IDs; no existing VPS containers are changed or removed.
The escape target currently includes this focused helper attack test, not the
complete ESC suite scheduled for T1.17.

## Chosen mechanism and quirks

- `run` starts argv with `SysProcAttr.Setpgid`. Timeout sends SIGKILL to the
  negative group ID and waits before writing status. `kill` sends SIGTERM,
  waits a bounded grace, then sends SIGKILL to the same group.
- `--tty` sets `Foreground` and `Ctty=0` as well as `Setpgid`. The ioctl-based
  probe confirms real terminal foreground ownership for both runtimes.
- Initial testing via `/proc/self/stat` failed under runsc: process group 41,
  reported terminal foreground group 0. A direct TIOCGPGRP diagnostic instead
  returned matching values (10/10). The test therefore asserts the actual
  terminal ioctl, not gVisor's `/proc` representation. This does not relax the
  job-control requirement or change the PRD's chosen mechanism.
- The diagnostic binary is streamed through exec into an executable, capped
  workspace tmpfs. Default `/tmp` was not executable in the first diagnostic;
  the read-only helper bind mount itself worked without adjustment.
- PID/status paths use validated IDs under `os.Root`, bounded metadata reads
  and mode 0600. This remains a prototype; T1.8 must address production
  lifecycle/race handling and the host hard deadline remains required.
- There was no bind-mount failure, so the conditional copy-through-exec/tar
  fallback was not needed. No host workspace reads, docker cp or commit used.

No PRD design change is necessary. Ordinary process groups cannot contain a
deliberately detached session; container destruction remains the final boundary
for runaway code. Later hard-deadline and escape work must retain that boundary.
