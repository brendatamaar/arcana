import { useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useExecOutput } from "../api/output";
import type { EventSourceFactory } from "../api/live";
import type { ColorIndex, Style } from "../lib/ansi";
import type { Line } from "../lib/lines";

// Whole class names, so Tailwind can generate them. ANSI colours map onto this fixed set only.
const FG: Record<ColorIndex, string> = {
  0: "text-slate-400", 1: "text-red-400", 2: "text-green-400", 3: "text-yellow-300",
  4: "text-blue-400", 5: "text-fuchsia-400", 6: "text-cyan-300", 7: "text-slate-100",
  8: "text-slate-300", 9: "text-red-300", 10: "text-green-300", 11: "text-yellow-200",
  12: "text-blue-300", 13: "text-fuchsia-300", 14: "text-cyan-200", 15: "text-white",
}; // prettier-ignore
const BG: Record<ColorIndex, string> = {
  0: "bg-slate-800", 1: "bg-red-900", 2: "bg-green-900", 3: "bg-yellow-900",
  4: "bg-blue-900", 5: "bg-fuchsia-900", 6: "bg-cyan-900", 7: "bg-slate-600",
  8: "bg-slate-700", 9: "bg-red-800", 10: "bg-green-800", 11: "bg-yellow-800",
  12: "bg-blue-800", 13: "bg-fuchsia-800", 14: "bg-cyan-800", 15: "bg-slate-500",
}; // prettier-ignore

function styleClasses(style: Style): string {
  const classes: string[] = [];
  if (style.inverse) classes.push("bg-slate-200 text-slate-900");
  else {
    if (style.fg !== undefined) classes.push(FG[style.fg]);
    if (style.bg !== undefined) classes.push(BG[style.bg]);
  }
  if (style.bold) classes.push("font-bold");
  if (style.dim) classes.push("opacity-70");
  if (style.italic) classes.push("italic");
  if (style.underline) classes.push("underline");
  return classes.join(" ");
}

const ROW_HEIGHT = 20;
const FOLLOW_SLACK = 24;

function Row({ line }: { line: Line }) {
  return (
    <>
      {line.segments.map((segment, i) => (
        // Segment text is a React text child: HTML in the output is shown, never interpreted.
        <span key={i} className={styleClasses(segment.style)}>
          {segment.text}
        </span>
      ))}
      {line.segments.length === 0 ? "​" : null}
    </>
  );
}

interface Props {
  sandboxId: string;
  execId: string;
  /** Replaces EventSource, for tests. */
  factory?: EventSourceFactory;
}

/** Live, virtualised view of an exec's output, following new lines unless the user scrolls up. */
export function LogViewer({ sandboxId, execId, factory }: Props) {
  const { buffer, version, status, end, gap } = useExecOutput(sandboxId, execId, factory);
  const scroller = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const count = buffer.length;
  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scroller.current,
    estimateSize: () => ROW_HEIGHT,
    overscan: 20,
  });

  useEffect(() => {
    if (following && count > 0) virtualizer.scrollToIndex(count - 1, { align: "end" });
  }, [version, count, following, virtualizer]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    setFollowing(el.scrollHeight - el.scrollTop - el.clientHeight < FOLLOW_SLACK);
  };

  const summary =
    status === "ended" && end
      ? end.expired
        ? `Output is no longer retained. ${exitText(end)}`
        : exitText(end)
      : status === "error"
        ? "The output stream was interrupted."
        : status === "connecting"
          ? "Connecting…"
          : "Running…";

  return (
    <section aria-label="Command output" className="space-y-2">
      <div className="flex items-center justify-between gap-2 text-sm">
        <p role="status" aria-live="polite">
          {summary}
        </p>
        {!following ? (
          <button
            type="button"
            onClick={() => setFollowing(true)}
            className="rounded border border-slate-400 px-2 py-1 hover:bg-slate-100 dark:hover:bg-slate-800"
          >
            Jump to latest
          </button>
        ) : null}
      </div>
      {gap || buffer.dropped > 0 ? (
        <p className="text-xs text-amber-800 dark:text-amber-300">
          Older output was discarded to stay within memory limits.
        </p>
      ) : null}
      <div
        ref={scroller}
        onScroll={onScroll}
        tabIndex={0}
        role="log"
        aria-label="Output lines"
        className="h-80 overflow-auto rounded border border-slate-700 bg-slate-950 font-mono text-xs leading-5 text-slate-100"
      >
        <div className="relative min-w-max" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((item) => {
            const line = buffer.lineAt(item.index);
            if (!line) return null;
            return (
              <div
                key={line.id}
                data-stream={line.stream}
                className={`absolute left-0 whitespace-pre px-2 ${line.stream === "stderr" ? "bg-red-950/40" : ""}`}
                style={{ top: 0, transform: `translateY(${item.start}px)`, height: ROW_HEIGHT }}
              >
                <Row line={line} />
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}

function exitText(end: {
  exit_code: number | null;
  timed_out: boolean;
  cancelled: boolean;
  truncated: boolean;
}): string {
  const parts = [
    end.exit_code === null ? "Exit status unknown" : `Exited with status ${end.exit_code}`,
  ];
  if (end.timed_out) parts.push("timed out");
  if (end.cancelled) parts.push("cancelled");
  if (end.truncated) parts.push("output truncated by the server");
  return parts.join(", ") + ".";
}
