import createClient, { type Middleware } from "openapi-fetch";
import { ApiError, toApiError } from "./errors";
import type { components, paths } from "./schema";

export type Sandbox = components["schemas"]["Sandbox"];
export type SandboxState = components["schemas"]["SandboxState"];
export type ExecRecord = components["schemas"]["ExecRecord"];
export type Overview = components["schemas"]["Overview"];
export type StateChange = components["schemas"]["StateChange"];

/** Called when a request that needed a session was refused, so the app can return to login. */
let onUnauthorized: () => void = () => undefined;
export function setUnauthorizedHandler(handler: () => void): void {
  onUnauthorized = handler;
}

const guard: Middleware = {
  onResponse({ response, request }) {
    // The login request itself legitimately answers 401 for a wrong key.
    if (response.status === 401 && !new URL(request.url).pathname.endsWith("/v1/sessions"))
      onUnauthorized();
    return undefined;
  },
};

/**
 * The only way the dashboard talks to /v1 (AGENTS: no hand-written fetch calls). Requests are
 * same-origin and carry the HttpOnly session cookie automatically; the dashboard never sees or
 * stores a token.
 */
export const api = createClient<paths>({
  baseUrl: typeof location === "undefined" ? "http://localhost" : location.origin,
  credentials: "same-origin",
  // Looked up per request rather than captured at start-up, so instrumentation that replaces
  // global fetch later (test doubles, devtools) is honoured.
  fetch: (request) => globalThis.fetch(request),
});
api.use(guard);

/** Returns the data of an openapi-fetch result or throws the server's error. */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.error !== undefined || result.data === undefined)
    throw toApiError(result.response.status, result.error);
  return result.data;
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}
