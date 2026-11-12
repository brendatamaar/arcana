import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { server } from "../test/server";
import { HOSTILE, exec, expectInert, sandbox } from "../test/fixtures";
import { renderApp } from "../test/helpers";

const overview = (extra: object = {}) => ({
  version: "v0.3.0",
  runtime: "runsc",
  sandboxes_by_template: [
    { template: "python", count: 2 },
    { template: "base", count: 1 },
  ],
  pool: [{ template: "python", available: 2, target: 3 }],
  recent_terminations: [
    {
      id: "sbx_old",
      template: "base",
      state: "expired",
      reason: "ttl",
      ended_at: "2026-11-12T10:00:00Z",
    },
  ],
  ...extra,
});
const envelope = (status: number, code: string, message: string) =>
  HttpResponse.json({ error: { code, message, request_id: "req_1" } }, { status });

describe("Login", () => {
  it("exchanges the key for a session, clears it, stores nothing and opens the dashboard", async () => {
    let seen: { auth: string | null; method: string } | null = null;
    server.use(
      http.post("*/v1/sessions", ({ request }) => {
        seen = { auth: request.headers.get("Authorization"), method: request.method };
        return HttpResponse.json(
          { id: "ses_1", expires_at: "2026-11-13T00:00:00Z" },
          { status: 201 },
        );
      }),
      http.get("*/v1/overview", () => HttpResponse.json(overview())),
    );
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    renderApp("/login");
    const user = userEvent.setup();
    const button = screen.getByRole("button", { name: "Sign in" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    const input = screen.getByLabelText<HTMLInputElement>("API key");
    expect(input.type).toBe("password");
    await user.type(input, "arc_live_secret");
    await user.click(button);
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeTruthy();
    expect(seen).toEqual({ auth: "Bearer arc_live_secret", method: "POST" });
    expect(setItem).not.toHaveBeenCalled();
    expect(document.cookie).toBe("");
    expect(JSON.stringify(localStorage) + JSON.stringify(sessionStorage)).not.toContain(
      "arc_live_secret",
    );
    setItem.mockRestore();
  });

  it("rejects a wrong key without leaving the page and does not keep the typed key", async () => {
    server.use(
      http.post("*/v1/sessions", () => envelope(401, "unauthorized", "valid bearer key required")),
    );
    renderApp("/login");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API key"), "arc_live_wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect((await screen.findByRole("alert")).textContent).toBe("That API key was not accepted.");
    expect(screen.getByLabelText<HTMLInputElement>("API key").value).toBe("");
    expect(screen.queryByRole("heading", { name: "Overview" })).toBeNull();
  });

  it("reports an unreachable server", async () => {
    server.use(http.post("*/v1/sessions", () => HttpResponse.error()));
    renderApp("/login");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API key"), "k");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect((await screen.findByRole("alert")).textContent).toBe("Could not reach the server.");
  });

  it("shows server errors that are not authentication failures", async () => {
    server.use(
      http.post("*/v1/sessions", () =>
        envelope(403, "forbidden", "dashboard sessions are disabled"),
      ),
    );
    renderApp("/login");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("API key"), "k");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "forbidden: dashboard sessions are disabled",
    );
  });
});

describe("session handling", () => {
  it("sends the user to login when the server refuses a request for lack of a session", async () => {
    server.use(
      http.get("*/v1/overview", () => envelope(401, "unauthorized", "valid session required")),
    );
    renderApp("/");
    expect(await screen.findByLabelText("API key")).toBeTruthy();
  });

  it("logs out through the API, clears cached data and returns to login", async () => {
    let deleted = false;
    server.use(
      http.get("*/v1/overview", () => HttpResponse.json(overview())),
      http.delete("*/v1/sessions/current", () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/");
    const user = userEvent.setup();
    await screen.findByRole("heading", { name: "Overview" });
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(await screen.findByLabelText("API key")).toBeTruthy();
    expect(deleted).toBe(true);
  });
});

describe("Overview", () => {
  it("shows version, runtime, counts, pool fill and recent terminations with reasons", async () => {
    server.use(http.get("*/v1/overview", () => HttpResponse.json(overview())));
    renderApp("/");
    await screen.findByRole("heading", { name: "Overview" });
    expect(screen.getByText("v0.3.0")).toBeTruthy();
    expect(screen.getByText("runsc")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy(); // 2 + 1 active
    expect(screen.getByText("2 of 3 ready")).toBeTruthy();
    const row = screen.getByRole("link", { name: "sbx_old" }).closest("tr")!;
    expect(within(row).getByText("reached its time-to-live")).toBeTruthy();
    expect(screen.getByRole("link", { name: "sbx_old" }).getAttribute("href")).toBe(
      "/sandboxes/sbx_old",
    );
  });

  it("explains empty states and hides the pool section when there is no pool", async () => {
    server.use(
      http.get("*/v1/overview", () =>
        HttpResponse.json(
          overview({ sandboxes_by_template: [], pool: [], recent_terminations: [] }),
        ),
      ),
    );
    renderApp("/");
    expect(await screen.findByText("No active sandboxes.")).toBeTruthy();
    expect(screen.getByText("Nothing has ended yet.")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Warm pool" })).toBeNull();
  });

  it("shows API failures as text", async () => {
    server.use(
      http.get("*/v1/overview", () => envelope(500, "internal", "sandbox operation failed")),
    );
    renderApp("/");
    expect((await screen.findByRole("alert")).textContent).toBe(
      "internal: sandbox operation failed",
    );
  });

  it("updates live when a sandbox event arrives", async () => {
    let active = 1;
    server.use(
      http.get("*/v1/overview", () =>
        HttpResponse.json(
          overview({ sandboxes_by_template: [{ template: "base", count: active }] }),
        ),
      ),
    );
    const view = renderApp("/");
    const total = async () =>
      (await screen.findByText("Active sandboxes")).nextElementSibling?.textContent;
    expect(await total()).toBe("1");
    active = 4;
    act(() => {
      view.sources
        .find("/v1/events")
        ?.emit("sandbox.created", { sandbox_id: "sbx_new", data: { template: "base" } });
    });
    await waitFor(async () => expect(await total()).toBe("4"));
  });
});

describe("Sandboxes", () => {
  const page = (items: object[], next?: string) =>
    HttpResponse.json({ items, ...(next ? { next_cursor: next } : {}) });

  it("lists sandboxes with state, template, labels and links", async () => {
    server.use(
      http.get("*/v1/sandboxes", () =>
        page([
          sandbox({ id: "sbx_one", labels: { task: "fix-1", team: "a" } }),
          sandbox({ id: "sbx_two", state: "failed", template: "node", labels: {} }),
        ]),
      ),
    );
    renderApp("/sandboxes");
    const link = await screen.findByRole("link", { name: "sbx_one" });
    expect(link.getAttribute("href")).toBe("/sandboxes/sbx_one");
    const row = link.closest("tr")!;
    expect(within(row).getByText("running")).toBeTruthy();
    expect(within(row).getByText("task:fix-1 team:a")).toBeTruthy();
    expect(
      within(screen.getByRole("link", { name: "sbx_two" }).closest("tr")!).getByText("failed"),
    ).toBeTruthy();
  });

  it("passes state and label filters to the server and template filters client-side", async () => {
    const queries: string[] = [];
    server.use(
      http.get("*/v1/sandboxes", ({ request }) => {
        queries.push(new URL(request.url).search);
        return page([
          sandbox({ id: "sbx_py", template: "python" }),
          sandbox({ id: "sbx_js", template: "node" }),
        ]);
      }),
    );
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await screen.findByRole("link", { name: "sbx_py" });
    await user.selectOptions(screen.getByLabelText("State"), "running");
    await waitFor(() => expect(queries.some((q) => q.includes("state=running"))).toBe(true));
    await user.type(screen.getByLabelText("Label (key:value)"), "task:x");
    await waitFor(() => expect(queries.some((q) => q.includes("label=task%3Ax"))).toBe(true));
    await user.selectOptions(screen.getByLabelText("Template"), "node");
    await waitFor(() => expect(screen.queryByRole("link", { name: "sbx_py" })).toBeNull());
    expect(screen.getByRole("link", { name: "sbx_js" })).toBeTruthy();
  });

  it("loads further pages with the server's cursor", async () => {
    const cursors: (string | null)[] = [];
    server.use(
      http.get("*/v1/sandboxes", ({ request }) => {
        const cursor = new URL(request.url).searchParams.get("cursor");
        cursors.push(cursor);
        return cursor
          ? page([sandbox({ id: "sbx_second" })])
          : page([sandbox({ id: "sbx_first" })], "sbx_first");
      }),
    );
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await screen.findByRole("link", { name: "sbx_first" });
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByRole("link", { name: "sbx_second" })).toBeTruthy();
    expect(cursors).toEqual([null, "sbx_first"]);
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("says when nothing matches", async () => {
    server.use(http.get("*/v1/sandboxes", () => page([])));
    renderApp("/sandboxes");
    expect(await screen.findByText("No sandboxes match.")).toBeTruthy();
  });

  it("patches a row in place when a state event arrives", async () => {
    // The server reflects the change, so the refetch that follows an event agrees with the patch.
    let state: "running" | "destroying" = "running";
    server.use(http.get("*/v1/sandboxes", () => page([sandbox({ id: "sbx_live", state })])));
    const view = renderApp("/sandboxes");
    const row = (await screen.findByRole("link", { name: "sbx_live" })).closest("tr")!;
    expect(within(row).getByText("running")).toBeTruthy();
    state = "destroying";
    act(() => {
      view.sources.find("/v1/events")?.emit("sandbox.state", {
        sandbox_id: "sbx_live",
        data: { state: "destroying", reason: "requested" },
      });
    });
    await waitFor(() =>
      expect(
        within(screen.getByRole("link", { name: "sbx_live" }).closest("tr")!).getByText(
          "destroying",
        ),
      ).toBeTruthy(),
    );
  });
});

function detailHandlers(
  sb = sandbox(),
  execs = [exec()],
  history = [
    { state: "creating", at: "2026-11-12T10:00:00Z" },
    { state: "running", at: "2026-11-12T10:00:02Z" },
  ],
) {
  server.use(
    http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sb)),
    http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: history })),
    http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: execs })),
  );
}

describe("SandboxDetail", () => {
  it("shows the spec, live countdowns, state history and command history", async () => {
    detailHandlers(sandbox({ network: { mode: "allowlist", allow: ["pypi.org", "github.com"] } }), [
      exec({ shell: "make test", cmd: null }),
      exec({ id: "exe_two", cmd: ["ls", "-l"], exit_code: 0 }),
    ]);
    renderApp("/sandboxes/sbx_a1b2c3d4e5f6g7h8i9j0");
    await screen.findByRole("heading", { name: "sbx_a1b2c3d4e5f6g7h8i9j0" });
    expect(screen.getByText("allowlist")).toBeTruthy();
    expect(screen.getByText("pypi.org, github.com")).toBeTruthy();
    expect(screen.getByText("1024 MB")).toBeTruthy();
    expect(screen.getByText(/Time to live: \d+m \d+s/)).toBeTruthy();
    expect(screen.getByText(/Idle timeout: \d+m \d+s/)).toBeTruthy();
    const history = within(
      screen.getByRole("heading", { name: "State history" }).closest("section")!,
    );
    expect(await history.findByText("creating")).toBeTruthy();
    expect(history.getByText("running")).toBeTruthy();
    const commands = within(screen.getByRole("table", { name: "Command history, newest first" }));
    expect(commands.getByText("make test")).toBeTruthy();
    expect(commands.getByText("ls -l")).toBeTruthy();
    expect(commands.getByText("exit 0")).toBeTruthy();
  });

  it("opens the newest command's live output and switches when another is chosen", async () => {
    detailHandlers(sandbox(), [
      exec({ id: "exe_new", ended_at: null, exit_code: null }),
      exec({ id: "exe_old" }),
    ]);
    const view = renderApp("/sandboxes/sbx_a1b2c3d4e5f6g7h8i9j0");
    const user = userEvent.setup();
    await screen.findByRole("log");
    expect(view.sources.all.filter((s) => s.url.includes("/output")).map((s) => s.url)).toEqual([
      "/v1/sandboxes/sbx_a1b2c3d4e5f6g7h8i9j0/execs/exe_new/output",
    ]);
    const newest = screen.getAllByRole("button", { name: "View output" })[0]!.closest("tr")!;
    expect(within(newest).getByText("running")).toBeTruthy();
    await user.click(screen.getAllByRole("button", { name: "View output" })[1]!);
    await waitFor(() => expect(view.sources.find("exe_old")).toBeTruthy());
    expect(view.sources.find("exe_new")?.closed).toBe(true); // switching closes the previous tail
  });

  it("shows an ended sandbox's reason instead of countdowns", async () => {
    detailHandlers(sandbox({ state: "expired", end_reason: "idle" }), []);
    renderApp("/sandboxes/sbx_a1b2c3d4e5f6g7h8i9j0");
    expect(await screen.findByText("Ended: idle timeout")).toBeTruthy();
    expect(screen.queryByText(/Time to live/)).toBeNull();
    expect(screen.getByText("No commands have run.")).toBeTruthy();
    expect(screen.queryByRole("log")).toBeNull();
  });

  it("says when a sandbox does not exist or is not visible", async () => {
    server.use(
      http.get("*/v1/sandboxes/:id", () => envelope(404, "sandbox_not_found", "sandbox not found")),
      http.get("*/v1/sandboxes/:id/history", () =>
        envelope(404, "sandbox_not_found", "sandbox not found"),
      ),
      http.get("*/v1/sandboxes/:id/execs", () =>
        envelope(404, "sandbox_not_found", "sandbox not found"),
      ),
    );
    renderApp("/sandboxes/sbx_missing");
    expect((await screen.findByRole("alert")).textContent).toBe(
      "sandbox_not_found: sandbox not found",
    );
  });
});

// ESC-24: everything a sandbox (or a caller of the API) controls is shown as inert text in every view.
describe("ESC-24: sandbox-controlled data renders as text", () => {
  it("in the sandbox list (id, template, labels)", async () => {
    server.use(
      http.get("*/v1/sandboxes", () =>
        HttpResponse.json({
          items: [
            sandbox({ id: `sbx_${HOSTILE}`, template: HOSTILE, labels: { [HOSTILE]: HOSTILE } }),
          ],
        }),
      ),
    );
    const view = renderApp("/sandboxes");
    await screen.findByRole("table");
    expectInert(view.container);
    expect(view.container.textContent).toContain("<script>window.__pwned = true</script>");
    expect(view.container.innerHTML).toContain("&lt;script&gt;");
  });

  it("in the overview (template names, termination reasons and ids)", async () => {
    server.use(
      http.get("*/v1/overview", () =>
        HttpResponse.json(
          overview({
            version: HOSTILE,
            runtime: HOSTILE,
            sandboxes_by_template: [{ template: HOSTILE, count: 1 }],
            pool: [{ template: HOSTILE, available: 1, target: 2 }],
            recent_terminations: [
              {
                id: `sbx_${HOSTILE}`,
                template: HOSTILE,
                state: "failed",
                reason: HOSTILE,
                ended_at: "2026-11-12T10:00:00Z",
              },
            ],
          }),
        ),
      ),
    );
    const view = renderApp("/");
    await screen.findByRole("table");
    expectInert(view.container);
    expect(view.container.textContent).toContain("<img src=x");
  });

  it("on the detail page (spec, labels, allowed hosts, history reasons, command text) and in an error message", async () => {
    detailHandlers(
      sandbox({
        template: HOSTILE,
        labels: { [HOSTILE]: HOSTILE },
        network: { mode: "allowlist", allow: [HOSTILE] },
        end_reason: HOSTILE,
        state: "failed",
      }),
      [exec({ cmd: [HOSTILE, HOSTILE] }), exec({ id: "exe_shell", shell: HOSTILE, cmd: null })],
      [{ state: "failed", at: "2026-11-12T10:00:00Z", reason: HOSTILE } as never],
    );
    const view = renderApp("/sandboxes/sbx_a1b2c3d4e5f6g7h8i9j0");
    await screen.findByRole("table", { name: "Command history, newest first" });
    expectInert(view.container);
    expect(view.container.textContent).toContain('<img src=x onerror="window.__pwned=true">');
  });

  it("when the server's own error message contains markup", async () => {
    server.use(http.get("*/v1/overview", () => envelope(500, "internal", HOSTILE)));
    const view = renderApp("/");
    await screen.findByRole("alert");
    expectInert(view.container);
    expect(screen.getByRole("alert").textContent).toContain("<script>");
  });
});
