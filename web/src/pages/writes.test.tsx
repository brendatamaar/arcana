import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it, vi } from "vitest";
import { server, sessionBody } from "../test/server";
import {
  HOSTILE,
  auditEvent,
  exec,
  expectInert,
  key,
  sandbox,
  snapshot,
  template,
} from "../test/fixtures";
import { fakeSources, renderApp } from "../test/helpers";

const envelope = (status: number, code: string, message: string) =>
  HttpResponse.json({ error: { code, message, request_id: "req_1" } }, { status });
const SECRET = "arc_live_THIS_SECRET_IS_SHOWN_ONCE_0123456789";

const overview = () => ({
  version: "v0.4.0",
  runtime: "runsc",
  sandboxes_by_template: [],
  pool: [],
  recent_terminations: [],
});

describe("navigation", () => {
  it("shows Keys and Audit only to admin sessions", async () => {
    server.use(http.get("*/v1/overview", () => HttpResponse.json(overview())));
    const view = renderApp("/");
    await screen.findByRole("heading", { name: "Overview" });
    expect(screen.getByRole("link", { name: "Snapshots" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Keys" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Audit" })).toBeNull();
    view.unmount();

    server.use(
      http.get("*/v1/sessions/current", () => HttpResponse.json(sessionBody({ admin: true }))),
    );
    renderApp("/");
    expect(await screen.findByRole("link", { name: "Keys" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Audit" })).toBeTruthy();
  });
});

describe("CSRF protection of writes", () => {
  it("sends the session's token on writes, fetched from the session and never stored", async () => {
    let seen: { csrf: string | null; reauth: string | null; method: string } | null = null;
    server.use(
      http.get("*/v1/sessions/current", () =>
        HttpResponse.json(sessionBody({ csrf_token: "token-from-the-server" })),
      ),
      http.get("*/v1/templates", () => HttpResponse.json({ items: [template()] })),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sandbox())),
      http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: [] })),
      http.post("*/v1/sandboxes", ({ request }) => {
        seen = {
          csrf: request.headers.get("X-CSRF-Token"),
          reauth: request.headers.get("X-Arcana-Reauth"),
          method: request.method,
        };
        return HttpResponse.json(sandbox(), { status: 201 });
      }),
    );
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Create sandbox" }));
    await user.click(await screen.findByRole("button", { name: "Create" }));
    await screen.findByRole("heading", { name: sandbox().id });
    expect(seen).toEqual({ csrf: "token-from-the-server", reauth: null, method: "POST" });
    expect(setItem).not.toHaveBeenCalled();
    expect(JSON.stringify(localStorage) + JSON.stringify(sessionStorage)).not.toContain(
      "token-from-the-server",
    );
    expect(document.cookie).toBe("");
    setItem.mockRestore();
  });

  it("does not send the token on reads", async () => {
    let csrf: string | null = "unset";
    server.use(
      http.get("*/v1/sandboxes", ({ request }) => {
        csrf = request.headers.get("X-CSRF-Token");
        return HttpResponse.json({ items: [] });
      }),
    );
    renderApp("/sandboxes");
    await screen.findByText("No sandboxes match.");
    expect(csrf).toBeNull();
  });

  it("shows the server's refusal when a write is rejected for its token", async () => {
    server.use(
      http.get("*/v1/templates", () => HttpResponse.json({ items: [template()] })),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
      http.post("*/v1/sandboxes", () =>
        envelope(403, "forbidden", "missing or invalid CSRF token"),
      ),
    );
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Create sandbox" }));
    await user.click(await screen.findByRole("button", { name: "Create" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "forbidden: missing or invalid CSRF token",
    );
  });
});

describe("Sandboxes: create", () => {
  it("creates from a template with an optional lifetime and opens the new sandbox", async () => {
    let body: unknown;
    server.use(
      http.get("*/v1/templates", () =>
        HttpResponse.json({ items: [template({ name: "base" }), template({ name: "node" })] }),
      ),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
      http.post("*/v1/sandboxes", async ({ request }) => {
        body = await request.json();
        return HttpResponse.json(sandbox({ id: "sbx_new", template: "node" }), { status: 201 });
      }),
      http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sandbox({ id: "sbx_new" }))),
      http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: [] })),
    );
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Create sandbox" }));
    const form = await screen.findByRole("form", { name: "Create sandbox" });
    await user.selectOptions(within(form).getByLabelText("Template"), "node");
    await user.type(within(form).getByLabelText(/Time to live/), "15");
    await user.click(screen.getByRole("button", { name: "Create" }));
    await screen.findByRole("heading", { name: "sbx_new" });
    expect(body).toEqual({ template: "node", ttl_seconds: 900 });
  });

  it("rejects an unusable lifetime before sending anything", async () => {
    server.use(
      http.get("*/v1/templates", () => HttpResponse.json({ items: [template()] })),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
    );
    renderApp("/sandboxes");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Create sandbox" }));
    await user.type(await screen.findByLabelText(/Time to live/), "soon");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Create" }).disabled).toBe(true);
  });

  it("renders hostile template names as inert text", async () => {
    server.use(
      http.get("*/v1/templates", () => HttpResponse.json({ items: [template({ name: HOSTILE })] })),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
    );
    const view = renderApp("/sandboxes");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Create sandbox" }));
    await screen.findByRole("option", { name: HOSTILE });
    expectInert(view.container);
  });
});

describe("Sandbox detail: actions", () => {
  const detail = () => [
    http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sandbox())),
    http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
    http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: [] })),
  ];

  it("keeps a sandbox alive and snapshots its workspace", async () => {
    const calls: string[] = [];
    server.use(
      ...detail(),
      http.post("*/v1/sandboxes/:id/keepalive", () => {
        calls.push("keepalive");
        return new HttpResponse(null, { status: 200 });
      }),
      http.post("*/v1/sandboxes/:id/snapshots", () => {
        calls.push("snapshot");
        return HttpResponse.json(snapshot(), { status: 201 });
      }),
    );
    renderApp(`/sandboxes/${sandbox().id}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Keep alive" }));
    expect(await screen.findByText("Idle timer reset.")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Snapshot workspace" }));
    expect(await screen.findByText(/Snapshot saved\./)).toBeTruthy();
    expect(screen.getByRole("link", { name: "View snapshots" })).toBeTruthy();
    expect(calls).toEqual(["keepalive", "snapshot"]);
  });

  it("destroys only after a second, explicit confirmation and returns to the list", async () => {
    let destroyed = 0;
    server.use(
      ...detail(),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
      http.delete("*/v1/sandboxes/:id", () => {
        destroyed++;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(`/sandboxes/${sandbox().id}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Destroy" }));
    expect(destroyed).toBe(0);
    const group = screen.getByRole("group", { name: /Destroy this sandbox/ });
    await user.click(within(group).getByRole("button", { name: "Cancel" }));
    expect(destroyed).toBe(0);
    await user.click(screen.getByRole("button", { name: "Destroy" }));
    await user.click(screen.getByRole("button", { name: "Destroy sandbox" }));
    await screen.findByRole("heading", { name: "Sandboxes" });
    expect(destroyed).toBe(1);
  });

  it("still returns to the list when the sandbox turns to destroyed before the response is handled", async () => {
    // The live stream and the refetch show the end state, which removes the buttons, while the
    // destroy request is still being resolved.
    let deleted = false;
    server.use(
      http.get("*/v1/sandboxes/:id", () =>
        HttpResponse.json(sandbox({ state: deleted ? "destroyed" : "running" })),
      ),
      http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes", () => HttpResponse.json({ items: [] })),
      http.delete("*/v1/sandboxes/:id", () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(`/sandboxes/${sandbox().id}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Destroy" }));
    await user.click(screen.getByRole("button", { name: "Destroy sandbox" }));
    await screen.findByRole("heading", { name: "Sandboxes" });
  });

  it("offers no actions for a sandbox that has ended", async () => {
    server.use(
      http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sandbox({ state: "destroyed" }))),
      http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id/execs", () => HttpResponse.json({ items: [] })),
    );
    renderApp(`/sandboxes/${sandbox().id}`);
    await screen.findByRole("heading", { name: sandbox().id });
    expect(screen.queryByRole("button", { name: "Destroy" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Keep alive" })).toBeNull();
  });

  it("shows the server's error for a failed action", async () => {
    server.use(
      ...detail(),
      http.post("*/v1/sandboxes/:id/snapshots", () =>
        envelope(429, "quota_exceeded", "snapshot limit reached"),
      ),
    );
    renderApp(`/sandboxes/${sandbox().id}`);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Snapshot workspace" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "quota_exceeded: snapshot limit reached",
    );
  });
});

describe("Snapshots", () => {
  it("lists snapshots and renders hostile data as inert text (ESC-24)", async () => {
    server.use(
      http.get("*/v1/snapshots", () =>
        HttpResponse.json({
          items: [snapshot({ template: HOSTILE, source_sandbox_id: HOSTILE, id: "snp_hostile" })],
        }),
      ),
    );
    const view = renderApp("/snapshots");
    expect(await screen.findByText("snp_hostile")).toBeTruthy();
    expect(screen.getByText("17.6 MB")).toBeTruthy();
    expect(view.container.textContent).toContain("<script>window.__pwned = true</script>");
    expectInert(view.container);
  });

  it("forks a snapshot and links to the new sandboxes, listing partial failures", async () => {
    let body: unknown;
    server.use(
      http.get("*/v1/snapshots", () => HttpResponse.json({ items: [snapshot()] })),
      http.post("*/v1/snapshots/:id/fork", async ({ request }) => {
        body = await request.json();
        return HttpResponse.json(
          {
            items: [sandbox({ id: "sbx_fork1" })],
            failures: [{ index: 1, code: "quota_exceeded", message: "too many sandboxes" }],
          },
          { status: 201 },
        );
      }),
    );
    renderApp("/snapshots");
    const user = userEvent.setup();
    await screen.findByText(snapshot().id);
    await user.clear(screen.getByLabelText("Copies"));
    await user.type(screen.getByLabelText("Copies"), "3");
    await user.click(screen.getByRole("button", { name: "Fork" }));
    expect(await screen.findByRole("link", { name: "sbx_fork1" })).toBeTruthy();
    expect(screen.getByText("Copy 2 failed: quota_exceeded: too many sandboxes")).toBeTruthy();
    expect(body).toEqual({ count: 3 });
  });

  it("does not submit an unusable number of copies", async () => {
    server.use(http.get("*/v1/snapshots", () => HttpResponse.json({ items: [snapshot()] })));
    renderApp("/snapshots");
    const user = userEvent.setup();
    await screen.findByText(snapshot().id);
    await user.clear(screen.getByLabelText("Copies"));
    await user.type(screen.getByLabelText("Copies"), "0");
    expect(screen.getByRole<HTMLButtonElement>("button", { name: "Fork" }).disabled).toBe(true);
  });

  it("deletes a snapshot only after confirmation", async () => {
    let deleted = "";
    server.use(
      http.get("*/v1/snapshots", () => HttpResponse.json({ items: [snapshot()] })),
      http.delete("*/v1/snapshots/:id", ({ params }) => {
        deleted = String(params.id);
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/snapshots");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Delete" }));
    expect(deleted).toBe("");
    await user.click(screen.getByRole("button", { name: "Delete snapshot" }));
    await waitFor(() => expect(deleted).toBe(snapshot().id));
  });

  it("says when there are none", async () => {
    server.use(http.get("*/v1/snapshots", () => HttpResponse.json({ items: [] })));
    renderApp("/snapshots");
    expect(await screen.findByText("No snapshots.")).toBeTruthy();
  });
});

const adminSession = () =>
  http.get("*/v1/sessions/current", () => HttpResponse.json(sessionBody({ admin: true })));

describe("Keys", () => {
  it("is closed to non-admin sessions and never asks the server for keys", async () => {
    let asked = false;
    server.use(
      http.get("*/v1/keys", () => {
        asked = true;
        return HttpResponse.json({ items: [] });
      }),
    );
    renderApp("/keys");
    expect((await screen.findByRole("alert")).textContent).toMatch(/needs an admin key/);
    expect(asked).toBe(false);
  });

  it("lists keys with their role and status, text only", async () => {
    server.use(
      adminSession(),
      http.get("*/v1/keys", () =>
        HttpResponse.json({
          items: [
            key({ name: HOSTILE, is_admin: true }),
            key({ id: "key_old", name: "old", revoked_at: "2026-11-10T00:00:00Z" }),
          ],
        }),
      ),
    );
    const view = renderApp("/keys");
    expect(await screen.findByText("key_old")).toBeTruthy();
    expect(screen.getByText("admin")).toBeTruthy();
    expect(screen.getByText(/revoked/)).toBeTruthy();
    expect(view.container.textContent).toContain("<script>window.__pwned = true</script>");
    expectInert(view.container);
    expect(screen.getAllByRole("button", { name: "Revoke" })).toHaveLength(1);
  });

  it("creates a key with the admin key re-entered, shows the secret once and keeps it nowhere", async () => {
    let seen: { reauth: string | null; csrf: string | null; body: unknown } | null = null;
    server.use(
      adminSession(),
      http.get("*/v1/keys", () => HttpResponse.json({ items: [] })),
      http.post("*/v1/keys", async ({ request }) => {
        seen = {
          reauth: request.headers.get("X-Arcana-Reauth"),
          csrf: request.headers.get("X-CSRF-Token"),
          body: await request.json(),
        };
        return HttpResponse.json({ key: key({ name: "ci" }), secret: SECRET }, { status: 201 });
      }),
    );
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const view = renderApp("/keys");
    const user = userEvent.setup();
    const form = await screen.findByRole("form", { name: "Create API key" });
    const submit = within(form).getByRole("button", { name: "Create key" });
    expect((submit as HTMLButtonElement).disabled).toBe(true);
    await user.type(within(form).getByLabelText("Name"), "ci");
    await user.click(within(form).getByLabelText("Admin key"));
    await user.type(within(form).getByLabelText("Your admin API key"), " arc_live_admin ");
    await user.click(submit);

    const secret = await screen.findByRole("region", { name: "New API key" });
    expect(within(secret).getByText(SECRET)).toBeTruthy();
    expect(seen).toEqual({
      reauth: "arc_live_admin",
      csrf: "csrf-test-token",
      body: { name: "ci", is_admin: true },
    });
    // The re-entered admin key is not kept in the form.
    expect(
      within(screen.getByRole("form", { name: "Create API key" })).getByLabelText<HTMLInputElement>(
        "Your admin API key",
      ).value,
    ).toBe("");
    expect(setItem).not.toHaveBeenCalled();
    expect(
      JSON.stringify(localStorage) + JSON.stringify(sessionStorage) + document.cookie,
    ).not.toContain(SECRET);

    // Typing elsewhere re-renders the page; the secret must stay until it is dismissed.
    await user.type(
      within(screen.getByRole("form", { name: "Create API key" })).getByLabelText("Name"),
      "x",
    );
    expect(screen.getByRole("region", { name: "New API key" })).toBeTruthy();

    await user.click(within(secret).getByRole("button", { name: "I have saved it" }));
    expect(screen.queryByRole("region", { name: "New API key" })).toBeNull();
    expect(view.container.textContent).not.toContain(SECRET);
    setItem.mockRestore();
  });

  it("shows the server's refusal when the admin key was typed wrong", async () => {
    server.use(
      adminSession(),
      http.get("*/v1/keys", () => HttpResponse.json({ items: [] })),
      http.post("*/v1/keys", () =>
        envelope(403, "forbidden", "re-enter your admin API key to manage keys"),
      ),
    );
    renderApp("/keys");
    const user = userEvent.setup();
    const form = await screen.findByRole("form", { name: "Create API key" });
    await user.type(within(form).getByLabelText("Name"), "ci");
    await user.type(within(form).getByLabelText("Your admin API key"), "wrong");
    await user.click(within(form).getByRole("button", { name: "Create key" }));
    expect((await screen.findByRole("alert")).textContent).toBe(
      "forbidden: re-enter your admin API key to manage keys",
    );
    expect(screen.queryByRole("region", { name: "New API key" })).toBeNull();
  });

  it("revokes a key only after confirmation with the admin key re-entered", async () => {
    let seen: { id: string; reauth: string | null } | null = null;
    server.use(
      adminSession(),
      http.get("*/v1/keys", () => HttpResponse.json({ items: [key()] })),
      http.delete("*/v1/keys/:id", ({ params, request }) => {
        seen = { id: String(params.id), reauth: request.headers.get("X-Arcana-Reauth") };
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/keys");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    expect(seen).toBeNull();
    const group = screen.getByRole("group", { name: /Revoke this key/ });
    await user.type(within(group).getByLabelText("Your admin API key"), "arc_live_admin");
    await user.click(within(group).getByRole("button", { name: "Revoke key" }));
    await waitFor(() => expect(seen).toEqual({ id: key().id, reauth: "arc_live_admin" }));
  });
});

describe("Audit", () => {
  it("is closed to non-admin sessions", async () => {
    renderApp("/audit");
    expect((await screen.findByRole("alert")).textContent).toMatch(/needs an admin key/);
  });

  it("shows events as text, filters server-side and pages with a cursor", async () => {
    const queries: string[] = [];
    server.use(
      adminSession(),
      http.get("*/v1/audit", ({ request }) => {
        const url = new URL(request.url);
        queries.push(url.search);
        if (url.searchParams.get("cursor") === "next")
          return HttpResponse.json({
            items: [auditEvent({ id: "aud_2", action: "file.written" })],
          });
        return HttpResponse.json({
          items: [
            auditEvent({
              details: { argv: [HOSTILE], shell: HOSTILE },
              action: url.searchParams.get("action") || "exec.started",
            }),
          ],
          next_cursor: url.searchParams.get("action") ? null : "next",
        });
      }),
    );
    const view = renderApp("/audit");
    const user = userEvent.setup();
    expect(await screen.findByText("exec.started")).toBeTruthy();
    expect(view.container.textContent).toContain("<script>");
    expectInert(view.container);
    await user.click(screen.getByRole("button", { name: "Load more" }));
    expect(await screen.findByText("file.written")).toBeTruthy();
    await user.type(screen.getByLabelText("Action"), "key.created");
    await user.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText("key.created")).toBeTruthy();
    expect(queries.some((q) => q.includes("action=key.created"))).toBe(true);
  });

  it("says when nothing matches", async () => {
    server.use(
      adminSession(),
      http.get("*/v1/audit", () => HttpResponse.json({ items: [] })),
    );
    renderApp("/audit");
    expect(await screen.findByText("No events match.")).toBeTruthy();
  });
});

describe("Sandbox detail: log viewer", () => {
  it("keeps showing the exec it started with when a newer one appears in the history", async () => {
    let newer = false;
    server.use(
      http.get("*/v1/sandboxes/:id", () => HttpResponse.json(sandbox())),
      http.get("*/v1/sandboxes/:id/history", () => HttpResponse.json({ items: [] })),
      http.get("*/v1/sandboxes/:id/execs", () =>
        HttpResponse.json({
          items: newer
            ? [exec({ id: "exe_newer", cmd: ["du"] }), exec({ id: "exe_watched" })]
            : [exec({ id: "exe_watched" })],
        }),
      ),
    );
    const sources = fakeSources();
    const view = renderApp(`/sandboxes/${sandbox().id}`, sources);
    await screen.findByRole("log");
    expect(sources.find("exe_watched")).toBeTruthy();
    newer = true;
    await view.client.invalidateQueries({ queryKey: ["execs", sandbox().id] });
    await screen.findByText("du");
    expect(sources.find("exe_newer")).toBeUndefined();
  });
});
