import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";

/** What GET /v1/sessions/current answers unless a test says otherwise: an ordinary, non-admin session. */
export function sessionBody(overrides: { admin?: boolean; csrf_token?: string } = {}) {
  return {
    id: "ses_test",
    expires_at: "2099-01-01T00:00:00Z",
    csrf_token: "csrf-test-token",
    admin: false,
    ...overrides,
  };
}

/** Mock Arcana API. Tests add handlers with server.use(...); unhandled requests fail the test. */
export const server = setupServer(
  // Every page of the dashboard loads the session (for the nav and the CSRF token).
  http.get("*/v1/sessions/current", () => HttpResponse.json(sessionBody())),
);
