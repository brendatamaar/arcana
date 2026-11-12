import { type SyntheticEvent, useId, useState } from "react";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import { toApiError } from "../api/errors";

export function Login() {
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const id = useId();
  const navigate = useNavigate();
  const client = useQueryClient();

  const submit = async (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      // The key is sent once, as a header, to exchange it for an HttpOnly session cookie. It is not
      // kept anywhere: not in storage, not in a cookie JavaScript can read, and not in state after this.
      const result = await api.POST("/v1/sessions", {
        headers: { Authorization: `Bearer ${key.trim()}` },
      });
      setKey("");
      if (result.error !== undefined) {
        const failure = toApiError(result.response.status, result.error);
        setError(
          result.response.status === 401
            ? "That API key was not accepted."
            : `${failure.code}: ${failure.message}`,
        );
        return;
      }
      client.clear();
      void navigate("/");
    } catch {
      setError("Could not reach the server.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="mx-auto mt-16 max-w-md px-4">
      <h1 className="mb-1 text-2xl font-semibold">Arcana</h1>
      <p className="mb-6 text-sm text-slate-600 dark:text-slate-400">
        Paste an API key to open the dashboard. It is exchanged for a session and not stored.
      </p>
      <form onSubmit={(e) => void submit(e)} className="space-y-4">
        <div>
          <label htmlFor={id} className="mb-1 block text-sm font-medium">
            API key
          </label>
          <input
            id={id}
            type="password"
            autoComplete="off"
            spellCheck={false}
            required
            value={key}
            onChange={(e) => setKey(e.target.value)}
            className="w-full rounded border border-slate-400 bg-white px-3 py-2 font-mono text-sm dark:bg-slate-900"
          />
        </div>
        {error ? (
          <p role="alert" className="text-sm text-red-700 dark:text-red-300">
            {error}
          </p>
        ) : null}
        <button
          type="submit"
          disabled={busy || key.trim() === ""}
          className="rounded bg-blue-700 px-4 py-2 font-medium text-white hover:bg-blue-800 disabled:opacity-60"
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </main>
  );
}
