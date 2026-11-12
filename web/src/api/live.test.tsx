import { renderHook } from "@testing-library/react";
import { QueryClientProvider, type InfiniteData } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { applyEvent, patchSandbox, useLiveEvents } from "./live";
import { fakeSources, testClient } from "../test/helpers";
import { sandbox } from "../test/fixtures";
import type { components } from "./schema";

type SandboxList = components["schemas"]["SandboxList"];

describe("applyEvent", () => {
  it("patches a cached sandbox in lists and on its detail page without refetching those rows", () => {
    const client = testClient();
    const a = sandbox({ id: "sbx_a" });
    const b = sandbox({ id: "sbx_b" });
    client.setQueryData<InfiniteData<SandboxList>>(["sandboxes", "", ""], {
      pages: [{ items: [a, b] }],
      pageParams: [""],
    });
    client.setQueryData(["sandbox", "sbx_a"], a);
    applyEvent(client, "sandbox.state", {
      sandbox_id: "sbx_a",
      data: { state: "destroying", reason: "ttl" },
    });
    const list = client.getQueryData<InfiniteData<SandboxList>>(["sandboxes", "", ""]);
    expect(list?.pages[0]?.items.map((s) => s.state)).toEqual(["destroying", "running"]);
    expect(list?.pages[0]?.items[0]?.end_reason).toBe("ttl");
    expect(client.getQueryData<typeof a>(["sandbox", "sbx_a"])?.state).toBe("destroying");
  });

  it("invalidates what an event may add, remove or recount, and nothing else", () => {
    const client = testClient();
    const stale = (key: unknown[]) => client.getQueryState(key)?.isInvalidated ?? false;
    for (const key of [
      ["sandboxes", "", ""],
      ["overview"],
      ["execs", "sbx_a"],
      ["execs", "sbx_other"],
      ["history", "sbx_a"],
    ])
      client.setQueryData(key, {});
    applyEvent(client, "exec.started", { sandbox_id: "sbx_a" });
    expect(stale(["execs", "sbx_a"])).toBe(true);
    expect(stale(["execs", "sbx_other"])).toBe(false);
    expect(stale(["overview"])).toBe(false);
    applyEvent(client, "sandbox.created", { sandbox_id: "sbx_z" });
    expect(stale(["sandboxes", "", ""])).toBe(true);
    expect(stale(["overview"])).toBe(true);
    applyEvent(client, "pool.changed", { data: { template: "base" } });
    expect(stale(["history", "sbx_a"])).toBe(false);
  });

  it("leaves a sandbox untouched when the event carries no usable state", () => {
    const original = sandbox();
    expect(patchSandbox(original, { data: {} })).toBe(original);
    expect(patchSandbox(original, { data: { state: 5 } })).toBe(original);
  });
});

describe("useLiveEvents", () => {
  function setup() {
    const sources = fakeSources();
    const client = testClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const view = renderHook(
      () => {
        useLiveEvents(sources.factory);
      },
      { wrapper },
    );
    return { sources, client, view };
  }

  it("subscribes to /v1/events and applies incoming events to the cache", () => {
    const { sources, client } = setup();
    expect(sources.all).toHaveLength(1);
    expect(sources.all[0]?.url).toBe("/v1/events");
    client.setQueryData(["sandbox", "sbx_a"], sandbox({ id: "sbx_a" }));
    sources.all[0]?.emit("sandbox.state", {
      sandbox_id: "sbx_a",
      data: { state: "failed", reason: "lost" },
    });
    expect(client.getQueryData<ReturnType<typeof sandbox>>(["sandbox", "sbx_a"])?.state).toBe(
      "failed",
    );
  });

  it("ignores malformed events instead of crashing", () => {
    const { sources, client } = setup();
    client.setQueryData(["sandbox", "sbx_a"], sandbox({ id: "sbx_a" }));
    sources.all[0]?.emit("sandbox.state", "not json");
    sources.all[0]?.emit("sandbox.state", "[1,2]");
    expect(client.getQueryData<ReturnType<typeof sandbox>>(["sandbox", "sbx_a"])?.state).toBe(
      "running",
    );
  });

  it("on reset closes the stream, reopens it without a resume point and refetches everything", () => {
    const { sources, client } = setup();
    client.setQueryData(["overview"], {});
    sources.all[0]?.emit("reset", {});
    expect(sources.all[0]?.closed).toBe(true);
    expect(sources.all).toHaveLength(2);
    expect(client.getQueryState(["overview"])?.isInvalidated).toBe(true);
  });

  it("refetches when the stream is closed for good, for example after a 401", () => {
    const { sources, client } = setup();
    client.setQueryData(["overview"], {});
    sources.all[0]?.fail();
    expect(client.getQueryState(["overview"])?.isInvalidated).toBe(true);
  });

  it("closes the stream when the view unmounts", () => {
    const { sources, view } = setup();
    view.unmount();
    expect(sources.all[0]?.closed).toBe(true);
    expect(sources.all).toHaveLength(1);
  });
});

describe("useLiveEvents with the production EventSource", () => {
  it("subscribes once and survives re-renders", () => {
    const created: string[] = [];
    class CountingEventSource {
      readyState = 1;
      onerror: ((event: Event) => void) | null = null;
      constructor(readonly url: string) {
        created.push(url);
      }
      addEventListener() {}
      close() {}
    }
    vi.stubGlobal("EventSource", CountingEventSource);
    try {
      const client = testClient();
      const wrapper = ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      );
      const view = renderHook(
        () => {
          useLiveEvents();
        },
        { wrapper },
      );
      for (let i = 0; i < 5; i++) view.rerender();
      expect(created).toEqual(["/v1/events"]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
