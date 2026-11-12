# 0005: SDKs generated from the OpenAPI contract, with hand-written wrappers

Status: accepted. Date: 2026-11-12. Tasks: T3.3. Dependencies approved by the
owner on 2026-11-12.

`api/openapi.yaml` is the source of truth. Each SDK has a generated layer that
is committed and never edited by hand, plus a small hand-written layer for what
generators handle badly: raw binary file bodies, `application/gzip` archives,
the WebSocket exec stream, and a uniform error type.

## TypeScript (`sdk/typescript`)

`openapi-typescript` generates `src/schema.d.ts`; `openapi-fetch` is the typed
JSON transport. Both were already approved for the dashboard. `ws` is added for
the exec stream because Node 18 (the SDK's minimum) has no WebSocket client.
Tests use the built-in `node --test` runner so the SDK does not require a newer
Node than its users have (the Vitest line used by the dashboard needs Node 22).

## Python (`sdk/python`)

`openapi-python-client` (pinned in the `dev` extra) generates `arcana._generated`:
attrs models and operations. The wrapper builds dict payloads, calls the
generated client's HTTP session, and parses responses with the generated models;
it does not depend on generated operation names. The generated client imports
`httpx2` (BSD-3, the maintained continuation of httpx published by the Pydantic
organisation), which is therefore a runtime dependency, with `attrs`. `websockets`
(BSD-3) is added for the exec stream; its sync client is used and entered as a
context manager by hand, because 17.1 deprecates the bare call and earlier
versions still support the context manager. Tests use `unittest` to avoid a
test-framework dependency.

## Consequences

- The generator cannot express the archive endpoints (`application/gzip`), so the
  generated Python layer omits them and the wrapper implements them directly.
- `make sdk-generate` regenerates both layers and `make sdk-verify-generated`
  fails CI when they are stale, as `make verify-generated` does for Go.
- Both SDKs follow the Go client's rules: redirects are never followed, mutating
  calls are never retried automatically, the API key never appears in errors, and
  stdin is sent in frames of at most 16 KiB.
- Live tests (`make sdk-test`) start a throwaway Arcana through
  `scripts/with-arcana.sh`, using runsc when registered and runc with the
  explicit insecure opt-in otherwise (CI).
- Licences: `openapi-fetch`, `openapi-typescript`, `ws`, `attrs`, `httpx2`,
  `websockets` and `openapi-python-client` are MIT or BSD-3, compatible with
  Apache-2.0 distribution.
