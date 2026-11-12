import { NavLink, Outlet, useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { useLiveEvents, type EventSourceFactory } from "../api/live";

const link = ({ isActive }: { isActive: boolean }) =>
  `rounded px-3 py-1.5 text-sm font-medium ${isActive ? "bg-blue-700 text-white" : "text-slate-700 hover:bg-slate-200 dark:text-slate-200 dark:hover:bg-slate-800"}`;

export function Layout({ eventSourceFactory }: { eventSourceFactory?: EventSourceFactory }) {
  useLiveEvents(eventSourceFactory);
  const client = useQueryClient();
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);

  const logout = async () => {
    setBusy(true);
    try {
      // The cookie is HttpOnly: only the server can clear it. This is the dashboard's one other write.
      await api.DELETE("/v1/sessions/current");
    } finally {
      client.clear();
      setBusy(false);
      void navigate("/login");
    }
  };

  return (
    <>
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:rounded focus:bg-white focus:p-2 focus:text-slate-900"
      >
        Skip to content
      </a>
      <header className="border-b border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
        <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-2 px-4 py-3">
          <div className="flex items-center gap-4">
            <span className="text-lg font-semibold">Arcana</span>
            <nav aria-label="Main" className="flex gap-1">
              <NavLink to="/" end className={link}>
                Overview
              </NavLink>
              <NavLink to="/sandboxes" className={link}>
                Sandboxes
              </NavLink>
            </nav>
          </div>
          <button
            type="button"
            onClick={() => void logout()}
            disabled={busy}
            className="rounded border border-slate-400 px-3 py-1.5 text-sm hover:bg-slate-100 disabled:opacity-60 dark:hover:bg-slate-800"
          >
            Log out
          </button>
        </div>
      </header>
      <main id="main" className="mx-auto max-w-6xl px-4 py-6">
        <Outlet />
      </main>
    </>
  );
}
