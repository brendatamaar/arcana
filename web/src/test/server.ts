import { setupServer } from "msw/node";

/** Mock Arcana API. Tests add handlers with server.use(...); unhandled requests fail the test. */
export const server = setupServer();
