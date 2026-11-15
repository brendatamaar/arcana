import { type SyntheticEvent, useId, useMemo, useState } from "react";
import { Link } from "react-router";
import { useDeleteSnapshot, useForkSnapshot, useSnapshots } from "../api/hooks";
import type { Snapshot } from "../api/client";
import { ConfirmAction } from "../components/ConfirmAction";
import { ErrorNotice, Loading } from "../components/Notice";
import { formatTime } from "../lib/format";

function megabytes(bytes: number): string {
  return `${(bytes / 1048576).toFixed(1)} MB`;
}

function Fork({ snapshot }: { snapshot: Snapshot }) {
  const [count, setCount] = useState("2");
  const id = useId();
  const fork = useForkSnapshot(snapshot.id);
  const n = Number(count);
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    fork.mutate(n);
  };
  return (
    <div className="space-y-1">
      <form
        onSubmit={submit}
        aria-label={`Fork ${snapshot.id}`}
        className="flex flex-wrap items-center gap-2"
      >
        <label htmlFor={id} className="text-sm">
          Copies
        </label>
        <input
          id={id}
          inputMode="numeric"
          value={count}
          onChange={(e) => setCount(e.target.value)}
          className="w-16 rounded border border-slate-400 bg-white px-2 py-1 text-sm dark:bg-slate-900"
        />
        <button
          type="submit"
          disabled={fork.isPending || !Number.isInteger(n) || n < 1}
          className="rounded border border-slate-400 px-3 py-1 text-sm hover:bg-slate-100 disabled:opacity-60 dark:hover:bg-slate-800"
        >
          {fork.isPending ? "Forking…" : "Fork"}
        </button>
      </form>
      {fork.error ? <ErrorNotice error={fork.error} /> : null}
      {fork.data ? (
        <div role="status" className="text-sm">
          <p>Created {fork.data.items.length}.</p>
          <ul className="font-mono text-xs">
            {fork.data.items.map((sb) => (
              <li key={sb.id}>
                <Link
                  to={`/sandboxes/${encodeURIComponent(sb.id)}`}
                  className="text-blue-700 underline dark:text-blue-300"
                >
                  {sb.id}
                </Link>
              </li>
            ))}
          </ul>
          {fork.data.failures.length > 0 ? (
            <ul className="text-red-800 dark:text-red-300">
              {fork.data.failures.map((failure) => (
                <li key={failure.index}>
                  Copy {failure.index + 1} failed: {failure.code}: {failure.message}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function Delete({ id }: { id: string }) {
  const remove = useDeleteSnapshot();
  return (
    <div className="space-y-1">
      <ConfirmAction
        label="Delete"
        confirmLabel="Delete snapshot"
        question="Delete this snapshot? Sandboxes created from it are not affected."
        busy={remove.isPending}
        onConfirm={() => remove.mutate(id)}
      />
      {remove.error ? <ErrorNotice error={remove.error} /> : null}
    </div>
  );
}

export function Snapshots() {
  const { data, error, isPending, hasNextPage, fetchNextPage, isFetchingNextPage } = useSnapshots();
  const rows = useMemo(() => (data?.pages ?? []).flatMap((page) => page.items), [data]);
  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">Snapshots</h1>
      <p className="text-sm text-slate-600 dark:text-slate-400">
        A snapshot is a copy of a sandbox&apos;s /workspace. Create one from a sandbox&apos;s page,
        then fork it into several sandboxes.
      </p>
      {isPending ? <Loading what="snapshots" /> : null}
      {error ? <ErrorNotice error={error} /> : null}
      {data && rows.length === 0 ? (
        <p className="text-sm text-slate-600 dark:text-slate-400">No snapshots.</p>
      ) : null}
      {rows.length > 0 ? (
        <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Snapshots</caption>
            <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
              <tr>
                {["ID", "Source", "Template", "Size", "Created", "Fork", "Delete"].map((name) => (
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
                  <td className="px-4 py-2 font-mono text-xs">{row.id}</td>
                  <td className="px-4 py-2 font-mono text-xs">{row.source_sandbox_id}</td>
                  <td className="px-4 py-2">{row.template}</td>
                  <td className="px-4 py-2">{megabytes(row.size_bytes)}</td>
                  <td className="px-4 py-2">{formatTime(row.created_at)}</td>
                  <td className="px-4 py-2">
                    <Fork snapshot={row} />
                  </td>
                  <td className="px-4 py-2">
                    <Delete id={row.id} />
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
