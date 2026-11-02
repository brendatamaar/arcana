# Doctor validation

T1.16 was validated on the owner-authorized Linux VPS on 2026-11-02, using
an isolated data directory inside the Arcana test workspace.

```text
go run ./cmd/arcana doctor --config doctor-config.yaml --json
```

The command exited successfully. Linux, kernel 6.8.0-138-generic, cgroups v2,
Docker 29.8.1 (overlayfs), data-directory writes, SQLite migrations, the locally
built base image, and actual runsc container/helper cleanup all passed.
`/dev/kvm` was absent and produced an informational warning with a fix hint.
No API key or sandbox output is included in the diagnostic report.

The integration/escape restart test also checks the negative startup path:
an unavailable configured template makes `doctor --json` exit nonzero with a
failed report, and `serve` exits before opening its listener. Runtime probes
use the installation's managed labels and remove their hardened tmpfs container
on completion or failure. The same test checks successful daemon restart,
peer health, CLI exit status, and binary file transfers.

Unit tests inject failures into each independent probe, exercise runc opt-in,
verify byte bounds, and use an HTTP fixture for Docker metadata, missing runtime
registration and unavailable images. They do not require a Docker daemon.
