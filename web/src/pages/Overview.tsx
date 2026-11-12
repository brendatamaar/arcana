import { Link } from "react-router";
import { useOverview } from "../api/hooks";
import { ErrorNotice, Loading } from "../components/Notice";
import { StateBadge } from "../components/StateBadge";
import { describeReason, formatTime } from "../lib/format";

export function Overview() {
  const { data, error, isPending } = useOverview();
  if (isPending) return <Loading what="overview" />;
  if (error) return <ErrorNotice error={error} />;
  const active = data.sandboxes_by_template.reduce((sum, row) => sum + row.count, 0);
  return (
    <div className="space-y-8">
      <h1 className="text-2xl font-semibold">Overview</h1>
      <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3">
        {[
          ["Version", data.version || "unknown"],
          ["Runtime", data.runtime || "unknown"],
          ["Active sandboxes", String(active)],
        ].map(([term, value]) => (
          <div
            key={term}
            className="rounded border border-slate-300 bg-white p-4 dark:border-slate-800 dark:bg-slate-900"
          >
            <dt className="text-xs uppercase tracking-wide text-slate-600 dark:text-slate-400">
              {term}
            </dt>
            <dd className="mt-1 break-words text-lg font-medium">{value}</dd>
          </div>
        ))}
      </dl>

      <section aria-labelledby="by-template">
        <h2 id="by-template" className="mb-2 text-lg font-semibold">
          Active sandboxes by template
        </h2>
        {data.sandboxes_by_template.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-slate-400">No active sandboxes.</p>
        ) : (
          <ul className="divide-y divide-slate-200 rounded border border-slate-300 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900">
            {data.sandboxes_by_template.map((row) => (
              <li key={row.template} className="flex justify-between px-4 py-2">
                <span>{row.template}</span>
                <span className="font-medium">{row.count}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      {data.pool.length > 0 ? (
        <section aria-labelledby="pool">
          <h2 id="pool" className="mb-2 text-lg font-semibold">
            Warm pool
          </h2>
          <ul className="divide-y divide-slate-200 rounded border border-slate-300 bg-white dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900">
            {data.pool.map((row) => (
              <li key={row.template} className="flex justify-between px-4 py-2">
                <span>{row.template}</span>
                <span className="font-medium">
                  {row.available} of {row.target} ready
                </span>
              </li>
            ))}
          </ul>
        </section>
      ) : null}

      <section aria-labelledby="recent">
        <h2 id="recent" className="mb-2 text-lg font-semibold">
          Recent terminations
        </h2>
        {data.recent_terminations.length === 0 ? (
          <p className="text-sm text-slate-600 dark:text-slate-400">Nothing has ended yet.</p>
        ) : (
          <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
                <tr>
                  <th scope="col" className="px-4 py-2">
                    Sandbox
                  </th>
                  <th scope="col" className="px-4 py-2">
                    Template
                  </th>
                  <th scope="col" className="px-4 py-2">
                    State
                  </th>
                  <th scope="col" className="px-4 py-2">
                    Why
                  </th>
                  <th scope="col" className="px-4 py-2">
                    Ended
                  </th>
                </tr>
              </thead>
              <tbody>
                {data.recent_terminations.map((row) => (
                  <tr key={row.id} className="border-t border-slate-200 dark:border-slate-800">
                    <td className="px-4 py-2 font-mono">
                      <Link
                        to={`/sandboxes/${encodeURIComponent(row.id)}`}
                        className="text-blue-700 underline dark:text-blue-300"
                      >
                        {row.id}
                      </Link>
                    </td>
                    <td className="px-4 py-2">{row.template}</td>
                    <td className="px-4 py-2">
                      <StateBadge state={row.state} />
                    </td>
                    <td className="px-4 py-2">{describeReason(row.reason)}</td>
                    <td className="px-4 py-2">{formatTime(row.ended_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
