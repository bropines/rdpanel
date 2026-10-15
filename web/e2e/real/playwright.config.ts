import { defineConfig } from "@playwright/test";

/**
 * End-to-end tests against a REAL rdpanel (embedded production UI + Go API +
 * SQLite), seeded through the client API by scripts/seed-demo:
 *
 *   make web build && scripts/e2e-server.sh          # backend on :31114
 *   cd web && npm run e2e:real
 *
 * E2E_BASE_URL overrides the target, E2E_ADMIN_PASSWORD / E2E_USER_PASSWORD the
 * seeded test credentials (defaults in fixtures.ts match scripts/e2e-server.sh).
 * The server-page spec needs hbbs/hbbr (HBB_BIN=... scripts/e2e-server.sh) and
 * E2E_HBBS=1; without it, it is skipped.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  outputDir: "../../test-results/real",
  use: {
    baseURL: process.env.E2E_BASE_URL ?? "http://localhost:31114",
    viewport: { width: 1440, height: 900 },
    locale: "en-US",
    timezoneId: "Europe/Moscow",
    trace: "retain-on-failure",
  },
});
