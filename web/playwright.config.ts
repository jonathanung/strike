import { defineConfig, devices } from "@playwright/test";

/**
 * Real-browser smokes for strike serve (WEBUI.5 / #1071).
 * Server URL comes from STRIKE_E2E_BASE (set by scripts/web-e2e.sh).
 */
// Origin only — tests navigate to /attach explicitly.
const baseURL = process.env.STRIKE_E2E_BASE?.replace(/\/attach\/?$/, "") || "http://127.0.0.1:8791";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [["list"], ["html", { open: "never", outputFolder: "e2e-report" }]] : "list",
  outputDir: "e2e-results",
  use: {
    baseURL,
    headless: true,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
    reducedMotion: "reduce",
    ignoreHTTPSErrors: true,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
