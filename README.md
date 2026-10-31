# Arcana

Self-hosted, open-source sandboxes for AI agents, written in Go.

Arcana is in development. The repository currently provides a CLI scaffold;
sandbox orchestration will follow the tasks in [PLAN.md](docs/PLAN.md).

Build with Go 1.27.3 and GNU Make:

```sh
CGO_ENABLED=0 make build
./bin/arcana version
make check
```

`make check` needs golangci-lint v2.14.0 and a C compiler for Go's race detector.
Use Linux (or WSL2) with Docker and cgroups v2 for runtime development.
Version metadata can be set with `make build VERSION=v0.0.0 COMMIT=abc123 BUILD_DATE=2026-10-31T00:00:00Z`.

Read [AGENTS.md](docs/AGENTS.md), [PRD.md](docs/PRD.md), and
[PLAN.md](docs/PLAN.md) before contributing. Licensed under Apache-2.0.
