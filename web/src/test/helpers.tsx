import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { AppRoutes } from "../App";
import type { EventSourceFactory, EventSourceLike } from "../api/live";

/** A controllable EventSource: tests push server events with emit(). */
export class FakeEventSource implements EventSourceLike {
  readyState = 1;
  onerror: ((event: Event) => void) | null = null;
  closed = false;
  private listeners = new Map<string, ((event: MessageEvent<string>) => void)[]>();
  constructor(readonly url: string) {}

  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  close(): void {
    this.closed = true;
    this.readyState = 2;
  }

  emit(type: string, data: unknown): void {
    const text = typeof data === "string" ? data : JSON.stringify(data);
    for (const listener of this.listeners.get(type) ?? [])
      listener(new MessageEvent(type, { data: text }));
  }

  fail(): void {
    this.readyState = 2;
    this.onerror?.(new Event("error"));
  }
}

export interface Sources {
  factory: EventSourceFactory;
  all: FakeEventSource[];
  /** The newest source opened for a URL fragment. */
  find(fragment: string): FakeEventSource | undefined;
}

export function fakeSources(): Sources {
  const all: FakeEventSource[] = [];
  return {
    all,
    factory: (url) => {
      const source = new FakeEventSource(url);
      all.push(source);
      return source;
    },
    find: (fragment) => [...all].reverse().find((s) => s.url.includes(fragment)),
  };
}

export function testClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 0, gcTime: Infinity } },
  });
}

export function renderApp(
  path: string,
  sources: Sources = fakeSources(),
  client: QueryClient = testClient(),
) {
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <AppRoutes eventSourceFactory={sources.factory} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { ...view, sources, client };
}

export function b64(text: string | Uint8Array): string {
  const bytes = typeof text === "string" ? new TextEncoder().encode(text) : text;
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}
