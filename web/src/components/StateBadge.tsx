import type { SandboxState } from "../api/client";

// Class names are listed in full so Tailwind can see them; the state itself is never turned into a class.
const STYLES: Record<SandboxState, string> = {
  creating: "bg-amber-100 text-amber-900 dark:bg-amber-900/40 dark:text-amber-200",
  running: "bg-emerald-100 text-emerald-900 dark:bg-emerald-900/40 dark:text-emerald-200",
  destroying: "bg-orange-100 text-orange-900 dark:bg-orange-900/40 dark:text-orange-200",
  destroyed: "bg-slate-200 text-slate-800 dark:bg-slate-700 dark:text-slate-200",
  failed: "bg-red-100 text-red-900 dark:bg-red-900/40 dark:text-red-200",
  expired: "bg-slate-200 text-slate-800 dark:bg-slate-700 dark:text-slate-200",
};

export function StateBadge({ state }: { state: SandboxState }) {
  const style = Object.hasOwn(STYLES, state) ? STYLES[state] : STYLES.destroyed;
  return (
    <span className={`inline-block rounded px-2 py-0.5 text-xs font-medium ${style}`}>{state}</span>
  );
}
