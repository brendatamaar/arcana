# MCP server

`arcana mcp` runs a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdin/stdout so MCP-capable agents can use Arcana with no glue code.
It is a client of a running Arcana API (`arcana serve`); it never touches Docker
itself, so every limit, quota and ownership check is Arcana's own.

```sh
arcana mcp --server http://127.0.0.1:7070 --api-key "$ARCANA_API_KEY"
```

`--server` and `--api-key` default to `ARCANA_URL` and `ARCANA_API_KEY`. Create
a dedicated non-admin key for the agent with `arcana keys create`: it then sees
only the sandboxes and snapshots it creates. Logs go to stderr as JSON and never
include the key; stdout carries only the protocol.

## Client configuration

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "arcana": {
      "command": "arcana",
      "args": ["mcp"],
      "env": {
        "ARCANA_URL": "http://127.0.0.1:7070",
        "ARCANA_API_KEY": "arc_live_..."
      }
    }
  }
}
```

Claude Code:

```sh
claude mcp add arcana --env ARCANA_URL=http://127.0.0.1:7070 --env ARCANA_API_KEY=arc_live_... -- arcana mcp
```

Any other stdio MCP client takes the same command, arguments and environment.

## Tools

| Tool | Purpose |
|---|---|
| `create_sandbox` | Create a sandbox from a template. Optional `ttl_seconds`, `network_mode`/`allow`/`presets`, `from_snapshot`, `keep`. |
| `run_command` | Run `cmd` (argv) or `shell` (`/bin/sh -c`), with `cwd`, `env`, `timeout_seconds`. Returns exit code, stdout, stderr, `timed_out`, `truncated`. |
| `read_file` | Read a file (default 256 KiB, at most 1 MiB; non-UTF-8 comes back base64). |
| `write_file` | Write a file of at most 1 MiB, as `utf8` or `base64`. |
| `list_files` | List a directory to depth 1..100. |
| `snapshot_sandbox` | Save `/workspace` as a snapshot (workspace only; no processes, no files outside `/workspace`, no environment values). |
| `fork_sandbox` | Create `count` sandboxes from a snapshot in parallel. Returns the sandboxes created and a failure per index that could not be. |
| `destroy_sandbox` | Destroy a sandbox. |

The tool descriptions the model sees state these limits, so it can plan around
them. Output returned to the model is capped at 64 KiB per stream on top of the
server's own cap (`limits.exec.max_output_bytes`).

## Session lifecycle

Every sandbox created through a session carries the label
`mcp_session=<id>`. When the session ends (the client disconnects or the process
receives SIGINT/SIGTERM), sandboxes and forks it created are destroyed, unless
the tool call set `keep: true` or the sandbox was already destroyed. Snapshots
are not tied to the session and persist until deleted. If cleanup fails, the
command exits non-zero and the sandboxes still expire by TTL or idle timeout. A
single session tracks at most 1000 sandboxes for cleanup; the rest rely on TTL.

Per-key quotas and the creation rate limit apply as for any API client.
