import { expect, loginAdmin, test, toast } from "./fixtures";

/**
 * Server page against a running hbbs/hbbr (scripts/e2e-server.sh with
 * HBB_BIN=…, then E2E_HBBS=1 npm run e2e:real). Without hbbs every /server/*
 * endpoint answers 503 by design, so the spec is skipped.
 */
test.skip(!process.env.E2E_HBBS, "needs hbbs/hbbr (E2E_HBBS=1)");

test("server: health, settings apply, viewers, console", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/server");
  await expect(page.getByText("ID server (hbbs)").first()).toBeVisible();
  await expect(page.getByText("up").first()).toBeVisible();

  await page.getByLabel("Total bandwidth").fill("2048");
  await expect(page.getByText("1 unsaved change")).toBeVisible();
  await page.getByRole("button", { name: "Save & apply" }).click();
  await toast(page, "Saved and applied");
  await expect(page.getByText("All settings are applied.")).toBeVisible();

  await page.getByRole("tab", { name: "Viewers" }).click();
  for (const tab of ["IP blocker", "IP changes", "Punch requests", "Relay usage"]) {
    const t = page.getByRole("tab", { name: tab });
    if (await t.count()) await t.click();
  }

  await page.getByRole("tab", { name: "Console" }).click();
  await page.getByRole("combobox", { name: "Command" }).click();
  await page.getByRole("option", { name: /^h\b|help/ }).first().click();
  await page.getByRole("button", { name: "Run" }).click();
  await expect(page.locator("pre").filter({ hasText: /relay-servers|always-use-relay|must-login/ }).first()).toBeVisible();
});
