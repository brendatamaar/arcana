import createClient, { type Middleware } from "openapi-fetch";
import { ApiError, toApiError } from "./errors";
import type { components, paths } from "./schema";

export type Sandbox = components["schemas"]["Sandbox"];
export type SandboxState = components["schemas"]["SandboxState"];
export type ExecRecord = components["schemas"]["ExecRecord"];
export type Overview = components["schemas"]["Overview"];
export type StateChange = components["schemas"]["StateChange"];
export type Session = components["schemas"]["Session"];
export type Snapshot = components["schemas"]["Snapshot"];
export type Template = components["schemas"]["Template"];
export type ApiKey = components["schemas"]["Key"];
export type AuditEvent = components["schemas"]["AuditEvent"];

/**
 * Supplies the session's CSRF token for writes. The app wires this to the session query so the
 * token lives only in memory (never in storage) and is refetched after a reload.
 */
let csrfProvider: () => Promise<string | undefined> = () => Promise.resolve(undefined);
export function setCsrfProvider(provider: () => Promise<string | undefined>): void {
  csrfProvider = provider;
}

/** Header names the server expects on cookie-authenticated writes (docs/decisions/0008). */
export const CSRF_HEADER = "X-CSRF-Token";
export const REAUTH_HEADER = "X-Arcana-Reauth";

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
api.use({
  async onRequest({ request }) {
    if (request.method === "GET" || request.method === "HEAD") return undefined;
    // Login authenticates with the API key and has no session yet.
    if (new URL(request.url).pathname.endsWith("/v1/sessions")) return undefined;
    const token = await csrfProvider();
    if (token) request.headers.set(CSRF_HEADER, token);
    return request;
  },
});

/** Returns the data of an openapi-fetch result or throws the server's error. */
export function unwrap<T>(result: { data?: T; error?: unknown; response: Response }): T {
  if (result.error !== undefined || result.data === undefined)
    throw toApiError(result.response.status, result.error);
  return result.data;
}

export function isUnauthorized(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}
