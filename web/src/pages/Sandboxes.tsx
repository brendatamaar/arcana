import { useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { useSandboxes, type SandboxFilters } from "../api/hooks";
import type { SandboxState } from "../api/client";
import { ErrorNotice, Loading } from "../components/Notice";
import { StateBadge } from "../components/StateBadge";
import { formatTime } from "../lib/format";

const STATES: SandboxState[] = [
  "creating",
  "running",
  "destroying",
  "destroyed",
  "failed",
  "expired",
];

export function Sandboxes() {
  const [filters, setFilters] = useState<SandboxFilters>({ state: "", label: "" });
  const [template, setTemplate] = useState("");
  const ids = { state: useId(), template: useId(), label: useId() };
  const { data, error, isPending, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useSandboxes(filters);

  const rows = useMemo(() => (data?.pages ?? []).flatMap((page) => page.items), [data]);
  const templates = useMemo(() => [...new Set(rows.map((row) => row.template))].sort(), [rows]);
  const visible = template ? rows.filter((row) => row.template === template) : rows;

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">Sandboxes</h1>
      <form
        className="flex flex-wrap items-end gap-3"
        onSubmit={(e) => e.preventDefault()}
        aria-label="Filter sandboxes"
      >
        <div>
          <label htmlFor={ids.state} className="mb-1 block text-sm font-medium">
            State
          </label>
          <select
            id={ids.state}
            value={filters.state}
            onChange={(e) => setFilters({ ...filters, state: e.target.value as SandboxState | "" })}
            className="rounded border border-slate-400 bg-white px-2 py-1.5 dark:bg-slate-900"
          >
            <option value="">Any</option>
            {STATES.map((state) => (
              <option key={state} value={state}>
                {state}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor={ids.template} className="mb-1 block text-sm font-medium">
            Template
          </label>
          <select
            id={ids.template}
            value={template}
            onChange={(e) => setTemplate(e.target.value)}
            className="rounded border border-slate-400 bg-white px-2 py-1.5 dark:bg-slate-900"
          >
            <option value="">Any</option>
            {templates.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor={ids.label} className="mb-1 block text-sm font-medium">
            Label (key:value)
          </label>
          <input
            id={ids.label}
            value={filters.label}
            onChange={(e) => setFilters({ ...filters, label: e.target.value })}
            placeholder="task:fix-123"
            spellCheck={false}
            className="rounded border border-slate-400 bg-white px-2 py-1.5 font-mono dark:bg-slate-900"
          />
        </div>
      </form>

      {isPending ? <Loading what="sandboxes" /> : null}
      {error ? <ErrorNotice error={error} /> : null}
      {data && visible.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-slate-400">No sandboxes match.</p>
      ) : null}
      {visible.length > 0 ? (
        <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Sandboxes</caption>
            <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
              <tr>
                <th scope="col" className="px-4 py-2">
                  ID
                </th>
                <th scope="col" className="px-4 py-2">
                  State
                </th>
                <th scope="col" className="px-4 py-2">
                  Template
                </th>
                <th scope="col" className="px-4 py-2">
                  Labels
                </th>
                <th scope="col" className="px-4 py-2">
                  Created
                </th>
                <th scope="col" className="px-4 py-2">
                  Expires
                </th>
              </tr>
            </thead>
            <tbody>
              {visible.map((row) => (
                <tr key={row.id} className="border-t border-slate-200 dark:border-slate-800">
                  <td className="px-4 py-2 font-mono">
                    <Link
                      to={`/sandboxes/${encodeURIComponent(row.id)}`}
                      className="text-blue-700 underline dark:text-blue-300"
                    >
                      {row.id}
                    </Link>
                  </td>
                  <td className="px-4 py-2">
                    <StateBadge state={row.state} />
                  </td>
                  <td className="px-4 py-2">{row.template}</td>
                  <td className="px-4 py-2 font-mono text-xs">
                    {Object.entries(row.labels)
                      .map(([key, value]) => `${key}:${value}`)
                      .join(" ") || "-"}
                  </td>
                  <td className="px-4 py-2">{formatTime(row.created_at)}</td>
                  <td className="px-4 py-2">{formatTime(row.expires_at)}</td>
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
