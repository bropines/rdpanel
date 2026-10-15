import { createHmac } from "node:crypto";

import { expect, test as base, type Page } from "@playwright/test";

/** Test credentials created by scripts/e2e-server.sh (test values only). */
export const ADMIN = { username: "admin", password: process.env.E2E_ADMIN_PASSWORD ?? "e2e-admin-password-1" };
export const USER_PASSWORD = process.env.E2E_USER_PASSWORD ?? "demo-password-1";

/**
 * Every test fails on browser console errors, uncaught page errors and any
 * 5xx answer from the server.
 */
export const test = base.extend<{ problems: string[] }>({
  problems: [
    async ({ page }, use) => {
      const problems: string[] = [];
      page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
      page.on("console", (m) => {
        // Chromium logs every 4xx fetch ("Failed to load resource"); those are
        // expected API answers (anonymous /auth/me, validation errors) — 5xx
        // are caught from the responses below.
        if (m.type() === "error" && !/Failed to load resource: the server responded with a status of 4\d\d/.test(m.text()))
          problems.push(`console.error: ${m.text()}`);
      });
      page.on("response", (r) => {
        if (r.status() >= 500) problems.push(`HTTP ${r.status()} ${r.request().method()} ${r.url()}`);
      });
      await page.addInitScript(() => {
        localStorage.setItem("rdpanel-lang", "en");
        localStorage.setItem("rdpanel-theme", "light");
      });
      await use(problems);
      expect(problems, "console errors / 5xx responses").toEqual([]);
    },
    { auto: true },
  ],
});
export { expect };

export const toast = (page: Page, text: string | RegExp) =>
  expect(page.locator("[data-sonner-toast]").filter({ hasText: text }).first()).toBeVisible();

export async function login(page: Page, username = ADMIN.username, password = ADMIN.password) {
  await page.goto("/login");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
}

export async function loginAdmin(page: Page) {
  await login(page);
  await expect(page).not.toHaveURL(/\/login/);
}

/** Unique suffix so the suite can run repeatedly against the same database. */
export const RUN = Date.now().toString(36);

/** Calls the admin API with the page's session (CSRF header from the cookie). */
export async function adminApi(page: Page, method: "POST" | "PATCH" | "DELETE", path: string, data?: unknown) {
  const csrf = (await page.context().cookies()).find((c) => c.name === "rdpanel_csrf")?.value ?? "";
  const res = await page.request.fetch(`/api/v1/admin${path}`, { method, data, headers: { "X-CSRF-Token": csrf } });
  expect(res.status(), `${method} ${path}: ${await res.text()}`).toBeLessThan(400);
  return res.status() === 204 ? undefined : res.json();
}

export async function logout(page: Page) {
  await page.getByRole("button", { name: "Account menu" }).click();
  await page.getByRole("menuitem", { name: "Log out" }).click();
  await expect(page).toHaveURL(/\/login/);
}

function base32Decode(s: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  const out: number[] = [];
  for (const c of s.replace(/=+$/, "").toUpperCase()) {
    const i = alphabet.indexOf(c);
    if (i < 0) continue;
    value = (value << 5) | i;
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return Buffer.from(out);
}

/** RFC 6238 TOTP (SHA-1, 6 digits, 30 s) for the step `offset` steps from now. */
export function totp(secret: string, offset = 0, now = Date.now()): string {
  const counter = Math.floor(now / 1000 / 30) + offset;
  const msg = Buffer.alloc(8);
  msg.writeBigUInt64BE(BigInt(counter));
  const h = createHmac("sha1", base32Decode(secret)).update(msg).digest();
  const o = h[h.length - 1] & 0xf;
  const code = ((h.readUInt32BE(o) & 0x7fffffff) % 1_000_000).toString();
  return code.padStart(6, "0");
}
