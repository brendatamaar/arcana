import { useEffect, useRef, useState } from "react";
import { LineBuffer } from "../lib/lines";
import { browserEventSource, type EventSourceFactory } from "./live";

export interface OutputEnd {
  exit_code: number | null;
  timed_out: boolean;
  truncated: boolean;
  cancelled: boolean;
  expired: boolean;
}

export type OutputStatus = "connecting" | "streaming" | "ended" | "error";

export interface OutputState {
  buffer: LineBuffer;
  /** Changes whenever the buffer does; use it to re-render. */
  version: number;
  status: OutputStatus;
  end: OutputEnd | null;
  /** True when the server discarded older output (its ring wrapped, or this reader was late). */
  gap: boolean;
}

function decodeBase64(data: string): Uint8Array {
  const binary = atob(data);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

/**
 * Follows an exec's output tail. Bytes are decoded and parsed into display lines as they arrive;
 * renders are batched to one per animation frame so a fast command cannot flood React.
 */
export function useExecOutput(
  sandboxId: string,
  execId: string,
  factory: EventSourceFactory = browserEventSource,
): OutputState {
  const bufferRef = useRef<LineBuffer>(new LineBuffer());
  const [version, setVersion] = useState(0);
  const [status, setStatus] = useState<OutputStatus>("connecting");
  const [end, setEnd] = useState<OutputEnd | null>(null);
  const [gap, setGap] = useState(false);

  useEffect(() => {
    const buffer = new LineBuffer();
    bufferRef.current = buffer;
    setVersion(0);
    setStatus("connecting");
    setEnd(null);
    setGap(false);
    let scheduled = false;
    let finished = false;
    const flush = () => {
      scheduled = false;
      setVersion(buffer.version);
    };
    const schedule = () => {
      if (scheduled) return;
      scheduled = true;
      if (typeof requestAnimationFrame === "function") requestAnimationFrame(flush);
      else setTimeout(flush, 16);
    };
    const es = factory(
      `/v1/sandboxes/${encodeURIComponent(sandboxId)}/execs/${encodeURIComponent(execId)}/output`,
    );
    es.addEventListener("output", (message) => {
      try {
        const payload = JSON.parse(message.data) as { stream?: string; data?: string };
        if (typeof payload.data !== "string") return;
        buffer.appendBytes(
          payload.stream === "stderr" ? "stderr" : "stdout",
          decodeBase64(payload.data),
        );
        setStatus("streaming");
        schedule();
      } catch {
        // A malformed frame is dropped; the stream continues.
      }
    });
    es.addEventListener("gap", () => setGap(true));
    es.addEventListener("end", (message) => {
      finished = true;
      es.close(); // otherwise EventSource would reconnect and replay everything
      try {
        setEnd(JSON.parse(message.data) as OutputEnd);
      } catch {
        setEnd({
          exit_code: null,
          timed_out: false,
          truncated: false,
          cancelled: false,
          expired: false,
        });
      }
      setStatus("ended");
      flush();
    });
    es.onerror = () => {
      if (!finished && es.readyState === 2) setStatus("error");
    };
    return () => {
      finished = true;
      es.close();
    };
  }, [sandboxId, execId, factory]);

  return { buffer: bufferRef.current, version, status, end, gap };
}
