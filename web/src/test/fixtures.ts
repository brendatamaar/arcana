import type { components } from "../api/schema";

type Sandbox = components["schemas"]["Sandbox"];
type ExecRecord = components["schemas"]["ExecRecord"];
type Snapshot = components["schemas"]["Snapshot"];
type Key = components["schemas"]["Key"];
type AuditEvent = components["schemas"]["AuditEvent"];
type Template = components["schemas"]["Template"];

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

export function snapshot(overrides: Partial<Snapshot> = {}): Snapshot {
  return {
    id: "snp_h4k2m9q7w3x8c5v1b6n0",
    source_sandbox_id: "sbx_a1b2c3d4e5f6g7h8i9j0",
    template: "python",
    resources: { cpus: 1, memory_mb: 1024, disk_mb: 2048, pids: 256 },
    network: { mode: "none", allow: [] },
    env_keys: [],
    size_bytes: 18_432_011,
    sha256: "9f2c",
    created_at: "2026-11-12T10:05:00Z",
    ...overrides,
  };
}

export function key(overrides: Partial<Key> = {}): Key {
  return {
    id: "key_a1b2c3d4e5f6g7h8i9j0",
    name: "ci",
    prefix: "arc_live",
    is_admin: false,
    created_at: "2026-11-01T09:00:00Z",
    ...overrides,
  };
}

export function auditEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: "aud_a1b2c3d4e5f6g7h8i9j0",
    ts: "2026-11-12T10:00:00Z",
    key_id: "key_a1b2c3d4e5f6g7h8i9j0",
    sandbox_id: "sbx_a1b2c3d4e5f6g7h8i9j0",
    action: "exec.started",
    details: { argv: ["ls"] },
    ...overrides,
  };
}

export function template(overrides: Partial<Template> = {}): Template {
  return {
    name: "python",
    image: "ghcr.io/brendatamaar/arcana-python:latest",
    resources: { cpus: 1, memory_mb: 1024, disk_mb: 2048, pids: 256 },
    ttl_seconds: 1800,
    idle_timeout_seconds: 600,
    workdir: "/workspace",
    network_mode: "none",
    warm_pool: 0,
    env_keys: [],
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
