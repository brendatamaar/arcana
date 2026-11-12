/** An API failure. `code` is Arcana's error code (for example `quota_exceeded`). */
export class ArcanaError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  /** Seconds to wait before retrying, from `Retry-After` on `rate_limited` responses. */
  readonly retryAfterSeconds?: number;

  constructor(init: { status: number; code: string; message: string; requestId?: string; retryAfterSeconds?: number }) {
    super(init.message);
    this.name = "ArcanaError";
    this.status = init.status;
    this.code = init.code;
    this.requestId = init.requestId ?? "";
    if (init.retryAfterSeconds !== undefined) this.retryAfterSeconds = init.retryAfterSeconds;
  }
}

/** Builds an error from a non-2xx response, tolerating bodies that are not the Arcana envelope. */
export async function errorFromResponse(response: Response): Promise<ArcanaError> {
  let code = "internal";
  let message = `HTTP ${response.status}`;
  let requestId = "";
  try {
    const body = (await response.json()) as { error?: { code?: string; message?: string; request_id?: string } };
    if (body.error) {
      code = body.error.code ?? code;
      message = body.error.message ?? message;
      requestId = body.error.request_id ?? "";
    }
  } catch {
    // Keep the generic message: a proxy may have answered instead of Arcana.
  }
  const retry = Number.parseInt(response.headers.get("Retry-After") ?? "", 10);
  return new ArcanaError({
    status: response.status,
    code,
    message,
    requestId,
    ...(Number.isFinite(retry) && retry > 0 ? { retryAfterSeconds: retry } : {}),
  });
}
