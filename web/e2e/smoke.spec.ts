import { expect, test, type Page } from "@playwright/test";

const key = process.env.ARCANA_API_KEY ?? "";
const auth = { Authorization: `Bearer ${key}` };

test.skip(
  !key || !process.env.ARCANA_URL,
  "set ARCANA_URL and ARCANA_API_KEY (scripts/with-arcana.sh does)",
);

/** Records every Content-Security-Policy violation the page reports, and uncaught page errors. */
async function watchViolations(page: Page) {
  const violations: string[] = [];
  const errors: string[] = [];
  await page.addInitScript(() => {
    (window as unknown as { __csp: string[] }).__csp = [];
    document.addEventListener("securitypolicyviolation", (e) => {
      (window as unknown as { __csp: string[] }).__csp.push(
        `${e.violatedDirective}: ${e.blockedURI}`,
      );
    });
  });
  page.on("console", (msg) => {
    if (/content security policy|refused to (load|execute|apply)/i.test(msg.text()))
      violations.push(msg.text());
  });
  page.on("pageerror", (error) => errors.push(error.message));
  return {
    async assertClean() {
      const fromPage = await page
        .evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? [])
        .catch(() => []);
      expect([...violations, ...fromPage], "CSP violations").toEqual([]);
      expect(errors, "uncaught page errors").toEqual([]);
    },
  };
}

test("log in, watch a sandbox appear and an exec's output arrive live", async ({
  page,
  request,
}) => {
  const watch = await watchViolations(page);

  // The dashboard asks for a session before showing anything.
  await page.goto("/ui/");
  await expect(page.getByLabel("API key")).toBeVisible();
  await page.getByLabel("API key").fill(key);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

  // The API key is never kept by the browser: no web storage, and the session cookie is HttpOnly.
  const stored = await page.evaluate(() => {
    const dump = (s: Storage) =>
      Object.keys(s)
        .map((k) => `${k}=${s.getItem(k) ?? ""}`)
        .join(";");
    return dump(localStorage) + dump(sessionStorage) + document.cookie;
  });
  expect(stored).not.toContain(key);
  const cookies = await page.context().cookies();
  const session = cookies.find((c) => c.name === "arcana_session");
  expect(session?.httpOnly).toBe(true);
  expect(session?.sameSite).toBe("Strict");

  // A sandbox created through the API appears without reloading, via the event stream.
  await page.getByRole("link", { name: "Sandboxes" }).click();
  await expect(page.getByRole("heading", { name: "Sandboxes" })).toBeVisible();
  const created = await request.post("/v1/sandboxes", {
    headers: auth,
    data: { template: "base", ttl_seconds: 600, labels: { e2e: "smoke" } },
  });
  expect(created.status()).toBe(201);
  const sandbox = (await created.json()) as { id: string };
  await expect(page.getByRole("link", { name: sandbox.id })).toBeVisible();

  try {
    // A command that prints over time, plus markup that must stay inert.
    const script =
      "echo first; echo '<script>window.__pwned=1</script><b>bold</b>'; sleep 10; echo last";
    const slow = request.post(`/v1/sandboxes/${sandbox.id}/exec`, {
      headers: auth,
      data: { shell: script },
      timeout: 60_000,
    });
    await page.getByRole("link", { name: sandbox.id }).click();
    await expect(page.getByRole("heading", { name: sandbox.id })).toBeVisible();
    const log = page.getByRole("log");
    await expect(log).toContainText("first");
    // Seen while the command is still running: output is live, not a replay after the exit.
    await expect(page.getByRole("status").filter({ hasText: /Running…|Exited/ })).toContainText(
      "Running…",
    );
    await expect(log).not.toContainText("last");
    await expect(log).toContainText("<script>window.__pwned=1</script><b>bold</b>");
    await expect(log).toContainText("last");
    await expect(page.getByRole("status").filter({ hasText: "Exited" })).toContainText(
      "Exited with status 0.",
    );
    expect((await slow).status()).toBe(200);

    // ESC-24 in a real browser: nothing from the output became live markup or ran.
    expect(
      await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned),
    ).toBeUndefined();
    await expect(log.locator("b, script")).toHaveCount(0);
    await expect(page.getByRole("table", { name: "Command history, newest first" })).toContainText(
      "echo first",
    );
    await expect(page.getByRole("heading", { name: "State history" })).toBeVisible();

    // The dashboard is read-only: the only writes are login and logout.
    await expect(page.getByRole("button", { name: /create|destroy|delete|run/i })).toHaveCount(0);
  } finally {
    await request.delete(`/v1/sandboxes/${sandbox.id}`, { headers: auth });
  }

  // The destroyed sandbox shows its end state live, then logout returns to the login form.
  await page.getByRole("link", { name: "All sandboxes" }).click();
  await expect(
    page.getByRole("link", { name: sandbox.id }).locator("xpath=ancestor::tr"),
  ).toContainText(/destroy/);
  await page.getByRole("button", { name: "Log out" }).click();
  await expect(page.getByLabel("API key")).toBeVisible();
  await page.goto("/ui/sandboxes");
  await expect(page.getByLabel("API key")).toBeVisible();

  await watch.assertClean();
});

test("the dashboard is served with the strict security headers and a working SPA fallback", async ({
  request,
}) => {
  const index = await request.get("/ui/");
  expect(index.status()).toBe(200);
  const csp = index.headers()["content-security-policy"] ?? "";
  expect(csp).toBe(
    "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  );
  expect(index.headers()["x-content-type-options"]).toBe("nosniff");
  expect(index.headers()["referrer-policy"]).toBe("no-referrer");
  expect(index.headers()["cache-control"]).toBe("no-cache");
  const deep = await request.get("/ui/sandboxes/sbx_whatever");
  expect(deep.status()).toBe(200);
  expect(await deep.text()).toContain('<div id="root">');
  const html = await index.text();
  const asset = /src="(\/ui\/assets\/[^"]+\.js)"/.exec(html)?.[1];
  expect(asset).toBeTruthy();
  const js = await request.get(asset ?? "");
  expect(js.headers()["cache-control"]).toBe("public, max-age=31536000, immutable");
  expect((await request.get("/ui/assets/nope.js")).status()).toBe(404);
  // The API stays protected next to the public static files.
  expect((await request.get("/v1/sandboxes")).status()).toBe(401);
});
