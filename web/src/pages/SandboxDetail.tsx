import { useState } from "react";
import { Link, useParams } from "react-router";
import { useExecs, useHistory, useSandbox } from "../api/hooks";
import type { ExecRecord } from "../api/client";
import type { EventSourceFactory } from "../api/live";
import { Countdown, useNow } from "../components/Countdown";
import { LogViewer } from "../components/LogViewer";
import { ErrorNotice, Loading } from "../components/Notice";
import { StateBadge } from "../components/StateBadge";
import { deadlines, describeReason, formatTime } from "../lib/format";

function commandText(exec: ExecRecord): string {
  return exec.shell ?? (exec.cmd ?? []).join(" ");
}

function execStatus(exec: ExecRecord): string {
  if (!exec.ended_at) return "running";
  if (exec.timed_out) return "timed out";
  if (exec.cancelled) return "cancelled";
  return exec.exit_code === null || exec.exit_code === undefined
    ? "ended"
    : `exit ${exec.exit_code}`;
}

export function SandboxDetail({ eventSourceFactory }: { eventSourceFactory?: EventSourceFactory }) {
  const { id = "" } = useParams();
  const sandbox = useSandbox(id);
  const history = useHistory(id);
  const execs = useExecs(id);
  const now = useNow();
  const [selected, setSelected] = useState<string | null>(null);

  if (sandbox.isPending) return <Loading what="sandbox" />;
  if (sandbox.error) return <ErrorNotice error={sandbox.error} />;
  const sb = sandbox.data;
  const live = sb.state === "running" || sb.state === "creating";
  const left = deadlines(sb, now);
  const items = execs.data?.items ?? [];
  const activeExec = selected ?? items[0]?.id ?? null;

  return (
    <div className="space-y-8">
      <p>
        <Link to="/sandboxes" className="text-sm text-blue-700 underline dark:text-blue-300">
          ← All sandboxes
        </Link>
      </p>
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="break-all font-mono text-xl font-semibold">{sb.id}</h1>
        <StateBadge state={sb.state} />
      </header>

      <section aria-labelledby="spec">
        <h2 id="spec" className="mb-2 text-lg font-semibold">
          Specification
        </h2>
        <dl className="grid grid-cols-1 gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
          {[
            ["Template", sb.template],
            ["Runtime", sb.runtime],
            ["Source", sb.source],
            ["CPUs", String(sb.resources.cpus ?? "-")],
            ["Memory", sb.resources.memory_mb ? `${sb.resources.memory_mb} MB` : "-"],
            ["Disk", sb.resources.disk_mb ? `${sb.resources.disk_mb} MB` : "-"],
            ["Processes", String(sb.resources.pids ?? "-")],
            ["Network", sb.network.mode],
            [
              "Allowed hosts",
              sb.network.allow && sb.network.allow.length > 0
                ? sb.network.allow.join(", ")
                : "none",
            ],
            [
              "Labels",
              Object.entries(sb.labels)
                .map(([k, v]) => `${k}:${v}`)
                .join(" ") || "none",
            ],
            ["Created", formatTime(sb.created_at)],
            ["Expires", formatTime(sb.expires_at)],
          ].map(([term, value]) => (
            <div key={term} className="flex gap-2">
              <dt className="w-28 shrink-0 text-slate-600 dark:text-slate-400">{term}</dt>
              <dd className="min-w-0 break-words">{value}</dd>
            </div>
          ))}
        </dl>
        {live ? (
          <p className="mt-3 flex flex-wrap gap-x-6 text-sm font-medium">
            <Countdown seconds={left.ttl} label="Time to live" />
            <Countdown seconds={left.idle} label="Idle timeout" />
          </p>
        ) : sb.end_reason ? (
          <p className="mt-3 text-sm">Ended: {describeReason(sb.end_reason)}</p>
        ) : null}
      </section>

      <section aria-labelledby="history">
        <h2 id="history" className="mb-2 text-lg font-semibold">
          State history
        </h2>
        {history.error ? <ErrorNotice error={history.error} /> : null}
        <ol className="space-y-1 text-sm">
          {(history.data?.items ?? []).map((change, index) => (
            <li key={index} className="flex flex-wrap items-center gap-3">
              <span className="w-44 text-slate-600 dark:text-slate-400">
                {formatTime(change.at)}
              </span>
              <StateBadge state={change.state} />
              {change.reason ? <span>{describeReason(change.reason)}</span> : null}
            </li>
          ))}
        </ol>
      </section>

      <section aria-labelledby="execs">
        <h2 id="execs" className="mb-2 text-lg font-semibold">
          Commands
        </h2>
        {execs.error ? <ErrorNotice error={execs.error} /> : null}
        {items.length === 0 && !execs.isPending ? (
          <p className="text-sm text-slate-600 dark:text-slate-400">No commands have run.</p>
        ) : null}
        {items.length > 0 ? (
          <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
            <table className="w-full text-left text-sm">
              <caption className="sr-only">Command history, newest first</caption>
              <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
                <tr>
                  <th scope="col" className="px-4 py-2">
                    Command
                  </th>
                  <th scope="col" className="px-4 py-2">
                    Started
                  </th>
                  <th scope="col" className="px-4 py-2">
                    Result
                  </th>
                  <th scope="col" className="px-4 py-2">
                    <span className="sr-only">Output</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((exec) => (
                  <tr
                    key={exec.id}
                    className={`border-t border-slate-200 dark:border-slate-800 ${exec.id === activeExec ? "bg-blue-50 dark:bg-blue-950/40" : ""}`}
                  >
                    <td
                      className="max-w-md truncate px-4 py-2 font-mono text-xs"
                      title={commandText(exec)}
                    >
                      {commandText(exec)}
                    </td>
                    <td className="px-4 py-2">{formatTime(exec.started_at)}</td>
                    <td className="px-4 py-2">{execStatus(exec)}</td>
                    <td className="px-4 py-2">
                      <button
                        type="button"
                        onClick={() => setSelected(exec.id)}
                        aria-pressed={exec.id === activeExec}
                        className="text-blue-700 underline dark:text-blue-300"
                      >
                        View output
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : null}
        {activeExec ? (
          <div className="mt-4">
            <LogViewer
              key={activeExec}
              sandboxId={sb.id}
              execId={activeExec}
              {...(eventSourceFactory ? { factory: eventSourceFactory } : {})}
            />
          </div>
        ) : null}
      </section>
    </div>
  );
}
