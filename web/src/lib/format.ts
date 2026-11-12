/** Formats a number of seconds as a short duration such as "12m 03s" or "2h 05m". */
export function formatDuration(totalSeconds: number): string {
  const seconds = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

export function formatTime(iso: string | undefined | null): string {
  if (!iso) return "-";
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

/** Human wording for the termination reasons Arcana records; unknown reasons are shown as given. */
export function describeReason(reason: string | null | undefined): string {
  switch (reason) {
    case "ttl":
      return "reached its time-to-live";
    case "idle":
      return "idle timeout";
    case "exec_deadline":
      return "a command exceeded its deadline";
    case "disk_quota":
      return "exceeded its disk quota";
    case "requested":
      return "destroyed on request";
    case "lost":
      return "lost (container disappeared)";
    case "restore_failed":
      return "snapshot restore failed";
    case "shutdown":
      return "server shutdown";
    case undefined:
    case null:
    case "":
      return "no reason recorded";
    default:
      return reason;
  }
}

export interface Deadlines {
  /** Seconds until the absolute expiry. */
  ttl: number;
  /** Seconds until the idle timeout, or null when idle expiry is disabled. Never beyond the TTL. */
  idle: number | null;
}

/** Remaining time before a sandbox expires, as the reaper will see it (TTL wins over idle). */
export function deadlines(
  sandbox: { expires_at: string; last_activity_at?: string; idle_timeout_seconds?: number },
  now: number,
): Deadlines {
  const ttl = (Date.parse(sandbox.expires_at) - now) / 1000;
  const idleSeconds = sandbox.idle_timeout_seconds ?? 0;
  if (!sandbox.last_activity_at || idleSeconds <= 0) return { ttl, idle: null };
  const idle = (Date.parse(sandbox.last_activity_at) + idleSeconds * 1000 - now) / 1000;
  return { ttl, idle: Math.min(idle, ttl) };
}
