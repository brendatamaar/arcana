import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { LogViewer } from "./LogViewer";
import { b64, fakeSources } from "../test/helpers";
import { HOSTILE, expectInert } from "../test/fixtures";

function open() {
  const sources = fakeSources();
  const view = render(<LogViewer sandboxId="sbx_a" execId="exe_1" factory={sources.factory} />);
  const es = sources.all[0]!;
  const out = (stream: "stdout" | "stderr", text: string) => {
    act(() => {
      es.emit("output", { seq: 1, stream, data: b64(text) });
    });
  };
  return { sources, view, es, out };
}

describe("LogViewer", () => {
  it("tails the output stream of the right exec", () => {
    const { es } = open();
    expect(es.url).toBe("/v1/sandboxes/sbx_a/execs/exe_1/output");
    expect(screen.getByRole("status").textContent).toBe("Connecting…");
  });

  it("shows lines as they arrive, keeps order across streams and reports the result", async () => {
    const { es, out } = open();
    out("stdout", "collected 2 items\n");
    out("stderr", "warning: slow\n");
    out("stdout", "2 passed");
    await waitFor(() => expect(screen.getByRole("log").textContent).toContain("2 passed"));
    const rows = [...screen.getByRole("log").querySelectorAll("[data-stream]")];
    expect(rows.map((r) => [r.getAttribute("data-stream"), r.textContent])).toEqual([
      ["stdout", "collected 2 items"],
      ["stderr", "warning: slow"],
      ["stdout", "2 passed"],
    ]);
    act(() => {
      es.emit("end", {
        exit_code: 0,
        timed_out: false,
        truncated: false,
        cancelled: false,
        expired: false,
      });
    });
    expect(screen.getByRole("status").textContent).toBe("Exited with status 0.");
    expect(es.closed).toBe(true); // otherwise EventSource would reconnect and replay the whole tail
  });

  it("renders ANSI colours as styled spans using a fixed class set", async () => {
    const { out } = open();
    out("stdout", "\u001b[31mFAILED\u001b[0m \u001b[1;32mok\u001b[0m\n");
    await waitFor(() => expect(screen.getByText("FAILED")).toBeTruthy());
    expect(screen.getByText("FAILED").className).toContain("text-red-400");
    expect(screen.getByText("ok").className).toContain("font-bold");
    expect(screen.getByRole("log").textContent).not.toContain("\u001b");
  });

  it("says when the exit status is unknown, timed out, or the output is gone", () => {
    const { es } = open();
    act(() => {
      es.emit("end", {
        exit_code: null,
        timed_out: true,
        truncated: true,
        cancelled: false,
        expired: true,
      });
    });
    expect(screen.getByRole("status").textContent).toBe(
      "Output is no longer retained. Exit status unknown, timed out, output truncated by the server.",
    );
  });

  it("warns when older output was discarded and when the stream breaks", () => {
    const { es } = open();
    act(() => {
      es.emit("gap", {});
    });
    expect(screen.getByText(/Older output was discarded/)).toBeTruthy();
    act(() => {
      es.fail();
    });
    expect(screen.getByRole("status").textContent).toBe("The output stream was interrupted.");
  });

  it("closes the stream on unmount", () => {
    const { view, es } = open();
    view.unmount();
    expect(es.closed).toBe(true);
  });

  it("pauses following when the user scrolls up and resumes on request", async () => {
    const { out } = open();
    out("stdout", "line\n".repeat(50));
    const log = screen.getByRole("log");
    expect(screen.queryByRole("button", { name: "Jump to latest" })).toBeNull();
    for (const [name, value] of [
      ["scrollHeight", 1000],
      ["clientHeight", 320],
      ["scrollTop", 100],
    ] as const) {
      Object.defineProperty(log, name, { configurable: true, value });
    }
    fireEvent.scroll(log);
    const jump = await screen.findByRole("button", { name: "Jump to latest" });
    fireEvent.click(jump);
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Jump to latest" })).toBeNull(),
    );
  });

  // ESC-24: output containing markup and terminal-escape tricks is shown as inert text.
  it("ESC-24: renders HTML and escape-sequence tricks as plain text", async () => {
    const { view, out } = open();
    out("stdout", `${HOSTILE}\n`);
    out(
      "stderr",
      "\u001b]0;pwned title\u0007\u001b[2J\u001b]8;;javascript:alert(1)\u0007link text\u001b]8;;\u0007\n",
    );
    out("stdout", "\u001b[31m<b>bold?</b>\u001b[0m\n");
    await waitFor(() => expect(screen.getByRole("log").textContent).toContain("link text"));
    expectInert(view.container);
    const text = screen.getByRole("log").textContent;
    expect(text).toContain("<script>window.__pwned = true</script>");
    expect(text).toContain('<img src=x onerror="window.__pwned=true">');
    expect(text).toContain("<b>bold?</b>");
    expect(text).not.toContain("pwned title"); // window-title sequences are dropped, not displayed
    expect(view.container.innerHTML).toContain("&lt;script&gt;");
    expect(view.container.querySelector("b")).toBeNull();
    expect(document.title).not.toContain("pwned");
  });
});

describe("production EventSource wiring", () => {
  it("opens one stream per exec and keeps it across re-renders instead of reconnecting in a loop", async () => {
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
      const view = render(<LogViewer sandboxId="sbx_a" execId="exe_1" />); // no factory: the real default
      for (let i = 0; i < 5; i++) view.rerender(<LogViewer sandboxId="sbx_a" execId="exe_1" />);
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });
      expect(created).toEqual(["/v1/sandboxes/sbx_a/execs/exe_1/output"]);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
