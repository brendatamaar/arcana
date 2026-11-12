/** An API failure in Arcana's error envelope. Messages come from the server and are shown as text. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;

  constructor(status: number, code: string, message: string, requestId = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
  }
}

interface Envelope {
  error?: { code?: string; message?: string; request_id?: string };
}

export function toApiError(status: number, body: unknown): ApiError {
  const envelope = (typeof body === "object" && body !== null ? body : {}) as Envelope;
  const e = envelope.error;
  return new ApiError(
    status,
    e?.code ?? "internal",
    e?.message ?? `HTTP ${status}`,
    e?.request_id ?? "",
  );
}
