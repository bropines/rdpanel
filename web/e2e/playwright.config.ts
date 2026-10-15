import { defineConfig } from "@playwright/test";

/**
 * Screenshot run against the mock build:
 *   npm run build:mock && npm run screenshots
 * (BASE_URL overrides the target, e.g. a running `npm run dev:mock`.)
 */
const port = 4174;

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  timeout: 60_000,
  fullyParallel: false,
  workers: 1,
  reporter: "list",
  outputDir: "../test-results",
  use: {
    baseURL: process.env.BASE_URL ?? `http://localhost:${port}`,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    locale: "en-US",
    timezoneId: "Europe/Moscow",
    serviceWorkers: "allow",
  },
  webServer: process.env.BASE_URL
    ? undefined
    : {
        command: "npm run preview:mock",
        url: `http://localhost:${port}`,
        reuseExistingServer: true,
        timeout: 60_000,
      },
});
