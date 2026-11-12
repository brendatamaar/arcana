import { useEffect } from "react";
import { type InfiniteData, type QueryClient, useQueryClient } from "@tanstack/react-query";
import type { components } from "./schema";

type SandboxList = components["schemas"]["SandboxList"];
type Sandbox = components["schemas"]["Sandbox"];

/** The subset of EventSource the dashboard uses, so tests can supply a controllable fake. */
export interface EventSourceLike {
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void;
  close(): void;
  readonly readyState: number;
  onerror: ((event: Event) => void) | null;
}
export type EventSourceFactory = (url: string) => EventSourceLike;

/**
 * The production factory. It must be one stable function: effects depend on their factory, and a
 * fresh arrow per render would tear down and reopen the stream on every render.
 */
export const browserEventSource: EventSourceFactory = (url) => new EventSource(url);

const EVENT_TYPES = [
  "sandbox.created",
  "sandbox.state",
  "sandbox.destroyed",
  "exec.started",
  "exec.ended",
  "pool.changed",
] as const;

interface WireEvent {
  sandbox_id?: string;
  data?: Record<string, unknown>;
}

function parse(raw: string): WireEvent | null {
  try {
    const value: unknown = JSON.parse(raw);
    return typeof value === "object" && value !== null ? value : null;
  } catch {
    return null;
  }
}

/** Patches a cached sandbox with the state an event reports, leaving every other field alone. */
export function patchSandbox(sandbox: Sandbox, event: WireEvent): Sandbox {
  const state = event.data?.state;
  const reason = event.data?.reason;
  if (typeof state !== "string") return sandbox;
  // The server only sends known states; the badge renders an unknown one as a plain label.
  return {
    ...sandbox,
    state: state as Sandbox["state"],
    ...(typeof reason === "string" && reason !== "" ? { end_reason: reason } : {}),
  };
}

/**
 * Applies one event to the query cache. State changes patch cached sandboxes in place, so lists and
 * detail pages update without a request; everything that may add or remove rows, or change counts,
 * is invalidated and refetched.
 */
export function applyEvent(client: QueryClient, type: string, event: WireEvent): void {
  const id = event.sandbox_id;
  if (type === "sandbox.state" && id) {
    client.setQueriesData<InfiniteData<SandboxList>>(
      { queryKey: ["sandboxes"] },
      (cached) =>
        cached && {
          ...cached,
          pages: cached.pages.map((page) => ({
            ...page,
            items: page.items.map((s) => (s.id === id ? patchSandbox(s, event) : s)),
          })),
        },
    );
    client.setQueryData<Sandbox>(
      ["sandbox", id],
      (cached) => cached && patchSandbox(cached, event),
    );
    void client.invalidateQueries({ queryKey: ["history", id] });
  }
  if (type === "sandbox.created" || type === "sandbox.destroyed" || type === "sandbox.state") {
    void client.invalidateQueries({ queryKey: ["sandboxes"] });
    void client.invalidateQueries({ queryKey: ["overview"] });
  }
  if (type === "pool.changed") void client.invalidateQueries({ queryKey: ["overview"] });
  if ((type === "exec.started" || type === "exec.ended") && id)
    void client.invalidateQueries({ queryKey: ["execs", id] });
}

/** Refetches everything after the server says its event history cannot be resumed. */
export function resync(client: QueryClient): void {
  void client.invalidateQueries();
}

const CLOSED = 2;

/**
 * Keeps the query cache live from /v1/events. EventSource reconnects by itself and resumes with
 * Last-Event-ID; on a `reset` the stream is reopened from scratch and every query refetched.
 * A permanently closed stream (for example after a 401) is left to the normal request path, which
 * sends the user back to login.
 */
export function useLiveEvents(factory: EventSourceFactory = browserEventSource): void {
  const client = useQueryClient();
  useEffect(() => {
    let source: EventSourceLike | null = null;
    let stopped = false;
    const open = () => {
      const es = factory("/v1/events");
      source = es;
      for (const type of EVENT_TYPES) {
        es.addEventListener(type, (message) => {
          const event = parse(message.data);
          if (event) applyEvent(client, type, event);
        });
      }
      es.addEventListener("reset", () => {
        es.close();
        resync(client);
        if (!stopped) open();
      });
      es.onerror = () => {
        if (es.readyState === CLOSED) resync(client);
      };
    };
    open();
    return () => {
      stopped = true;
      source?.close();
    };
  }, [client, factory]);
}
