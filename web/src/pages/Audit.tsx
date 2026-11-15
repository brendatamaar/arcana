import { type SyntheticEvent, useId, useMemo, useState } from "react";
import { useAudit, useSession, type AuditFilters } from "../api/hooks";
import { ErrorNotice, Loading } from "../components/Notice";
import { formatTime } from "../lib/format";

const EMPTY: AuditFilters = { action: "", sandbox_id: "", key_id: "" };

/** Details come from sandboxes and clients, so they are shown as plain text and shortened. */
function summarize(details: Record<string, unknown>): string {
  const text = JSON.stringify(details);
  return text.length > 240 ? `${text.slice(0, 240)}…` : text;
}

export function Audit() {
  const session = useSession();
  const admin = session.data?.admin === true;
  const [draft, setDraft] = useState<AuditFilters>(EMPTY);
  const [applied, setApplied] = useState<AuditFilters>(EMPTY);
  const ids = { action: useId(), sandbox: useId(), key: useId() };
  const { data, error, isPending, hasNextPage, fetchNextPage, isFetchingNextPage } = useAudit(
    applied,
    admin,
  );
  const rows = useMemo(() => (data?.pages ?? []).flatMap((page) => page.items), [data]);

  if (session.isPending) return <Loading what="session" />;
  if (!admin)
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold">Audit log</h1>
        <p role="alert" className="text-sm">
          The audit log needs an admin key. Sign in with one to continue.
        </p>
      </div>
    );

  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    setApplied({
      action: draft.action.trim(),
      sandbox_id: draft.sandbox_id.trim(),
      key_id: draft.key_id.trim(),
    });
  };
  const input =
    "rounded border border-slate-400 bg-white px-2 py-1.5 font-mono text-sm dark:bg-slate-900";
  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">Audit log</h1>
      <form
        onSubmit={submit}
        aria-label="Filter audit events"
        className="flex flex-wrap items-end gap-3"
      >
        {(
          [
            ["action", "Action", ids.action, "exec.started"],
            ["sandbox_id", "Sandbox ID", ids.sandbox, "sbx_…"],
            ["key_id", "Key ID", ids.key, "key_…"],
          ] as const
        ).map(([name, label, id, placeholder]) => (
          <div key={name}>
            <label htmlFor={id} className="mb-1 block text-sm font-medium">
              {label}
            </label>
            <input
              id={id}
              value={draft[name]}
              placeholder={placeholder}
              spellCheck={false}
              onChange={(e) => setDraft({ ...draft, [name]: e.target.value })}
              className={input}
            />
          </div>
        ))}
        <button
          type="submit"
          className="rounded bg-blue-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-800"
        >
          Apply
        </button>
      </form>
      {isPending ? <Loading what="audit events" /> : null}
      {error ? <ErrorNotice error={error} /> : null}
      {data && rows.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-slate-400">No events match.</p>
      ) : null}
      {rows.length > 0 ? (
        <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Audit events, newest first</caption>
            <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
              <tr>
                {["Time", "Action", "Key", "Sandbox", "Details"].map((name) => (
                  <th key={name} scope="col" className="px-4 py-2">
                    {name}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.id}
                  className="border-t border-slate-200 align-top dark:border-slate-800"
                >
                  <td className="whitespace-nowrap px-4 py-2">{formatTime(row.ts)}</td>
                  <td className="px-4 py-2 font-mono text-xs">{row.action}</td>
                  <td className="px-4 py-2 font-mono text-xs">{row.key_id || "-"}</td>
                  <td className="px-4 py-2 font-mono text-xs">{row.sandbox_id || "-"}</td>
                  <td className="max-w-md break-all px-4 py-2 font-mono text-xs">
                    {summarize(row.details)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      {hasNextPage ? (
        <button
          type="button"
          onClick={() => void fetchNextPage()}
          disabled={isFetchingNextPage}
          className="rounded border border-slate-400 px-3 py-1.5 text-sm hover:bg-slate-100 disabled:opacity-60 dark:hover:bg-slate-800"
        >
          {isFetchingNextPage ? "Loading…" : "Load more"}
        </button>
      ) : null}
    </div>
  );
}
