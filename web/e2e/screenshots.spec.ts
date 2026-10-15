import { fileURLToPath } from "node:url";

import { expect, test, type Page } from "@playwright/test";

/** Writes docs/screenshots/<name>.jpg (1440×900, JPEG keeps each file well under 300 KB). */
const OUT = fileURLToPath(new URL("../../docs/screenshots/", import.meta.url));

type Theme = "light" | "dark";

async function prepare(page: Page, opts: { theme?: Theme; lang?: string; loggedOut?: boolean } = {}) {
  await page.addInitScript(
    ([theme, lang, loggedOut]) => {
      localStorage.setItem("rdpanel-theme", theme);
      localStorage.setItem("rdpanel-lang", lang);
      if (loggedOut) localStorage.setItem("rdpanel-mock-user", "");
      else if (localStorage.getItem("rdpanel-mock-user") === "") localStorage.removeItem("rdpanel-mock-user");
    },
    [opts.theme ?? "light", opts.lang ?? "en", opts.loggedOut ?? false] as const,
  );
}

async function settle(page: Page) {
  await page.waitForLoadState("networkidle");
  // Skeletons gone, fonts/animations done.
  await expect(page.locator(".animate-pulse")).toHaveCount(0, { timeout: 15_000 });
  await page.mouse.move(0, 0);
  await page.waitForTimeout(400);
}

async function shot(page: Page, name: string) {
  await page.screenshot({ path: `${OUT}${name}.jpg`, type: "jpeg", quality: 82 });
}

const pages: { name: string; path: string; ready?: (p: Page) => Promise<void> }[] = [
  { name: "users", path: "/users" },
  {
    name: "user-drawer",
    path: "/users?user=1",
    ready: async (p) => {
      await p.getByRole("tab", { name: "Security" }).click();
    },
  },
  { name: "groups", path: "/groups" },
  { name: "address-books", path: "/address-books?book=4" },
  { name: "audit", path: "/audit" },
  { name: "audit-logins", path: "/audit?tab=logins" },
  { name: "identity-providers", path: "/identity-providers" },
  { name: "server", path: "/server" },
  { name: "server-console", path: "/server?tab=console" },
  { name: "settings", path: "/settings" },
  { name: "profile", path: "/my/profile" },
  { name: "my-sessions", path: "/my/sessions" },
  { name: "my-devices", path: "/my/devices" },
];

test("login", async ({ page }) => {
  await prepare(page, { loggedOut: true });
  await page.goto("/login");
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
  await settle(page);
  await shot(page, "login");
});

for (const theme of ["light", "dark"] as const) {
  test(`dashboard ${theme}`, async ({ page }) => {
    await prepare(page, { theme });
    await page.goto("/");
    await expect(page.getByText("Online now")).toBeVisible();
    await settle(page);
    await shot(page, `dashboard-${theme}`);
  });

  test(`devices ${theme}`, async ({ page }) => {
    await prepare(page, { theme });
    await page.goto("/devices");
    await expect(page.getByRole("table")).toBeVisible();
    await settle(page);
    await shot(page, `devices-${theme}`);
  });
}

test("device drawer", async ({ page }) => {
  await prepare(page);
  await page.goto("/devices?device=3");
  await expect(page.getByText("Recent connections")).toBeVisible();
  await settle(page);
  await shot(page, "device-drawer");
});

test("devices ru", async ({ page }) => {
  await prepare(page, { lang: "ru" });
  await page.goto("/devices");
  await expect(page.getByRole("table")).toBeVisible();
  await settle(page);
  await shot(page, "devices-ru");
});

test("dashboard mobile", async ({ page }) => {
  await prepare(page);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/");
  await expect(page.getByText("Online now")).toBeVisible();
  await settle(page);
  await shot(page, "dashboard-mobile");
});

for (const p of pages) {
  test(p.name, async ({ page }) => {
    await prepare(page);
    await page.goto(p.path);
    await page.waitForLoadState("networkidle");
    await p.ready?.(page);
    await settle(page);
    await shot(page, p.name);
  });
}
