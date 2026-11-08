# Official templates

Build all images with `make templates`. Local tags are `arcana-<name>:local`;
release tags publish multi-architecture images to
`ghcr.io/brendatamaar/arcana-<name>:<release>`.

All images provide sandbox UID/GID 1000, a volume-ready workspace, shell,
tar, du, find, coreutils, git and CA certificates. Runtime hardening and
network denial remain the orchestrator's responsibility.

Python: first run `python -m venv /workspace/.venv`, then
`python -m pip install requests`. The configured PATH selects that venv in
subsequent execs. Creating it lazily also works with empty tmpfs workspaces.
Pip's cache is in the workspace.

Node: run `npm i lodash` from /workspace; cache and global installs also
stay under /workspace. Go: create/upload a module in /workspace, then
`go mod download`; GOPATH and GOCACHE stay under /workspace.

Package downloads require allowlist networking (T2.4). Until then, integration
smokes use offline pip/venv, a local npm package, and a dependency-free Go module.

Set a template's `warm_pool` to a nonnegative target; combined targets cannot
exceed `limits.max_sandboxes`. The filler creates one member at a time and
backs off on failures. Active sandboxes take precedence over pool capacity.
Acquisition consumes a fresh instance once; it is never returned to the pool.
TTL starts when acquired. Matching environment, resources, workdir and network
settings can use the pool; differing settings or user labels use a cold start
because Docker creation settings cannot be changed safely after start.
Pool members carry `arcana.pool` labels and are reconciled after a restart.
Default shutdown preserves them for restart cleanup; `destroy_on_shutdown`
also drains the pool.
