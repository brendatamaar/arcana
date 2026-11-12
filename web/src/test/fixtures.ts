import type { components } from "../api/schema";

type Sandbox = components["schemas"]["Sandbox"];
type ExecRecord = components["schemas"]["ExecRecord"];

export function sandbox(overrides: Partial<Sandbox> = {}): Sandbox {
  const now = Date.now();
  return {
    id: "sbx_a1b2c3d4e5f6g7h8i9j0",
    state: "running",
    template: "python",
    runtime: "runsc",
    source: "cold",
    resources: { cpus: 1, memory_mb: 1024, disk_mb: 2048, pids: 256 },
    network: { mode: "none", allow: [] },
    labels: { task: "fix-123" },
    created_at: new Date(now - 60_000).toISOString(),
    expires_at: new Date(now + 30 * 60_000).toISOString(),
    last_activity_at: new Date(now - 10_000).toISOString(),
    idle_timeout_seconds: 600,
    ...overrides,
  };
}

export function exec(overrides: Partial<ExecRecord> = {}): ExecRecord {
  return {
    id: "exe_k3j9w2m4p8q1r7t5v6x0",
    sandbox_id: "sbx_a1b2c3d4e5f6g7h8i9j0",
    cmd: ["pytest", "-q"],
    cwd: "/workspace",
    tty: false,
    started_at: new Date(Date.now() - 5_000).toISOString(),
    ended_at: new Date(Date.now() - 1_000).toISOString(),
    exit_code: 1,
    timed_out: false,
    cancelled: false,
    truncated: false,
    stdout_bytes: 10,
    stderr_bytes: 0,
    ...overrides,
  };
}

/** Text that would be dangerous if a view ever interpreted sandbox-controlled data as markup. */
export const HOSTILE = [
  "<script>window.__pwned = true</script>",
  '<img src=x onerror="window.__pwned=true">',
  '"><iframe src="javascript:alert(1)"></iframe>',
  '<a href="javascript:alert(1)">click</a>',
  "<style>body{display:none}</style>",
].join(" ");

/** Asserts that nothing hostile became live DOM, and that the payload is visible as plain text. */
export function expectInert(root: HTMLElement): void {
  const live = root.querySelectorAll(
    "script, img, iframe, style, object, embed, svg, link, a[href^='javascript']",
  );
  const withHandlers = [...root.querySelectorAll("*")].filter((el) =>
    el.getAttributeNames().some((name) => name.startsWith("on")),
  );
  if (live.length > 0 || withHandlers.length > 0) {
    throw new Error(
      `hostile markup became live DOM: ${[...live, ...withHandlers].map((el) => el.outerHTML).join(" | ")}`,
    );
  }
  if ((window as unknown as { __pwned?: boolean }).__pwned) throw new Error("script ran");
}
