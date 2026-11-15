import { type SyntheticEvent, useId, useMemo, useState } from "react";
import { useCreateKey, useKeys, useRevokeKey, useSession } from "../api/hooks";
import { ConfirmAction } from "../components/ConfirmAction";
import { ErrorNotice, Loading } from "../components/Notice";
import { formatTime } from "../lib/format";

const field = "rounded border border-slate-400 bg-white px-2 py-1.5 text-sm dark:bg-slate-900";

/** The admin key typed again for one action. It is sent in a header and kept nowhere else. */
function ReauthField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const id = useId();
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-sm font-medium">
        Your admin API key
      </label>
      <input
        id={id}
        type="password"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className={`${field} w-72 font-mono`}
      />
    </div>
  );
}

/**
 * Shown once after creation. The secret lives in the parent's state and nowhere else, so it is
 * gone when the user dismisses it or leaves the page.
 */
function NewSecret({ secret, onDone }: { secret: string; onDone: () => void }) {
  const [copied, setCopied] = useState<"yes" | "no" | null>(null);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      setCopied("yes");
    } catch {
      setCopied("no");
    }
  };
  return (
    <section
      aria-label="New API key"
      className="space-y-2 rounded border border-amber-400 bg-amber-50 p-3 text-amber-950 dark:border-amber-700 dark:bg-amber-950 dark:text-amber-100"
    >
      <p className="text-sm font-medium">
        Copy this key now. It is shown only once and cannot be recovered.
      </p>
      <code className="block break-all rounded bg-white p-2 font-mono text-sm text-slate-900">
        {secret}
      </code>
      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => void copy()}
          className="rounded border border-slate-500 px-3 py-1 text-sm hover:bg-amber-100 dark:hover:bg-amber-900"
        >
          Copy
        </button>
        <button
          type="button"
          onClick={onDone}
          className="rounded bg-slate-800 px-3 py-1 text-sm text-white hover:bg-slate-700"
        >
          I have saved it
        </button>
      </div>
      {copied === "yes" ? (
        <p role="status" className="text-sm">
          Copied.
        </p>
      ) : null}
      {copied === "no" ? (
        <p role="status" className="text-sm">
          Could not copy automatically. Select the key and copy it by hand.
        </p>
      ) : null}
    </section>
  );
}

function CreateKey() {
  const [name, setName] = useState("");
  const [isAdmin, setIsAdmin] = useState(false);
  const [adminKey, setAdminKey] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const create = useCreateKey();
  const ids = { name: useId(), admin: useId() };

  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    create.mutate(
      { name: name.trim(), isAdmin, adminKey },
      {
        onSuccess: (created) => {
          setSecret(created.secret);
          setName("");
          setIsAdmin(false);
        },
        onSettled: () => setAdminKey(""),
      },
    );
  };

  return (
    <div className="space-y-3">
      <form
        onSubmit={submit}
        aria-label="Create API key"
        className="flex flex-wrap items-end gap-3 rounded border border-slate-300 bg-white p-3 dark:border-slate-800 dark:bg-slate-900"
      >
        <div>
          <label htmlFor={ids.name} className="mb-1 block text-sm font-medium">
            Name
          </label>
          <input
            id={ids.name}
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            className={field}
          />
        </div>
        <div className="flex items-center gap-2 pb-1.5">
          <input
            id={ids.admin}
            type="checkbox"
            checked={isAdmin}
            onChange={(e) => setIsAdmin(e.target.checked)}
          />
          <label htmlFor={ids.admin} className="text-sm">
            Admin key
          </label>
        </div>
        <ReauthField value={adminKey} onChange={setAdminKey} />
        <button
          type="submit"
          disabled={create.isPending || name.trim() === "" || adminKey.trim() === ""}
          className="rounded bg-blue-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-blue-800 disabled:opacity-60"
        >
          {create.isPending ? "Creating…" : "Create key"}
        </button>
      </form>
      {create.error ? <ErrorNotice error={create.error} /> : null}
      {secret ? <NewSecret secret={secret} onDone={() => setSecret(null)} /> : null}
    </div>
  );
}

function Revoke({ id }: { id: string }) {
  const [adminKey, setAdminKey] = useState("");
  const revoke = useRevokeKey();
  return (
    <div className="space-y-1">
      <ConfirmAction
        label="Revoke"
        confirmLabel="Revoke key"
        question="Revoke this key? Its sandboxes keep running, but the key and its dashboard sessions stop working at once."
        busy={revoke.isPending}
        onConfirm={() => revoke.mutate({ id, adminKey }, { onSettled: () => setAdminKey("") })}
      >
        <ReauthField value={adminKey} onChange={setAdminKey} />
      </ConfirmAction>
      {revoke.error ? <ErrorNotice error={revoke.error} /> : null}
    </div>
  );
}

export function Keys() {
  const session = useSession();
  const admin = session.data?.admin === true;
  const { data, error, isPending, hasNextPage, fetchNextPage, isFetchingNextPage } = useKeys(admin);
  const rows = useMemo(() => (data?.pages ?? []).flatMap((page) => page.items), [data]);

  if (session.isPending) return <Loading what="session" />;
  if (!admin)
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold">API keys</h1>
        <p role="alert" className="text-sm">
          Managing API keys needs an admin key. Sign in with one to continue.
        </p>
      </div>
    );
  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-semibold">API keys</h1>
      <p className="text-sm text-slate-600 dark:text-slate-400">
        Creating or revoking a key asks for your admin key again, so a stolen browser session cannot
        change keys on its own.
      </p>
      <CreateKey />
      {isPending ? <Loading what="keys" /> : null}
      {error ? <ErrorNotice error={error} /> : null}
      {rows.length > 0 ? (
        <div className="overflow-x-auto rounded border border-slate-300 bg-white dark:border-slate-800 dark:bg-slate-900">
          <table className="w-full text-left text-sm">
            <caption className="sr-only">API keys</caption>
            <thead className="bg-slate-100 text-xs uppercase dark:bg-slate-800">
              <tr>
                {["ID", "Name", "Prefix", "Role", "Created", "Status", "Revoke"].map((name) => (
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
                  <td className="px-4 py-2">{row.name}</td>
                  <td className="px-4 py-2 font-mono text-xs">{row.prefix}</td>
                  <td className="px-4 py-2">{row.is_admin ? "admin" : "standard"}</td>
                  <td className="px-4 py-2">{formatTime(row.created_at)}</td>
                  <td className="px-4 py-2">
                    {row.revoked_at ? `revoked ${formatTime(row.revoked_at)}` : "active"}
                  </td>
                  <td className="px-4 py-2">{row.revoked_at ? null : <Revoke id={row.id} />}</td>
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
