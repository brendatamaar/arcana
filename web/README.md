# Arcana dashboard

The read-only web UI served at `/ui`. See [docs/dashboard.md](../docs/dashboard.md) for what it does and how
it is built. Requires Node 22 or newer and pnpm.

```sh
pnpm install --frozen-lockfile
pnpm run dev          # Vite on :5173; /v1 proxied to $ARCANA_URL (default http://127.0.0.1:7070)
pnpm run generate     # src/api/schema.d.ts from ../api/openapi.yaml (never edit it by hand)
pnpm run lint && pnpm run typecheck && pnpm test
pnpm run build        # into ../internal/webui/dist
pnpm run size         # enforce the 200 KB gzip initial-JavaScript budget
pnpm run e2e          # Playwright; needs a running Arcana with the dashboard (see `make web-e2e`)
```

Conventions (see AGENTS.md): all API calls go through the generated `openapi-fetch` client in `src/api/`;
server state lives in TanStack Query and live events update that cache; sandbox-controlled text is rendered
only as React text (never markup); nothing is stored in web storage; styling is Tailwind only, with no inline
`<style>` or `<script>`.
