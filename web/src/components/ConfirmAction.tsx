import { type ReactNode, useState } from "react";

/**
 * A destructive action that needs a second, explicit click. The confirmation is part of the page,
 * not a browser dialog, so it is keyboard- and screen-reader-friendly and testable.
 */
export function ConfirmAction({
  label,
  confirmLabel,
  question,
  busy,
  onConfirm,
  children,
}: {
  label: string;
  confirmLabel: string;
  question: string;
  busy: boolean;
  onConfirm: () => void;
  /** Extra fields shown with the confirmation (for example a re-entered key). */
  children?: ReactNode;
}) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <button
        type="button"
        onClick={() => setAsking(true)}
        className="rounded border border-red-500 px-3 py-1.5 text-sm text-red-800 hover:bg-red-50 dark:text-red-300 dark:hover:bg-red-950"
      >
        {label}
      </button>
    );
  }
  return (
    <div
      role="group"
      aria-label={question}
      className="flex flex-wrap items-end gap-3 rounded border border-red-300 p-3 dark:border-red-800"
    >
      <p className="w-full text-sm font-medium">{question}</p>
      {children}
      <button
        type="button"
        disabled={busy}
        onClick={onConfirm}
        className="rounded bg-red-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-red-800 disabled:opacity-60"
      >
        {busy ? "Working…" : confirmLabel}
      </button>
      <button
        type="button"
        disabled={busy}
        onClick={() => setAsking(false)}
        className="rounded border border-slate-400 px-3 py-1.5 text-sm hover:bg-slate-100 disabled:opacity-60 dark:hover:bg-slate-800"
      >
        Cancel
      </button>
    </div>
  );
}
