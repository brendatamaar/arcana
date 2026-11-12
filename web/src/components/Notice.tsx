import { ApiError } from "../api/errors";

/** Shows a failed request. The server's message is rendered as text. */
export function ErrorNotice({ error }: { error: unknown }) {
  const message =
    error instanceof ApiError
      ? `${error.code}: ${error.message}`
      : "The request failed. Check the connection and try again.";
  return (
    <p
      role="alert"
      className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-900 dark:border-red-800 dark:bg-red-950 dark:text-red-200"
    >
      {message}
    </p>
  );
}

export function Loading({ what }: { what: string }) {
  return (
    <p role="status" className="text-sm text-slate-600 dark:text-slate-400">
      Loading {what}…
    </p>
  );
}
