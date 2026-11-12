// Records the dashboard while a snapshot-and-fork demo runs, and takes a screenshot of it.
//
//   scripts/with-arcana.sh node scripts/record-dashboard.mjs <output-dir>
//
// Needs the web/ dependencies installed, a Playwright Chromium, ffmpeg on PATH, and an Arcana built with the
// dashboard (make build-all) whose config has the python template (ARCANA_LIVE_TEMPLATES=python).
// ARCANA_URL and ARCANA_API_KEY come from scripts/with-arcana.sh. Writes dashboard.png and dashboard.gif.
import { execFileSync } from "node:child_process";
import { mkdirSync, readdirSync, renameSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { chromium } from "../web/node_modules/@playwright/test/index.mjs";

const out = resolve(process.argv[2] ?? "docs/images");
const base = process.env.ARCANA_URL;
const key = process.env.ARCANA_API_KEY;
if (!base || !key) throw new Error("ARCANA_URL and ARCANA_API_KEY are required (use scripts/with-arcana.sh)");
mkdirSync(out, { recursive: true });
const video = join(out, "video-tmp");
rmSync(video, { recursive: true, force: true });

const headers = { Authorization: `Bearer ${key}`, "Content-Type": "application/json" };
const api = async (method, path, body) => {
  const response = await fetch(new URL(path, base), { method, headers, body: body ? JSON.stringify(body) : undefined });
  if (!response.ok && response.status !== 204) throw new Error(`${method} ${path}: ${response.status} ${await response.text()}`);
  return response.status === 204 ? null : response.json();
};
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const size = { width: 1100, height: 680 };
const browser = await chromium.launch();
const context = await browser.newContext({ viewport: size, recordVideo: { dir: video, size } });
const page = await context.newPage();

// Sign in like a user would.
await page.goto(`${base}/ui/`);
await page.getByLabel("API key").fill(key);
await page.waitForTimeout(600);
await page.getByRole("button", { name: "Sign in" }).click();
await page.getByRole("heading", { name: "Overview" }).waitFor();
await page.waitForTimeout(1200);
await page.getByRole("link", { name: "Sandboxes" }).click();
await page.getByRole("heading", { name: "Sandboxes" }).waitFor();
await page.waitForTimeout(800);

// The demo: prepare a workspace once, snapshot it and fork three variants. Rows appear live.
const source = await api("POST", "/v1/sandboxes", { template: "python", ttl_seconds: 600, labels: { demo: "fork", role: "source" } });
await api("POST", `/v1/sandboxes/${source.id}/exec`, { shell: "echo base > /workspace/state.txt" });
await pause(1000);
const snapshot = await api("POST", `/v1/sandboxes/${source.id}/snapshots`);
const forks = (await api("POST", `/v1/snapshots/${snapshot.id}/fork`, { count: 3, labels: { demo: "fork" } })).items;
await pause(1500);

// Each variant works for a few seconds with coloured output. They run in parallel.
const work = forks.map((fork, i) =>
  api("POST", `/v1/sandboxes/${fork.id}/exec`, {
    shell: `for step in 1 2 3 4 5 6; do printf '\\033[36mvariant ${i + 1}\\033[0m step %s: ' $step; if [ $((step % 3)) -eq 0 ]; then printf '\\033[31mFAIL\\033[0m\\n'; else printf '\\033[32mok\\033[0m\\n'; fi; sleep 1; done; echo "variant ${i + 1} finished"`,
    timeout_seconds: 60,
  }),
);
await page.waitForTimeout(1000);
await page.getByRole("link", { name: forks[0].id }).click();
await page.getByRole("log").waitFor();
await page.getByRole("log").scrollIntoViewIfNeeded(); // so the live output is in frame
await page.waitForTimeout(3500);
await page.screenshot({ path: join(out, "dashboard.png"), fullPage: true });
await Promise.all(work);
await page.waitForTimeout(1500);

// Back to the list, then clean up and watch the states change.
await page.getByRole("link", { name: "All sandboxes" }).click();
await page.waitForTimeout(1200);
for (const fork of forks) await api("DELETE", `/v1/sandboxes/${fork.id}`);
await api("DELETE", `/v1/sandboxes/${source.id}`);
await api("DELETE", `/v1/snapshots/${snapshot.id}`);
await page.waitForTimeout(2500);

await context.close(); // finalises the video
await browser.close();

const recorded = readdirSync(video).find((name) => name.endsWith(".webm"));
if (!recorded) throw new Error("no video was recorded");
renameSync(join(video, recorded), join(out, "dashboard.webm"));
rmSync(video, { recursive: true, force: true });
execFileSync("ffmpeg", ["-loglevel", "error", "-y", "-i", join(out, "dashboard.webm"), "-vf", "fps=6,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=64[p];[b][p]paletteuse=dither=bayer", join(out, "dashboard.gif")]);
rmSync(join(out, "dashboard.webm"));
console.log(`wrote ${join(out, "dashboard.png")} and ${join(out, "dashboard.gif")}`);
