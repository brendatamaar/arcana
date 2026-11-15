import { useEffect } from "react";
import { BrowserRouter, Navigate, Route, Routes, useLocation, useNavigate } from "react-router";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { isUnauthorized, setCsrfProvider, setUnauthorizedHandler } from "./api/client";
import { sessionQuery } from "./api/hooks";
import type { EventSourceFactory } from "./api/live";
import { Layout } from "./components/Layout";
import { Audit } from "./pages/Audit";
import { Keys } from "./pages/Keys";
import { Login } from "./pages/Login";
import { Overview } from "./pages/Overview";
import { SandboxDetail } from "./pages/SandboxDetail";
import { Sandboxes } from "./pages/Sandboxes";
import { Snapshots } from "./pages/Snapshots";

export function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        // A refused or missing resource will not succeed on retry; only transient failures are retried.
        retry: (count, error) =>
          count < 2 &&
          !(
            error instanceof Error &&
            "status" in error &&
            typeof error.status === "number" &&
            error.status < 500
          ),
        refetchOnWindowFocus: true,
      },
    },
  });
}

/**
 * Sends the user to the login page whenever the server refuses a request for lack of a session, and
 * tells the API client where to get the CSRF token for writes: the in-memory session query.
 */
function SessionGuard() {
  const navigate = useNavigate();
  const client = useQueryClient();
  const location = useLocation();
  useEffect(() => {
    setCsrfProvider(async () => (await client.query(sessionQuery)).csrf_token);
    return () => setCsrfProvider(() => Promise.resolve(undefined));
  }, [client]);
  useEffect(() => {
    setUnauthorizedHandler(() => {
      client.clear();
      if (location.pathname !== "/login") void navigate("/login");
    });
    return () => setUnauthorizedHandler(() => undefined);
  }, [client, navigate, location.pathname]);
  return null;
}

export function AppRoutes({ eventSourceFactory }: { eventSourceFactory?: EventSourceFactory }) {
  return (
    <>
      <SessionGuard />
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route element={<Layout {...(eventSourceFactory ? { eventSourceFactory } : {})} />}>
          <Route index element={<Overview />} />
          <Route path="sandboxes" element={<Sandboxes />} />
          <Route path="snapshots" element={<Snapshots />} />
          <Route path="keys" element={<Keys />} />
          <Route path="audit" element={<Audit />} />
          <Route
            path="sandboxes/:id"
            element={<SandboxDetail {...(eventSourceFactory ? { eventSourceFactory } : {})} />}
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Routes>
    </>
  );
}

export function App() {
  return (
    <QueryClientProvider client={newQueryClient()}>
      <BrowserRouter basename="/ui">
        <AppRoutes />
      </BrowserRouter>
    </QueryClientProvider>
  );
}

export { isUnauthorized };
