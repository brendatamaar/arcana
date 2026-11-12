# Dashboard sessions, event stream and output tails

These endpoints exist for the read-only web dashboard, and any client can use them with an API key. The
contract is in [`api/openapi.yaml`](../api/openapi.yaml). Configuration is under `ui.*` and
`limits.exec.tail_*` in the [configuration reference](configuration.md).

## Sessions

`POST /v1/sessions` (authenticated with an API key in the `Authorization` header) returns `{id, expires_at}`
and sets the session token as a cookie: `arcana_session`, `HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure`
unless the request is plain HTTP to a loopback address (or `X-Forwarded-Proto: https` is present). The token
is random, shown only in that cookie, and stored only as a SHA-256 hash. A session expires after
`ui.session_ttl_seconds` and stops working the moment its key is revoked. Each key keeps at most 20 sessions
(the oldest are dropped). `DELETE /v1/sessions/current` logs out and clears the cookie.

Rules for a request authenticated **only** by the cookie:

- Only `GET`, `HEAD` and `DELETE /v1/sessions/current` are allowed. Everything else is `403 forbidden`: a
  session can never create or destroy a sandbox, run a command, write a file or manage keys. Use an API key.
- It must prove it comes from this server's own pages. Either an `Origin` header equals the request's own
  origin (its `Host`) or an entry of `ui.allowed_origins`, or, for `GET`/`HEAD` without `Origin`,
  `Sec-Fetch-Site: same-origin`. A foreign, `null` or malformed `Origin` is refused even if
  `Sec-Fetch-Site` claims same-origin. Requests with neither header (such as `curl`) are refused: the cookie
  exists for browsers. Logout requires an `Origin`.
- Two cookies with the name `arcana_session` are refused.
- An `Authorization` header always takes precedence: a bad bearer key never falls back to the cookie.

If Arcana is served under a different hostname than the browser sees (a reverse proxy that rewrites `Host`),
list the public origin in `ui.allowed_origins`. Set `ui.enabled: false` to disable sessions and the event
stream; the other endpoints on this page still work with API keys.

## Event stream: `GET /v1/events`

Server-Sent Events. Each event has an increasing `id`, an `event:` type and a JSON `data:` object
`{"type", "sandbox_id", "at", "data"}`. Types: `sandbox.created`, `sandbox.state` (every state change),
`sandbox.destroyed` (with the reason), `exec.started`, `exec.ended` and `pool.changed`. Only metadata is
sent: never exec output, arguments, environment values or owner identity. Commands run for file transfers
and by the workspace watchdog do not produce exec events.

Visibility: admin keys see everything; other keys see events of their own sandboxes only, and never pool
events. A comment line (`: keep-alive`) is sent every 15 seconds.

Resuming: reconnect with `Last-Event-ID`. The server keeps the last `ui.event_buffer` events and replays those
after the ID. If that ID is too old, or from before a restart (IDs restart at 1), or the backlog is too large,
the server sends `event: reset` and closes: refetch state (for example `GET /v1/sandboxes`) and reconnect
without an ID. A client that stops reading is dropped the same way once 64 events queue up. At most 256
event streams are open at once (`429 quota_exceeded` beyond that).

## Exec history and overview

`GET /v1/sandboxes/{id}/execs` returns exec metadata newest first, paginated with `limit` and `cursor`: argv
(`cmd`) or the shell string, working directory, timing, and the real exit status. `GET /v1/overview` returns
the version, runtime, active sandboxes by template, warm-pool fill (admin keys only), and the last 20
terminations with reasons; admin keys see installation-wide numbers, other keys their own.

## Output tail: `GET /v1/sandboxes/{id}/execs/{exec_id}/output`

Both synchronous and streaming execs feed a per-exec ring in memory, `limits.exec.tail_buffer_bytes`
(256 KiB by default), keeping the newest bytes. **Output is never written to disk**: the ring is dropped
`limits.exec.tail_retention_seconds` after the exec ends, or lost on restart. File transfers are not tailed.
The stream replays the ring, then follows live output:

| Event | Data |
|---|---|
| `output` | `{"seq", "stream": "stdout" or "stderr", "data": "<base64>"}` |
| `gap` | older output was discarded (the ring wrapped, or the reader was late) |
| `end` | `{"exit_code", "timed_out", "truncated", "cancelled", "expired"}` |

`exit_code` is `null` when the real status could not be confirmed. If the ring is no longer retained, the
stream sends a single `end` with `expired: true` and the exit status from the exec record. At most
`limits.exec.max_tail_subscribers` readers follow one exec (`429 quota_exceeded` beyond that). An unknown
exec, or one that belongs to another sandbox, is `404 sandbox_not_found` (there is no separate exec error
code). The worst-case memory held by tails is 512 execs times `tail_buffer_bytes`.

Treat everything from a sandbox as hostile: the dashboard renders these bytes as text, never as HTML.
