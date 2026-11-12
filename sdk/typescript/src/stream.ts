import WebSocket from "ws";
import { ArcanaError, errorFromResponse } from "./errors.js";

/** Options for a streaming exec; set exactly one of `cmd` or `shell`. */
export interface StreamRequest {
  cmd?: string[];
  shell?: string;
  cwd?: string;
  env?: Record<string, string>;
  timeoutSeconds?: number;
  tty?: boolean;
  cols?: number;
  rows?: number;
}

/** One chunk of command output. Data is binary-safe, not decoded text. */
export interface StreamEvent {
  type: "stdout" | "stderr";
  data: Uint8Array;
}

/** The terminal result of a command. `exitCode` is the command's real exit status. */
export interface ExecExit {
  exitCode: number;
  timedOut: boolean;
  truncated: boolean;
  cancelled: boolean;
  durationMs: number;
}

const MAX_FRAME = 65536;
const STDIN_CHUNK = 16384;

type Frame = { type: string } & Record<string, unknown>;

/**
 * A running command over WebSocket. Iterate it (`for await`) to receive output, or call
 * {@link ExecStream.collect}; `result` settles when the command exits. Output is buffered in
 * memory until consumed, bounded by the server's per-exec output cap.
 */
export class ExecStream implements AsyncIterable<StreamEvent> {
  /** The exec ID assigned by the server. */
  readonly execId: string;
  /** Resolves with the terminal result; rejects if the connection ends before `exit`. */
  readonly result: Promise<ExecExit>;

  private readonly queue: StreamEvent[] = [];
  private waiter: (() => void) | undefined;
  private finished = false;
  private stdinClosed = false;

  /** @internal Use {@link connectExecStream}. */
  constructor(
    private readonly socket: WebSocket,
    execId: string,
    private readonly events: { onFrame: (handler: (frame: Frame) => void) => void },
    resultPromise: Promise<ExecExit>,
  ) {
    this.execId = execId;
    this.result = resultPromise;
    this.result.then(
      () => this.end(),
      () => this.end(),
    );
    events.onFrame((frame) => {
      if (frame.type === "stdout" || frame.type === "stderr") {
        this.queue.push({ type: frame.type, data: Buffer.from(String(frame.data ?? ""), "base64") });
        this.wake();
      }
    });
    // Avoid an unhandled rejection when callers only iterate.
    this.result.catch(() => undefined);
  }

  private wake(): void {
    const waiter = this.waiter;
    this.waiter = undefined;
    waiter?.();
  }

  private end(): void {
    this.finished = true;
    this.wake();
  }

  async *[Symbol.asyncIterator](): AsyncIterator<StreamEvent> {
    for (;;) {
      const next = this.queue.shift();
      if (next) {
        yield next;
        continue;
      }
      if (this.finished) {
        await this.result; // surface a transport failure instead of ending silently
        return;
      }
      await new Promise<void>((resolve) => {
        this.waiter = resolve;
      });
    }
  }

  /** Waits for the command to exit and returns all output as UTF-8 text with the result. */
  async collect(): Promise<{ stdout: string; stderr: string; exit: ExecExit }> {
    const out: Buffer[] = [];
    const err: Buffer[] = [];
    for await (const event of this) (event.type === "stdout" ? out : err).push(Buffer.from(event.data));
    return { stdout: Buffer.concat(out).toString("utf8"), stderr: Buffer.concat(err).toString("utf8"), exit: await this.result };
  }

  private send(frame: Frame): Promise<void> {
    const text = JSON.stringify(frame);
    if (Buffer.byteLength(text) > MAX_FRAME) return Promise.reject(new RangeError("frame exceeds 64 KiB"));
    return new Promise((resolve, reject) => this.socket.send(text, (error) => (error ? reject(error) : resolve())));
  }

  /** Sends binary-safe stdin in bounded frames. */
  async write(data: Uint8Array | string): Promise<void> {
    if (this.stdinClosed) throw new Error("stdin is closed");
    const bytes = typeof data === "string" ? Buffer.from(data, "utf8") : Buffer.from(data);
    for (let offset = 0; offset < bytes.length; offset += STDIN_CHUNK) {
      await this.send({ type: "stdin", data: bytes.subarray(offset, offset + STDIN_CHUNK).toString("base64") });
    }
  }

  /** Signals end of input without cancelling the command. */
  async closeStdin(): Promise<void> {
    if (this.stdinClosed) return;
    this.stdinClosed = true;
    await this.send({ type: "stdin_close" });
  }

  /** Updates a TTY's size. */
  resize(cols: number, rows: number): Promise<void> {
    return this.send({ type: "resize", cols, rows });
  }

  /** Asks the server to kill the whole process tree; keep draining output until `result` settles. */
  cancel(): Promise<void> {
    return this.send({ type: "cancel" });
  }

  /** Disconnects. The server cancels a command whose client disconnects before `exit`. */
  close(): void {
    this.socket.terminate();
  }
}

/** @internal */
export async function connectExecStream(
  baseUrl: string,
  apiKey: string,
  sandboxId: string,
  request: StreamRequest,
  signal?: AbortSignal,
): Promise<ExecStream> {
  if ((request.cmd === undefined) === (request.shell === undefined)) {
    throw new TypeError("set exactly one of cmd or shell");
  }
  const url = new URL(`/v1/sandboxes/${encodeURIComponent(sandboxId)}/exec/stream`, baseUrl);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  const socket = new WebSocket(url, { headers: { Authorization: `Bearer ${apiKey}` }, maxPayload: MAX_FRAME * 2, followRedirects: false });
  signal?.addEventListener("abort", () => socket.terminate(), { once: true });

  const handlers: Array<(frame: Frame) => void> = [];
  // Output can arrive in the same tick as `started`, before the ExecStream exists.
  const pending: Frame[] = [];
  let resolveResult!: (value: ExecExit) => void;
  let rejectResult!: (error: unknown) => void;
  const result = new Promise<ExecExit>((resolve, reject) => {
    resolveResult = resolve;
    rejectResult = reject;
  });
  // Nobody observes `result` if the stream fails before `started`; callers see that failure
  // through the rejected connect instead, so this must not surface as an unhandled rejection.
  result.catch(() => undefined);
  let settled = false;
  const settle = (action: () => void): void => {
    if (!settled) {
      settled = true;
      action();
    }
  };

  const started = new Promise<string>((resolve, reject) => {
    socket.once("unexpected-response", (_request, response) => {
      const chunks: Buffer[] = [];
      response.on("data", (chunk: Buffer) => chunks.length < 64 && chunks.push(chunk));
      response.on("end", () => {
        const headers = new Headers();
        if (response.headers["retry-after"]) headers.set("Retry-After", String(response.headers["retry-after"]));
        void errorFromResponse(new Response(Buffer.concat(chunks), { status: response.statusCode ?? 500, headers })).then(reject);
      });
    });
    socket.once("error", (error) => {
      reject(error);
      settle(() => rejectResult(error));
    });
    socket.once("open", () => {
      const start: Record<string, unknown> = { type: "start" };
      if (request.cmd !== undefined) start.cmd = request.cmd;
      if (request.shell !== undefined) start.shell = request.shell;
      if (request.cwd !== undefined) start.cwd = request.cwd;
      if (request.env !== undefined) start.env = request.env;
      if (request.timeoutSeconds !== undefined) start.timeout_seconds = request.timeoutSeconds;
      if (request.tty) {
        start.tty = true;
        if (request.cols !== undefined) start.cols = request.cols;
        if (request.rows !== undefined) start.rows = request.rows;
      }
      socket.send(JSON.stringify(start));
    });
    socket.on("message", (data) => {
      let frame: Frame;
      try {
        frame = JSON.parse(data.toString()) as Frame;
      } catch {
        settle(() => rejectResult(new Error("invalid exec message")));
        socket.terminate();
        return;
      }
      switch (frame.type) {
        case "started":
          resolve(String(frame.exec_id ?? ""));
          break;
        case "exit":
          settle(() =>
            resolveResult({
              exitCode: Number(frame.exit_code),
              timedOut: Boolean(frame.timed_out),
              truncated: Boolean(frame.truncated),
              cancelled: Boolean(frame.cancelled),
              durationMs: Number(frame.duration_ms),
            }),
          );
          break;
        case "error": {
          const failure = new ArcanaError({ status: 0, code: String(frame.code ?? "internal"), message: String(frame.message ?? "exec failed") });
          reject(failure);
          settle(() => rejectResult(failure));
          break;
        }
        default:
          if (handlers.length === 0) pending.push(frame);
          else for (const handler of handlers) handler(frame);
      }
    });
    socket.once("close", () => {
      const failure = new Error("exec stream closed before the command exited");
      reject(failure);
      settle(() => rejectResult(failure));
    });
  });

  const execId = await started;
  return new ExecStream(
    socket,
    execId,
    {
      onFrame: (handler) => {
        handlers.push(handler);
        for (const frame of pending.splice(0)) handler(frame);
      },
    },
    result,
  );
}
