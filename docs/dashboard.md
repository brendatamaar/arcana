# Web dashboard

Arcana serves a dashboard at `/ui` on the API listener (for example
`http://127.0.0.1:7070/ui/`). Release binaries include it; a binary built with `go install` serves an
explanatory page instead, because the dashboard is built with Node and embedded at release time. Set
`ui.enabled: false` to turn it off.

![The dashboard following three parallel forks of one snapshot while their commands run](images/dashboard.gif)

## Using it

Open `/ui/`, paste an API key and sign in. The key is sent once to `POST /v1/sessions` and exchanged for an
`HttpOnly`, `SameSite=Strict` session cookie; the dashboard never stores the key or a token in the browser.
Pages:

- **Overview**: version, runtime, active sandboxes by template, warm-pool fill (admin keys) and the latest
  terminations with reasons. Admin keys see the whole installation; other keys see their own sandboxes.
- **Sandboxes**: a list filterable by state, template and label, updated live.
- **Sandbox detail**: specification, time-to-live and idle countdowns, state history, command history and a
  live log viewer that follows a running command's output and keeps colours. Live sandboxes have **Keep
  alive**, **Snapshot workspace** and **Destroy** (after a second click to confirm).
- **Sandboxes** also has **Create sandbox** from a configured template.
- **Snapshots**: list, fork into several sandboxes, delete.
- **Keys** and **Audit** (admin keys only): create and revoke API keys, and browse the audit log with filters.
  Creating or revoking a key asks for your admin key again, and a new key's secret is shown once and kept
  nowhere.

The dashboard can create, destroy and keep alive sandboxes, manage snapshots and manage keys, but it **cannot
run commands or touch files**: those need an API key. Its writes are protected against cross-site request
forgery with a per-session token plus `Origin` checks (see [events and sessions](events.md) and
[ADR 0008](decisions/0008-dashboard-writes-and-csrf.md)). Everything a sandbox prints, and every name, label
or audit detail, is shown as text.

Behind a reverse proxy that changes the `Host` header, add the public origin to `ui.allowed_origins`.

## Building it

Only building needs Node (LTS) and pnpm (`corepack enable pnpm`); running Arcana never does.

```sh
make web          # build into internal/webui/dist (checks the 200 KB gzip bundle budget)
make build-all    # web, then the arcana binary with the dashboard embedded
make web-dev      # Vite dev server with hot reload; /v1 is proxied to a local Arcana (ARCANA_URL)
make web-generate # regenerate API types from api/openapi.yaml
make web-check    # lint, typecheck, unit tests, generated-types freshness, build and size budget
make web-e2e      # Playwright smoke test against a throwaway Arcana (needs `make build-all` and a browser)
```

The source is in `web/`: React and TypeScript with Vite, TanStack Query for server state, React Router,
Tailwind CSS, and a typed API client generated from the OpenAPI contract. The page is served with a strict
Content-Security-Policy (no inline script or style, no third-party origins), and the code never uses
`innerHTML` or `dangerouslySetInnerHTML` (enforced by lint).
