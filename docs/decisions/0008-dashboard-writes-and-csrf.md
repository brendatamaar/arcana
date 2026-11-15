# 0008: dashboard write actions and CSRF protection

Status: accepted. Date: 2026-11-15. Task: T4.4. The synchronizer-token design and the key-management
safeguards were chosen by the owner on 2026-11-15.

Until now a session cookie could only read (PRD FR-WD-5, AGENTS rule 12). T4.4 lets the
dashboard create, destroy and keep alive sandboxes, snapshot, fork and delete snapshots, manage API keys
and read the audit log. A cookie is ambient authority, so every write has to prove it came from the
dashboard's own page, not merely from the user's browser.

## Cookie reach is an allowlist of routes

`classifyRoute` (internal/auth) names exactly the routes a cookie may use, per method:

- **Read:** overview, events, templates, sandbox list and detail, exec history, state history, exec output
  tail, snapshot list and detail, keys and audit (both admin-only in their handlers), `GET /v1/sessions/current`.
- **Write:** `POST /v1/sandboxes`, `DELETE /v1/sandboxes/{id}`, `POST .../keepalive`, `POST .../snapshots`,
  `POST /v1/snapshots/{id}/fork`, `DELETE /v1/snapshots/{id}`.
- **Key management:** `POST /v1/keys`, `DELETE /v1/keys/{id}`.
- **Logout:** `DELETE /v1/sessions/current`.

Everything else is `403`, notably **exec, the WebSocket exec stream, and every file and archive route**.
Running code and moving files stay with API keys even for a page that holds every proof below.

This also fixes a gap that predates T4.4. The old rule allowed every `GET` with a same-origin cookie, and the
exec stream and file reads are `GET`s: a same-origin request with only the cookie reached those handlers
(observed on v0.3 code: the stream answered "valid WebSocket upgrade required" instead of `403`). A
cross-site page could not exploit it (`SameSite=Strict`, Origin checks), but script running in the dashboard
origin could. "Safe method" was the wrong test; the allowlist is the fix, and ESC-26 covers it.

## Proofs a write must carry

1. **A matching `Origin`.** Writes accept only `Origin` equal to the request's own origin or an entry of
   `ui.allowed_origins`; `Sec-Fetch-Site` alone no longer suffices for them (browsers always send `Origin` on
   non-GET requests). Reads keep the `Sec-Fetch-Site: same-origin` fallback.
2. **The session's CSRF token** in `X-CSRF-Token`, compared in constant time.
3. **For key management only, the admin API key typed again** in `X-Arcana-Reauth`, which must verify as the
   same admin key that owns the session. The dashboard sends it in one request and keeps it nowhere: not in
   state after the request, storage, a cookie or the query cache. A hijacked admin session therefore cannot
   mint or revoke keys on its own, and a non-admin session fails whatever it presents.

Bearer-key requests are unchanged and need none of this. An `Authorization` header still always wins over the
cookie.

## The token

The token is `base64url(HMAC-SHA256(key = session token hash, "arcana-csrf-v1"))`: unguessable without the
stored hash, different for every session, stable for the life of the session, and not stored separately.
The approved design said "stored hashed"; deriving it instead lets a reloaded page fetch the same token
again through `GET /v1/sessions/current` without a new column or a stored plaintext. `POST /v1/sessions` also
returns it, plus an `admin` flag so the UI can show the key and audit pages. The endpoint is a read, so it
needs a same-origin request: a foreign page cannot obtain the token (ESC-25). The token is useless without
the cookie, and the cookie without the token cannot write. The frontend holds the token in memory only (the
session query) and the API client adds it to every non-GET request.

Every cookie-authenticated write also records a `session.write` audit event (key, session ID, method, path).

## Not done

- A per-action step-up for other writes (only key management asks for the admin key again).
- Rotating the token within a session; it lives and dies with the session, which `ui.session_ttl_seconds`
  and revocation already bound.

## Verification

Unit tests cover the route table, the origin rules, the token and every proof for writes and key management.
Escape tests ESC-25 (writes need the token), ESC-26 (a cookie with every proof still cannot exec or touch
files) and ESC-27 (key management needs the admin key again) run through the public API on runsc, and ESC-22
and ESC-23 still pass unchanged. Two mutations (a token check that always passes, exec made a writable
route) were caught. The dashboard has component tests with hostile data for every new view, and the
Playwright smoke test drives the writes in Chromium and attempts forged requests from the page itself.
