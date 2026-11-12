# Quickstart

This page is the short path. The repository README holds the complete, verbatim quickstart for a fresh Ubuntu
24.04 host (it is executed in CI on every change, so it does not drift), including gVisor setup.

![The CLI quickstart: create a sandbox, run Python, move a file, clean up](tapes/quickstart.gif)

## 1. Install and check the host

```sh
go install github.com/brendatamaar/arcana/cmd/arcana@latest   # or the release installer, see Install
arcana doctor                                                   # prints a fix hint for every failure
```

Release binaries and the installer script include the [web dashboard](dashboard.md); a `go install` build does
not (it serves an explanatory page at `/ui`). See [Install](install.md) for all channels.

## 2. Run the server

```sh
arcana serve --config config.yaml        # first start prints a one-time admin key
export ARCANA_URL=http://127.0.0.1:7070 ARCANA_API_KEY=arc_live_...
```

Build the template images once with `make templates` (see [configuration](configuration.md)).

## 3. Use it

```sh
id=$(arcana --json sandbox create --template python | jq -r .id)
arcana exec "$id" -- python -c 'print(1 + 1)'              # streams output, exits with the command's status
arcana files put "$id" ./input.txt /workspace/input.txt

snap=$(arcana --json snapshot create "$id" | jq -r .id)      # save /workspace
arcana fork "$snap" --count 3                                # three independent copies to try fixes in parallel
arcana sandbox rm "$id"
```

![Snapshot a workspace and fork three independent copies](tapes/fork.gif)

Open `/ui/` in a browser to watch sandboxes and live command output, or point an MCP client at `arcana mcp`
(see [MCP server](mcp.md)). Programmatic use: [SDKs](sdks.md) and the Go client.

## Where next

- [Security model](security.md): what is and is not protected, and how to harden the host.
- [Operations](operations.md): upgrades, backups, metrics and capacity.
- [Architecture](architecture.md): how the pieces fit together.
