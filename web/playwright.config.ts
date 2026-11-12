import { defineConfig, devices } from "@playwright/test";

// The suite runs against a real Arcana started by scripts/with-arcana.sh (ARCANA_URL and
// ARCANA_API_KEY are set by it); it never starts a server of its own.
export default defineConfig({
  testDir: "e2e",
  timeout: 90_000,
  expect: { timeout: 15_000 },
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: { baseURL: process.env.ARCANA_URL, trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
