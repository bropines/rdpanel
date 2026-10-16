import { expect, test, type Page } from "@playwright/test";

/**
 * Click-through smoke test against the mock build: every page, every main dialog,
 * and no uncaught errors or React warnings in the console.
 */

function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => {
    if (m.type() === "error" || (m.type() === "warning" && /React|Warning/.test(m.text()))) errors.push(`${m.type()}: ${m.text()}`);
  });
  return errors;
}

async function start(page: Page, user = "1") {
  await page.addInitScript((u) => {
    localStorage.setItem("rdpanel-lang", "en");
    localStorage.setItem("rdpanel-theme", "light");
    localStorage.setItem("rdpanel-mock-user", u);
  }, user);
}

const toast = (page: Page, text: string | RegExp) => expect(page.locator("[data-sonner-toast]").filter({ hasText: text }).first()).toBeVisible();

test("devices: bulk assign, drawer edit, delete", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page);
  await page.goto("/devices");
  const rows = page.getByRole("row");
  await expect(rows.nth(3)).toBeVisible();
  await page.getByRole("checkbox", { name: "Select row" }).nth(0).check();
  await page.getByRole("checkbox", { name: "Select row" }).nth(1).check();
  await expect(page.getByText("2 selected")).toBeVisible();
  await page.getByRole("button", { name: "Assign owner" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("combobox").click();
  await page.getByRole("option", { name: /Maria Kuznetsova/ }).click();
  await dialog.getByRole("button", { name: "Apply" }).click();
  await toast(page, "2 items updated");

  // Sorting via header
  await page.getByRole("button", { name: "RustDesk ID" }).click();
  await expect(page).toHaveURL(/sort=rustdesk_id/);

  // Filters
  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "Offline" }).click();
  await expect(page).toHaveURL(/online=false/);

  // Drawer
  await rows.nth(1).click();
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByText("Recent connections")).toBeVisible();
  await drawer.getByLabel("Alias").fill("Renamed by smoke");
  await drawer.getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
  await drawer.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete" }).click();
  await toast(page, "Device deleted");

  // CSV export
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export CSV" }).click();
  expect((await download).suggestedFilename()).toMatch(/^devices-.*\.csv$/);
  expect(errors).toEqual([]);
});

test("address books: share, tags, entries, add from devices", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page);
  await page.goto("/address-books");
  await page.getByRole("button", { name: /Datacenter/ }).click();
  await expect(page.getByRole("heading", { name: "Datacenter" })).toBeVisible();

  await page.getByRole("button", { name: /Share/ }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Group" }).click();
  await dialog.getByRole("combobox").first().click();
  await page.getByRole("option", { name: "Support" }).click();
  await dialog.getByRole("button", { name: "Add", exact: true }).click();
  await expect(dialog.getByRole("listitem").filter({ hasText: "Support" })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).last().click();

  await page.getByRole("button", { name: "Tags" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByPlaceholder("New tag").fill("smoke");
  await dialog.getByRole("button", { name: "Add" }).click();
  await expect(dialog.getByText("smoke")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).last().click();

  await page.getByRole("button", { name: "Add entry" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("RustDesk ID").fill("555666777");
  await dialog.getByLabel("Alias").fill("Smoke entry");
  await dialog.getByRole("button", { name: /^smoke/ }).click();
  await dialog.getByRole("button", { name: "Create" }).click();
  await toast(page, "Saved");

  await page.getByRole("button", { name: "Add from devices" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox").nth(0).check();
  await dialog.getByRole("checkbox").nth(1).check();
  await dialog.getByRole("button", { name: /Add 2 devices/ }).click();
  await toast(page, /added/);

  await page.getByRole("checkbox", { name: "Select row" }).nth(0).check();
  await page.getByRole("button", { name: "Edit tags" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: /^prod/ }).click();
  await dialog.getByRole("button", { name: "Apply" }).click();
  await toast(page, /updated/);
  expect(errors).toEqual([]);
});

test("users, groups, identity providers", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page);
  await page.goto("/users");
  await page.getByRole("button", { name: "New user" }).click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("Username").fill("smoke.user");
  await dialog.getByLabel("Password").fill("short");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(dialog.getByText("At least 8 characters")).toBeVisible();
  await dialog.getByLabel("Password").fill("long-enough-pass");
  await dialog.getByRole("button", { name: "Create" }).click();
  await toast(page, "User smoke.user created");
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByText("smoke.user").first()).toBeVisible();
  await drawer.getByRole("tab", { name: "Security" }).click();
  await drawer.getByRole("button", { name: "Reset password" }).click();
  await page.getByLabel("New password").fill("another-pass-1");
  await page.getByRole("dialog", { name: "Reset password" }).getByRole("button", { name: "Reset password" }).click();
  await toast(page, "Password changed");
  await drawer.getByRole("button", { name: "Close" }).click();

  // Last admin protection is visible.
  await page.getByRole("row", { name: /Alexei Volkov/ }).click();
  await expect(page.getByText("This is the only active administrator.", { exact: false })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();

  await page.goto("/groups?tab=device");
  await page.getByRole("button", { name: "New device group" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill("Smoke group");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("cell", { name: /Smoke group/ })).toBeVisible();

  await page.goto("/identity-providers");
  await page.getByRole("button", { name: "Test" }).first().click();
  await expect(page.getByRole("status").first()).toContainText("reachable");
  await page.getByRole("button", { name: "Add provider" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("Slug").fill("smoke");
  await dialog.getByLabel("Button label").fill("Smoke SSO");
  await dialog.getByLabel("Issuer URL").fill("https://sso.example.org");
  await dialog.getByLabel("Client ID").fill("panel");
  await dialog.getByLabel("Client secret").fill("secret");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByText("Smoke SSO")).toBeVisible();
  expect(errors).toEqual([]);
});

test("audit, server, settings, profile", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page);
  await page.goto("/audit");
  for (const tab of ["File transfers", "Alarms", "Sign-ins", "Admin actions", "Connections"]) {
    await page.getByRole("tab", { name: tab }).click();
    await expect(page.getByRole("table")).toBeVisible();
  }
  await page.getByRole("combobox", { name: "Period" }).click();
  await page.getByRole("option", { name: "Last 24 hours" }).click();
  await expect(page).toHaveURL(/co_range=24h/);
  await page.getByRole("button", { name: "Retention" }).click();
  await page.getByRole("textbox", { name: "Sign-ins" }).fill("90");
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");

  await page.goto("/server");
  await page.getByLabel("Total bandwidth").fill("2048");
  await expect(page.getByText("1 unsaved change")).toBeVisible();
  await page.getByRole("button", { name: "Save & apply" }).click();
  await toast(page, "Saved and applied");
  await page.getByRole("tab", { name: "Viewers" }).click();
  await page.getByRole("tab", { name: "IP blocker" }).click();
  await page.getByRole("button", { name: "Unblock" }).first().click();
  await toast(page, /unblocked/);
  await page.getByRole("tab", { name: "Console" }).click();
  await page.getByRole("combobox", { name: "Command" }).click();
  await page.getByRole("option", { name: /must-login/ }).click();
  await page.getByLabel("Arguments").fill("maybe");
  await expect(page.getByText("characters this command does not accept")).toBeVisible();
  await page.getByLabel("Arguments").fill("");
  await page.getByRole("button", { name: "Run" }).click();
  await expect(page.getByText("MUST_LOGIN: true")).toBeVisible();

  await page.goto("/settings");
  await page.getByRole("tab", { name: "Branding" }).click();
  await page.getByLabel("Title").fill("Smoke Panel");
  await page.getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
  await expect(page.getByRole("link", { name: "Smoke Panel" }).first()).toBeVisible();

  await page.goto("/my/profile");
  await page.getByRole("button", { name: "New recovery codes" }).click();
  await page.getByLabel("Authentication code or recovery code").fill("123456");
  await page.getByRole("dialog").getByRole("button", { name: "New recovery codes" }).click();
  await expect(page.getByText("These codes are shown only once.")).toBeVisible();
  await page.getByLabel("I have saved these codes").check();
  await page.getByRole("button", { name: "Done" }).click();
  expect(errors).toEqual([]);
});

test("about page: user menu, sidebar version, update check", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page);
  await page.goto("/");
  await page.getByRole("button", { name: "Account menu" }).click();
  await page.getByRole("menuitem", { name: "About" }).click();
  await expect(page).toHaveURL(/\/about$/);
  await expect(page.getByRole("heading", { name: "About" })).toBeVisible();
  await expect(page.getByTestId("update-status")).toHaveText("Version v0.2.0 is available.");
  await expect(page.getByText("Runtime")).toBeVisible();
  await expect(page.getByRole("link", { name: /Server fork/ })).toHaveAttribute("href", "https://github.com/bropines/rustdesk-server");

  await page.goto("/devices");
  await page.getByTestId("sidebar-version").click();
  await expect(page).toHaveURL(/\/about$/);
  expect(errors).toEqual([]);
});

test("user role sees no System pages", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page, "2"); // ivan
  await page.goto("/server");
  await expect(page.getByText("Access denied")).toBeVisible();
  await expect(page.getByRole("link", { name: "Users" })).toHaveCount(0);
  await page.getByRole("link", { name: "My address books" }).click();
  await page.getByRole("button", { name: /Family/ }).click();
  await expect(page.getByRole("heading", { name: "Family" })).toBeVisible();
  expect(errors).toEqual([]);
});

test("2FA enrolment, language and logout", async ({ page }) => {
  const errors = watchErrors(page);
  await start(page, "3"); // maria: 2FA required, not enrolled
  await page.goto("/my/devices");
  await expect(page).toHaveURL(/\/my\/profile/);
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Set up two-factor authentication")).toBeVisible();
  await expect(dialog.locator("canvas")).toBeVisible();
  await dialog.getByLabel("Code from the app").fill("123456");
  await dialog.getByRole("button", { name: "Activate" }).click();
  await expect(dialog.getByText("Save your recovery codes")).toBeVisible();
  await dialog.getByLabel("I have saved these codes").check();
  await dialog.getByRole("button", { name: "Done" }).click();

  await page.getByRole("link", { name: "My devices" }).click();
  await expect(page.getByRole("table")).toBeVisible();
  await page.getByRole("button", { name: "Language" }).click();
  await page.getByRole("menuitemradio", { name: "Русский" }).click();
  await expect(page.getByRole("heading", { name: "Мои устройства" })).toBeVisible();

  await page.getByRole("button", { name: "Меню учётной записи" }).click();
  await page.getByRole("menuitem", { name: "Выйти" }).click();
  await expect(page).toHaveURL(/\/login/);
  expect(errors).toEqual([]);
});
