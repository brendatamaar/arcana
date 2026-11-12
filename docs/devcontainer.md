# Development container

`.devcontainer/` gives contributors a ready environment in GitHub Codespaces or any Dev Containers
client: Go, Node LTS with pnpm (for the dashboard in `web/`), Python 3.12 (for the Python SDK), and a
nested Docker daemon (Docker-in-Docker) so Arcana can start sandboxes.

## What happens on creation

1. Toolchains are checked, Go modules and the dashboard dependencies are installed, and `golangci-lint` is installed.
2. `.devcontainer/setup-runtime.sh` tries to use **gVisor (runsc)**: it installs `runsc` from the official apt
   repository, registers it with the nested Docker daemon, and proves it works by starting a container with
   `--runtime=runsc`.
3. It writes `~/.config/arcana/dev.yaml`, and the `arcana-base:local` template image is built.

Then:

```sh
make build-all
./bin/arcana serve --config ~/.config/arcana/dev.yaml
```

Ports 7070 (API and the dashboard at `/ui`) and 9090 (metrics) are forwarded.

## Falling back to runc

Nested sandboxes are restricted in some environments, so runsc may not start inside the container. When
it cannot, the script does **not** pretend: it writes a config with `oci_runtime: runc` and
`allow_insecure_runtime: true`, and prints a banner on creation, on every start and in every new
terminal:

```text
INSECURE DEVELOPMENT RUNTIME: gVisor (runsc) is not usable here.
Sandboxes use runc and SHARE THIS MACHINE'S KERNEL. Do not run untrusted code.
```

The opt-in lives only in the generated `dev.yaml`; the repository's own defaults are unchanged. Under
runc you can develop the API, SDKs and dashboard, but `make test-escape` cannot prove isolation, so run
the escape suite on a host with runsc (CI does).

Environment variables for the script: `ARCANA_DEV_FORCE_RUNC=1` forces the fallback,
`ARCANA_DEV_SKIP_INSTALL=1` only detects an existing runsc, `ARCANA_DEV_HOME` and
`ARCANA_DEV_DATA_DIR` move the output and data directories.

## Verification status

What was checked on a normal Linux host (with runsc installed): the scripts parse (`bash -n`), the
JSON is valid, the forced-runc and detect-only paths of `setup-runtime.sh` produce the expected
`RUNTIME` and `dev.yaml`, and `arcana serve` accepts the generated config and logs the insecure-runtime
warning for the runc variant.

**Not checked:** an actual Codespaces creation. Whether runsc installs and starts there is unknown
until someone tries it; record the outcome (the `RUNTIME` file says `runsc` or `runc`) in the pull
request or here.
