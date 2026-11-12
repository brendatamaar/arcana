import createClient from "openapi-fetch";
import { ArcanaError, errorFromResponse } from "./errors.js";
import type { components, paths } from "./schema.js";
import { connectExecStream, type ExecStream, type StreamRequest } from "./stream.js";

export type Sandbox = components["schemas"]["Sandbox"];
export type SandboxList = components["schemas"]["SandboxList"];
export type CreateSandbox = components["schemas"]["CreateSandbox"];
export type ExecResult = components["schemas"]["ExecResult"];
export type FileEntry = components["schemas"]["FileEntry"];
export type Snapshot = components["schemas"]["Snapshot"];
export type SnapshotList = components["schemas"]["SnapshotList"];
export type ForkRequest = components["schemas"]["ForkRequest"];
export type ForkResult = components["schemas"]["ForkResult"];
export type Template = components["schemas"]["Template"];
export type Key = components["schemas"]["Key"];
export type CreatedKey = components["schemas"]["CreatedKey"];
export type AuditList = components["schemas"]["AuditList"];

/** Sync exec options; set exactly one of `cmd` or `shell`. `stdin` is sent as bytes. */
export interface ExecRequest {
  cmd?: string[];
  shell?: string;
  cwd?: string;
  env?: Record<string, string>;
  timeoutSeconds?: number;
  stdin?: Uint8Array | string;
}

export interface ArcanaOptions {
  /** Base URL of the Arcana server, for example `http://127.0.0.1:7070`. */
  url: string;
  /** API key (`arc_live_...`). Never logged or included in errors. */
  apiKey: string;
  /** Replace the global `fetch` (for tests or proxies). */
  fetch?: typeof fetch;
}

/** Per-call options. */
export interface RequestOptions {
  signal?: AbortSignal;
}

export interface Page {
  limit?: number;
  cursor?: string;
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/**
 * Client for the Arcana API. Methods throw {@link ArcanaError} for API failures. Mutating calls
 * (create, exec, fork, ...) are never retried automatically.
 */
export class Arcana {
  readonly sandboxes: SandboxesApi;
  readonly files: FilesApi;
  readonly snapshots: SnapshotsApi;
  readonly admin: AdminApi;
  private readonly baseUrl: string;
  private readonly apiKey: string;
  private readonly fetchImpl: typeof fetch;

  constructor(options: ArcanaOptions) {
    if (!options.url) throw new TypeError("url is required");
    if (!options.apiKey) throw new TypeError("apiKey is required");
    this.baseUrl = options.url;
    this.apiKey = options.apiKey;
    const fetchImpl = options.fetch ?? globalThis.fetch;
    this.fetchImpl = fetchImpl;
    const typed = createClient<paths>({
      baseUrl: options.url.replace(/\/+$/, ""),
      headers: { Authorization: `Bearer ${options.apiKey}` },
      fetch: (request: Request) => fetchImpl(request, { redirect: "error" }),
    });
    const transport: Transport = {
      typed,
      raw: (method, path, init) => this.raw(fetchImpl, method, path, init),
      stream: (id, request, signal) => connectExecStream(this.baseUrl, this.apiKey, id, request, signal),
    };
    this.sandboxes = new SandboxesApi(transport);
    this.files = new FilesApi(transport);
    this.snapshots = new SnapshotsApi(transport);
    this.admin = new AdminApi(transport);
  }

  /** Liveness check; the only unauthenticated endpoint. */
  async health(options?: RequestOptions): Promise<{ status: string }> {
    const response = await this.fetchImpl(new URL("/v1/health", this.baseUrl), { signal: options?.signal ?? null, redirect: "error" });
    if (!response.ok) throw await errorFromResponse(response);
    return (await response.json()) as { status: string };
  }

  /** Lists the configured templates. */
  async templates(options?: RequestOptions): Promise<Template[]> {
    // Reuse the sandbox transport: it already carries auth.
    return (await this.sandboxes.templates(options)).items;
  }

  private async raw(fetchImpl: typeof fetch, method: string, path: string, init: RawInit): Promise<Response> {
    const url = new URL(path, this.baseUrl);
    for (const [key, value] of Object.entries(init.query ?? {})) url.searchParams.set(key, value);
    const headers: Record<string, string> = { Authorization: `Bearer ${this.apiKey}` };
    if (init.contentType) headers["Content-Type"] = init.contentType;
    const request: RequestInit & { duplex?: "half" } = { method, headers, redirect: "error" };
    if (init.signal) request.signal = init.signal;
    if (init.body !== undefined) {
      request.body = init.body as BodyInit;
      request.duplex = "half";
    }
    const response = await fetchImpl(url, request);
    if (!response.ok) throw await errorFromResponse(response);
    return response;
  }
}

interface RawInit {
  query?: Record<string, string>;
  body?: Uint8Array | ReadableStream<Uint8Array> | string;
  contentType?: string;
  signal?: AbortSignal | undefined;
}

interface Transport {
  typed: ReturnType<typeof createClient<paths>>;
  raw: (method: string, path: string, init: RawInit) => Promise<Response>;
  stream: (id: string, request: StreamRequest, signal?: AbortSignal) => Promise<ExecStream>;
}

async function unwrap<T>(pending: Promise<Result<T>>): Promise<T> {
  const { data, error, response } = await pending;
  if (error !== undefined || data === undefined) {
    if (error !== undefined && typeof error === "object" && error !== null && "error" in error) {
      const envelope = (error as { error: { code: string; message: string; request_id: string } }).error;
      const retry = Number.parseInt(response.headers.get("Retry-After") ?? "", 10);
      throw new ArcanaError({
        status: response.status,
        code: envelope.code,
        message: envelope.message,
        requestId: envelope.request_id,
        ...(Number.isFinite(retry) && retry > 0 ? { retryAfterSeconds: retry } : {}),
      });
    }
    throw new ArcanaError({ status: response.status, code: "internal", message: `HTTP ${response.status}` });
  }
  return data;
}

async function unwrapEmpty(pending: Promise<{ error?: unknown; response: Response }>): Promise<void> {
  const { error, response } = await pending;
  if (error !== undefined) {
    await unwrap(Promise.resolve({ error, response } as Result<never>));
  }
}

function bytes(data: Uint8Array | string): Uint8Array {
  return typeof data === "string" ? new TextEncoder().encode(data) : data;
}

const idOk = (id: string): string => {
  if (!id || id.length > 128 || /[/\\?#%]/.test(id)) throw new TypeError("invalid resource ID");
  return id;
};

/** Sandbox lifecycle and command execution. */
export class SandboxesApi {
  constructor(private readonly t: Transport) {}

  create(body: CreateSandbox, options?: RequestOptions): Promise<Sandbox> {
    return unwrap(this.t.typed.POST("/v1/sandboxes", { body, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  get(id: string, options?: RequestOptions): Promise<Sandbox> {
    return unwrap(this.t.typed.GET("/v1/sandboxes/{id}", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  list(query: Page & { state?: Sandbox["state"]; label?: string } = {}, options?: RequestOptions): Promise<SandboxList> {
    return unwrap(this.t.typed.GET("/v1/sandboxes", { params: { query }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  /** Idempotently destroys a sandbox. */
  destroy(id: string, options?: RequestOptions): Promise<void> {
    return unwrapEmpty(this.t.typed.DELETE("/v1/sandboxes/{id}", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  /** Resets the idle timer without extending the absolute expiry. */
  keepalive(id: string, options?: RequestOptions): Promise<Sandbox> {
    return unwrap(this.t.typed.POST("/v1/sandboxes/{id}/keepalive", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  /** Runs a command and waits. `result.exit_code` is the command's real exit status. */
  exec(id: string, request: ExecRequest, options?: RequestOptions): Promise<ExecResult> {
    if ((request.cmd === undefined) === (request.shell === undefined)) throw new TypeError("set exactly one of cmd or shell");
    const body: components["schemas"]["ExecRequest"] = {};
    if (request.cmd !== undefined) body.cmd = request.cmd;
    if (request.shell !== undefined) body.shell = request.shell;
    if (request.cwd !== undefined) body.cwd = request.cwd;
    if (request.env !== undefined) body.env = request.env;
    if (request.timeoutSeconds !== undefined) body.timeout_seconds = request.timeoutSeconds;
    if (request.stdin !== undefined) body.stdin = Buffer.from(bytes(request.stdin)).toString("base64");
    return unwrap(this.t.typed.POST("/v1/sandboxes/{id}/exec", { params: { path: { id: idOk(id) } }, body, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  /** Starts a command with live, binary-safe output and stdin (WebSocket). */
  execStream(id: string, request: StreamRequest, options?: RequestOptions): Promise<ExecStream> {
    return this.t.stream(idOk(id), request, options?.signal);
  }

  /** @internal Used by {@link Arcana.templates}. */
  templates(options?: RequestOptions): Promise<{ items: Template[] }> {
    return unwrap(this.t.typed.GET("/v1/templates", { ...(options?.signal ? { signal: options.signal } : {}) }));
  }
}

/** File transfer. All operations run inside the sandbox. */
export class FilesApi {
  constructor(private readonly t: Transport) {}

  /** Reads a file's bytes. */
  async read(id: string, path: string, options?: RequestOptions): Promise<Uint8Array> {
    const response = await this.t.raw("GET", `/v1/sandboxes/${idOk(id)}/files`, { query: { path }, signal: options?.signal });
    return new Uint8Array(await response.arrayBuffer());
  }

  /** Atomically writes a file, creating parent directories. */
  async write(id: string, path: string, data: Uint8Array | string, options?: RequestOptions): Promise<void> {
    await this.t.raw("PUT", `/v1/sandboxes/${idOk(id)}/files`, { query: { path }, body: bytes(data), contentType: "application/octet-stream", signal: options?.signal });
  }

  async delete(id: string, path: string, recursive = false, options?: RequestOptions): Promise<void> {
    await this.t.raw("DELETE", `/v1/sandboxes/${idOk(id)}/files`, { query: { path, recursive: String(recursive) }, signal: options?.signal });
  }

  async list(id: string, path: string, depth = 1, options?: RequestOptions): Promise<FileEntry[]> {
    const response = await this.t.raw("GET", `/v1/sandboxes/${idOk(id)}/files/list`, { query: { path, depth: String(depth) }, signal: options?.signal });
    return ((await response.json()) as { items: FileEntry[] }).items;
  }

  /** Downloads a directory as a `.tar.gz`. */
  async downloadArchive(id: string, path: string, options?: RequestOptions): Promise<Uint8Array> {
    const response = await this.t.raw("GET", `/v1/sandboxes/${idOk(id)}/files/archive`, { query: { path }, signal: options?.signal });
    return new Uint8Array(await response.arrayBuffer());
  }

  /** Uploads a `.tar.gz`, extracted inside the sandbox. */
  async uploadArchive(id: string, path: string, archive: Uint8Array, options?: RequestOptions): Promise<void> {
    await this.t.raw("PUT", `/v1/sandboxes/${idOk(id)}/files/archive`, { query: { path }, body: archive, contentType: "application/gzip", signal: options?.signal });
  }
}

/** Workspace snapshots and forks. Snapshots capture `/workspace` only. */
export class SnapshotsApi {
  constructor(private readonly t: Transport) {}

  create(sandboxId: string, options?: RequestOptions): Promise<Snapshot> {
    return unwrap(this.t.typed.POST("/v1/sandboxes/{id}/snapshots", { params: { path: { id: idOk(sandboxId) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  list(query: Page = {}, options?: RequestOptions): Promise<SnapshotList> {
    return unwrap(this.t.typed.GET("/v1/snapshots", { params: { query }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  get(id: string, options?: RequestOptions): Promise<Snapshot> {
    return unwrap(this.t.typed.GET("/v1/snapshots/{id}", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  delete(id: string, options?: RequestOptions): Promise<void> {
    return unwrapEmpty(this.t.typed.DELETE("/v1/snapshots/{id}", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  /**
   * Creates `body.count` sandboxes from a snapshot. Resolves when at least one was created; check
   * `failures` for the rest. Rejects with the first failure when none could be created.
   */
  fork(id: string, body: ForkRequest, options?: RequestOptions): Promise<ForkResult> {
    return unwrap(this.t.typed.POST("/v1/snapshots/{id}/fork", { params: { path: { id: idOk(id) } }, body, ...(options?.signal ? { signal: options.signal } : {}) }));
  }
}

/** Key management and the audit log; these require an admin key. */
export class AdminApi {
  constructor(private readonly t: Transport) {}

  createKey(body: { name: string; is_admin?: boolean }, options?: RequestOptions): Promise<CreatedKey> {
    return unwrap(this.t.typed.POST("/v1/keys", { body, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  revokeKey(id: string, options?: RequestOptions): Promise<void> {
    return unwrapEmpty(this.t.typed.DELETE("/v1/keys/{id}", { params: { path: { id: idOk(id) } }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }

  audit(query: Page & { sandbox_id?: string; key_id?: string; action?: string; since?: string; until?: string } = {}, options?: RequestOptions): Promise<AuditList> {
    return unwrap(this.t.typed.GET("/v1/audit", { params: { query }, ...(options?.signal ? { signal: options.signal } : {}) }));
  }
}
