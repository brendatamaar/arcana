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
