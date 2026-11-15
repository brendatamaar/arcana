import { type SyntheticEvent, useId, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { useCreateSandbox, useSandboxes, useTemplates, type SandboxFilters } from "../api/hooks";
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

/** Creates a sandbox from a configured template. Everything else uses the template's defaults. */
function CreateSandbox() {
  const [open, setOpen] = useState(false);
  const [template, setTemplate] = useState("");
  const [minutes, setMinutes] = useState("");
  const ids = { template: useId(), ttl: useId() };
  // Templates are only needed once the form is open.
  const templates = useTemplates(open);
  const create = useCreateSandbox();
  const navigate = useNavigate();

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="rounded bg-blue-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-800"
      >
        Create sandbox
      </button>
    );
  }
  const names = (templates.data?.items ?? []).map((t) => t.name);
  const chosen = template || names[0] || "";
  const ttl = minutes.trim() === "" ? undefined : Number(minutes) * 60;
  const invalid = chosen === "" || (ttl !== undefined && (!Number.isInteger(ttl) || ttl < 60));

  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    create.mutate(
      { template: chosen, ...(ttl === undefined ? {} : { ttl_seconds: ttl }) },
      { onSuccess: (created) => void navigate(`/sandboxes/${encodeURIComponent(created.id)}`) },
    );
  };

  return (
    <form
      onSubmit={submit}
      aria-label="Create sandbox"
      className="flex flex-wrap items-end gap-3 rounded border border-slate-300 bg-white p-3 dark:border-slate-800 dark:bg-slate-900"
    >
      <div>
        <label htmlFor={ids.template} className="mb-1 block text-sm font-medium">
          Template
        </label>
        <select
          id={ids.template}
          value={chosen}
          onChange={(e) => setTemplate(e.target.value)}
          className="rounded border border-slate-400 bg-white px-2 py-1.5 dark:bg-slate-900"
        >
          {names.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </div>
      <div>
        <label htmlFor={ids.ttl} className="mb-1 block text-sm font-medium">
          Time to live (minutes, optional)
        </label>
        <input
          id={ids.ttl}
          inputMode="numeric"
          value={minutes}
          onChange={(e) => setMinutes(e.target.value)}
          placeholder="template default"
          className="w-40 rounded border border-slate-400 bg-white px-2 py-1.5 dark:bg-slate-900"
        />
      </div>
      <button
        type="submit"
        disabled={invalid || create.isPending}
        className="rounded bg-blue-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-800 disabled:opacity-60"
      >
        {create.isPending ? "Creating…" : "Create"}
      </button>
      <button
        type="button"
        onClick={() => {
          create.reset();
          setOpen(false);
        }}
        className="rounded border border-slate-400 px-3 py-1.5 text-sm hover:bg-slate-100 dark:hover:bg-slate-800"
      >
        Cancel
      </button>
      {templates.error ? <ErrorNotice error={templates.error} /> : null}
      {create.error ? <ErrorNotice error={create.error} /> : null}
    </form>
  );
}

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
      <CreateSandbox />
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
