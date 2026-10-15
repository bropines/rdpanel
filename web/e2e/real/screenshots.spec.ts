import { fileURLToPath } from "node:url";

import type { Page } from "@playwright/test";

import { adminApi, expect, loginAdmin, test } from "./fixtures";

/**
 * Documentation screenshots from the REAL backend with seed-demo data
 * (run with hbbs so the server page and presence are live):
 *
 *   HBB_BIN=... scripts/e2e-server.sh && cd web && npm run screenshots:real
 *
 * Writes docs/screenshots/<name>.jpg (1440×900 JPEG, each well under 300 KB).
 * Pages that need data the demo cannot produce (linked OIDC identities) keep
 * their mock screenshots (npm run screenshots).
 */
const OUT = fileURLToPath(new URL("../../../docs/screenshots/", import.meta.url));

async function settle(page: Page) {
  await page.waitForLoadState("networkidle");
  await expect(page.locator(".animate-pulse")).toHaveCount(0, { timeout: 15_000 });
  await page.mouse.move(0, 0);
  await page.waitForTimeout(500);
}

async function shot(page: Page, name: string) {
  await settle(page);
  await page.screenshot({ path: `${OUT}${name}.jpg`, type: "jpeg", quality: 82 });
}

async function theme(page: Page, t: "light" | "dark", lang = "en") {
  await page.addInitScript(
    ([t, lang]) => {
      localStorage.setItem("rdpanel-theme", t);
      localStorage.setItem("rdpanel-lang", lang);
    },
    [t, lang] as const,
  );
}

test.describe("@screenshots", () => {
  test("login", async ({ page }) => {
    await page.goto("/login");
    await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
    await shot(page, "login");
  });

  test("admin pages (light)", async ({ page }) => {
    await loginAdmin(page);
    // Nice-looking providers for the identity-providers page.
    const list = (await (await page.request.get("/api/v1/admin/identity-providers")).json()) as { items: { slug: string }[] };
    if (!list.items.some((p) => p.slug === "github")) {
      await adminApi(page, "POST", "/identity-providers", { slug: "github", kind: "github", display_name: "GitHub", client_id: "Iv1.8a61f9b3a7aba766", client_secret: "demo", sort_order: 1 });
      await adminApi(page, "POST", "/identity-providers", { slug: "corp-sso", kind: "oidc", display_name: "Corporate SSO", client_id: "rdpanel", client_secret: "demo", issuer: "https://sso.example.com/realms/corp", sort_order: 2, link_by_verified_email: true });
    }
    await page.goto("/");
    await expect(page.getByText("10 devices in total")).toBeVisible();
    await shot(page, "dashboard-light");

    await page.goto("/devices");
    await expect(page.getByRole("row")).toHaveCount(11);
    await shot(page, "devices-light");
    await page.getByRole("row", { name: /ws-alexei/ }).first().click();
    await expect(page.getByRole("dialog").getByText("Recent connections")).toBeVisible();
    await shot(page, "device-drawer");

    await page.goto("/users");
    await shot(page, "users");
    await page.getByRole("row", { name: /Ivan Sokolov/ }).click();
    await page.getByRole("dialog").getByRole("tab", { name: "Security" }).click();
    await shot(page, "user-drawer");

    await page.goto("/groups");
    await shot(page, "groups");

    await page.goto("/address-books");
    await page.getByRole("button", { name: /ivan/ }).first().click();
    await expect(page.getByRole("row", { name: /Build 01/ })).toBeVisible();
    await shot(page, "address-books");

    await page.goto("/audit");
    await shot(page, "audit");
    await page.goto("/audit?tab=logins");
    await shot(page, "audit-logins");

    await page.goto("/identity-providers");
    await shot(page, "identity-providers");

    await page.goto("/server");
    await expect(page.getByText("ID server (hbbs)").first()).toBeVisible();
    await shot(page, "server");
    await page.goto("/server?tab=console");
    await page.getByRole("combobox", { name: "Command" }).click();
    await page.getByRole("option", { name: /^h\b|help/ }).first().click();
    await page.getByRole("button", { name: "Run" }).click();
    await expect(page.locator("pre").first()).toBeVisible();
    await shot(page, "server-console");

    await page.goto("/settings");
    await shot(page, "settings");
    await page.goto("/my/sessions");
    await shot(page, "my-sessions");
    await page.goto("/my/devices");
    await shot(page, "my-devices");
  });

  test("dark and russian", async ({ page }) => {
    await theme(page, "dark");
    await loginAdmin(page);
    await page.goto("/");
    await shot(page, "dashboard-dark");
    await page.goto("/devices");
    await shot(page, "devices-dark");
  });

  test("russian", async ({ page }) => {
    await theme(page, "light", "ru");
    await page.goto("/login");
    await page.getByLabel("Имя пользователя").fill("admin");
    await page.getByLabel("Пароль").fill(process.env.E2E_ADMIN_PASSWORD ?? "e2e-admin-password-1");
    await page.getByRole("button", { name: "Войти" }).click();
    await expect(page).not.toHaveURL(/\/login/);
    await page.goto("/devices");
    await shot(page, "devices-ru");
  });

  test("mobile", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await loginAdmin(page);
    await page.goto("/");
    await shot(page, "dashboard-mobile");
  });
});
