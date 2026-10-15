import { ADMIN, adminApi, expect, login, loginAdmin, logout, RUN, test, toast, totp, USER_PASSWORD } from "./fixtures";

/**
 * Real-backend walk through every panel area with the seed-demo data
 * (10 devices, users ivan/maria/olga, groups Engineering/Support,
 * device groups Office/Servers, a shared "Support desk" book).
 */
test.describe.configure({ mode: "default" });

test("password login, dashboard, logout", async ({ page }) => {
  await login(page, ADMIN.username, "definitely-wrong");
  await expect(page.getByRole("alert")).toHaveText("Invalid username or password");
  const dash = page.waitForResponse((r) => r.url().endsWith("/api/v1/admin/dashboard") && r.ok());
  await login(page);
  await expect(page).toHaveURL(/\/$/);
  const d = await (await dash).json();
  expect(d.devices_total).toBe(10);
  expect(d.users_total).toBe(4);
  await expect(page.getByText("10 devices in total")).toBeVisible();
  await expect(page.getByText("Online now").first()).toBeVisible();
  await expect(page.getByText("Recent connections")).toBeVisible();
  await logout(page);
});

test("TOTP enrolment and MFA login", async ({ page }) => {
  // A fresh user per run (2FA cannot be enrolled twice).
  const name = `totp.${RUN}`;
  await loginAdmin(page);
  await adminApi(page, "POST", "/users", { username: name, password: USER_PASSWORD, role: "user", display_name: "TOTP Tester" });
  await logout(page);
  await login(page, name, USER_PASSWORD);
  await expect(page).not.toHaveURL(/\/login/);
  await page.goto("/my/profile");
  const enroll = page.waitForResponse((r) => r.url().endsWith("/me/security/totp/enroll") && r.ok());
  await page.getByRole("button", { name: "Set up 2FA" }).click();
  const { secret } = (await (await enroll).json()) as { secret: string };
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("canvas")).toBeVisible();
  await dialog.getByLabel("Code from the app").fill("000000");
  await dialog.getByRole("button", { name: "Activate" }).click();
  await expect(dialog.getByText("The code is wrong or expired").or(dialog.getByText(/invalid verification code/i))).toBeVisible();
  const confirm = page.waitForResponse((r) => r.url().endsWith("/me/security/totp/confirm") && r.ok());
  await dialog.getByLabel("Code from the app").fill(totp(secret));
  await dialog.getByRole("button", { name: "Activate" }).click();
  const { recovery_codes } = (await (await confirm).json()) as { recovery_codes: string[] };
  expect(recovery_codes.length).toBeGreaterThanOrEqual(8);
  await expect(dialog.getByText("Save your recovery codes")).toBeVisible();
  await dialog.getByLabel("I have saved these codes").check();
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(page.getByText("A code from your authenticator app is required at sign-in.")).toBeVisible();
  await logout(page);

  // Password step → second factor (the next TOTP step: the confirmed one is spent).
  await login(page, name, USER_PASSWORD);
  await expect(page.getByText("Two-factor authentication")).toBeVisible();
  await page.getByLabel("Authentication code").fill("123456");
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByRole("alert")).toHaveText("The code is wrong or expired");
  await page.getByLabel("Authentication code").fill(totp(secret, 1));
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page).not.toHaveURL(/\/login/);
  await logout(page);

  // A recovery code works once.
  await login(page, name, USER_PASSWORD);
  await page.getByLabel("Authentication code").fill(recovery_codes[0]);
  await page.getByRole("button", { name: "Verify" }).click();
  await expect(page).not.toHaveURL(/\/login/);
  await page.goto("/my/sessions");
  await expect(page.getByRole("heading", { name: "My sessions" })).toBeVisible();
  await logout(page);
});

test("devices: list, drawer, alias, kick", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/devices");
  await expect(page.getByRole("row")).toHaveCount(11); // header + 10
  await expect(page.getByRole("cell", { name: /mbp-ivan/ }).first()).toBeVisible();
  await page.getByRole("row", { name: /ws-alexei/ }).first().click();
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByText("Intel Core i7-12700, 12 cores")).toBeVisible();
  await expect(drawer.getByText("Recent connections")).toBeVisible();
  const alias = `Alexei workstation ${RUN}`;
  await drawer.getByLabel("Alias").fill(alias);
  await drawer.getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
  await drawer.getByRole("button", { name: "Disconnect" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Disconnect" }).click();
  await toast(page, "Disconnect requested");
  await drawer.getByRole("button", { name: "Close" }).first().click();
  await expect(page.getByRole("row", { name: new RegExp(alias) })).toBeVisible();

  // Filters hit the real query parameters.
  await page.getByRole("combobox", { name: "Status" }).click();
  await page.getByRole("option", { name: "Offline" }).click();
  await expect(page).toHaveURL(/online=false/);
  // Presence depends on timing (heartbeat fallback, 45 s): compare with the API.
  const offline = (await (await page.request.get("/api/v1/admin/devices?online=false&page_size=100")).json()) as {
    items: { hostname: string; online: boolean }[];
  };
  expect(offline.items.every((d) => !d.online)).toBe(true);
  if (offline.items.length === 0) await expect(page.getByText("No devices match the filters")).toBeVisible();
  else await expect(page.getByRole("row", { name: new RegExp(offline.items[0].hostname) })).toBeVisible();

  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export CSV" }).click();
  expect((await download).suggestedFilename()).toMatch(/\.csv$/);
});

test("users: create, reset password, delete", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/users");
  await expect(page.getByRole("row", { name: /Maria Kuznetsova/ })).toBeVisible();
  await page.getByRole("button", { name: "New user" }).click();
  const dialog = page.getByRole("dialog");
  const uname = `e2e.${RUN}`;
  await dialog.getByLabel("Username").fill(uname);
  await dialog.getByLabel("Display name").fill("E2E User");
  await dialog.getByLabel("Password").fill("short");
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(dialog.getByText("At least 8 characters")).toBeVisible();
  await dialog.getByLabel("Password").fill("e2e-user-password");
  await dialog.getByRole("button", { name: "Create" }).click();
  await toast(page, `User ${uname} created`);
  const drawer = page.getByRole("dialog");
  await drawer.getByRole("tab", { name: "Security" }).click();
  await drawer.getByRole("button", { name: "Reset password" }).click();
  await page.getByLabel("New password").fill("e2e-user-password-2");
  await page.getByRole("dialog", { name: "Reset password" }).getByRole("button", { name: "Reset password" }).click();
  await toast(page, "Password changed");
  await drawer.getByRole("tab", { name: "Profile" }).click();
  await drawer.getByRole("button", { name: "Delete" }).click();
  const confirm = page.getByRole("alertdialog");
  const typed = confirm.getByRole("textbox");
  if (await typed.count()) await typed.fill(uname);
  await confirm.getByRole("button", { name: "Delete" }).click();
  await toast(page, "User deleted");
  await expect(page.getByRole("row", { name: new RegExp(uname.replace(".", "\\.")) })).toHaveCount(0);

  // The last admin cannot be demoted.
  await page.getByRole("row", { name: /Alexei Volkov/ }).click();
  await expect(page.getByText("This is the only active administrator.", { exact: false })).toBeVisible();
});

test("address books: create, share, entries, tags", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/address-books");
  await expect(page.getByRole("button", { name: /Support desk/ })).toBeVisible();
  await page.getByRole("button", { name: "New address book" }).click();
  let dialog = page.getByRole("dialog");
  const book = `E2E book ${RUN}`;
  await dialog.getByLabel("Name").fill(book);
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: book })).toBeVisible();

  await page.getByRole("button", { name: /^Share/ }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Group" }).click();
  await dialog.getByRole("combobox").first().click();
  await page.getByRole("option", { name: "Engineering" }).click();
  await dialog.getByRole("button", { name: "Add", exact: true }).click();
  await expect(dialog.getByRole("listitem").filter({ hasText: "Engineering" }).first()).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).last().click();

  await page.getByRole("button", { name: "Tags" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByPlaceholder("New tag").fill("e2e");
  await dialog.getByRole("button", { name: "Add" }).click();
  await expect(dialog.getByText("e2e")).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: "Close" }).last().click();

  await page.getByRole("button", { name: "Add entry" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("RustDesk ID").fill("555666777");
  await dialog.getByLabel("Alias").fill("E2E entry");
  await dialog.getByRole("button", { name: /^e2e/ }).click();
  await dialog.getByRole("button", { name: "Create" }).click();
  await toast(page, "Saved");
  await expect(page.getByRole("row", { name: /E2E entry/ })).toBeVisible();

  await page.getByRole("button", { name: "Add from devices" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox").nth(0).check();
  await dialog.getByRole("checkbox").nth(1).check();
  await dialog.getByRole("button", { name: /Add 2 devices/ }).click();
  await toast(page, /added/);
  await expect(page.getByRole("row")).toHaveCount(4); // header + 3

  // The seeded personal book of ivan (written through the client API).
  await page.getByRole("button", { name: /ivan/ }).first().click();
  await expect(page.getByRole("row", { name: /Build 01/ })).toBeVisible();
});

test("audit tabs", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/audit");
  await expect(page.getByRole("row").nth(1)).toBeVisible();
  await expect(page.getByRole("cell", { name: /234567891|mbp-ivan|Mbp-ivan/ }).first()).toBeVisible();
  for (const tab of ["File transfers", "Alarms", "Sign-ins", "Admin actions", "Connections"]) {
    await page.getByRole("tab", { name: tab }).click();
    await expect(page.getByRole("table")).toBeVisible();
    await expect(page.getByRole("row").nth(1)).toBeVisible();
  }
  await page.getByRole("button", { name: "Retention" }).click();
  await page.getByRole("textbox", { name: "Sign-ins" }).fill("90");
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
});

test("settings: save and read-only configuration", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/settings");
  await expect(page.getByText("set via RDPANEL_PUBLIC_URL")).toBeVisible();
  await page.getByRole("tab", { name: "Branding" }).click();
  await page.getByLabel("Title").fill(`E2E Panel ${RUN}`);
  await page.getByRole("tab", { name: "Security" }).click();
  await expect(page.getByText("3 free attempts", { exact: false })).toBeVisible();
  const idle = page.getByLabel("Panel session idle timeout");
  const want = (await idle.inputValue()) === "24" ? "18" : "24";
  await idle.fill(want);
  await page.getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
  await page.reload();
  await page.getByRole("tab", { name: "Security" }).click();
  await expect(page.getByLabel("Panel session idle timeout")).toHaveValue(want);
  await expect(page.getByRole("link", { name: `E2E Panel ${RUN}` }).first()).toBeVisible();
});

test("user role: my devices, my address books, no system pages", async ({ page }) => {
  await login(page, "ivan", USER_PASSWORD);
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page.getByRole("link", { name: "Users" })).toHaveCount(0);
  await page.getByRole("link", { name: "My devices" }).click();
  // Devices ivan signed in from with the RustDesk client (seed-demo).
  await expect(page.getByRole("row", { name: /mbp-ivan/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /ws-design/ })).toBeVisible();
  await expect(page.getByRole("row", { name: /build-01/ })).toHaveCount(0);
  await page.getByRole("link", { name: "My address books" }).click();
  await page.getByRole("button", { name: /ivan|Personal/ }).first().click();
  await expect(page.getByRole("row", { name: /Old MacBook/ })).toBeVisible();
  await page.goto("/server");
  await expect(page.getByText("Access denied")).toBeVisible();
  await page.goto("/my/sessions");
  await expect(page.getByText("RustDesk client").first()).toBeVisible();
});

test("groups: user and device groups", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/groups");
  await expect(page.getByRole("cell", { name: /Engineering/ })).toBeVisible();
  await page.goto("/groups?tab=device");
  await expect(page.getByRole("cell", { name: /Servers/ })).toBeVisible();
  await page.getByRole("button", { name: "New device group" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(`Lab ${RUN}`);
  await dialog.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("cell", { name: new RegExp(`Lab ${RUN}`) })).toBeVisible();
});

test("identity provider: create and failing test", async ({ page }) => {
  await loginAdmin(page);
  await page.goto("/identity-providers");
  await page.getByRole("button", { name: "Add provider" }).first().click();
  const dialog = page.getByRole("dialog");
  const label = `E2E SSO ${RUN}`;
  await dialog.getByLabel("Slug").fill(`e2e-${RUN}`);
  await dialog.getByLabel("Button label").fill(label);
  await dialog.getByLabel("Issuer URL").fill("https://sso.invalid/realms/e2e");
  await dialog.getByLabel("Client ID").fill("rdpanel");
  await dialog.getByLabel("Client secret").fill("not-a-real-secret");
  await dialog.getByRole("button", { name: "Create" }).click();
  await toast(page, "Saved");
  const card = page.locator(".rounded-xl, [class*='card']").filter({ hasText: label }).filter({ has: page.getByRole("button", { name: "Test" }) }).last();
  await expect(card).toBeVisible();
  await expect(page.getByText("http://localhost:31114/api/oidc/callback").or(page.getByText(/\/api\/oidc\/callback$/)).first()).toBeVisible();
  await card.getByRole("button", { name: "Test" }).click();
  await expect(card.getByRole("status")).toContainText(/discovery|lookup|no such host|failed/i);

  // Edit keeps the secret (PATCH without client_secret).
  await card.getByRole("button", { name: "Edit" }).click();
  await page.getByRole("dialog").getByLabel("Button label").fill(`${label} renamed`);
  await page.getByRole("dialog").getByRole("button", { name: "Save" }).click();
  await toast(page, "Saved");
  const renamed = page.locator(".rounded-xl, [class*='card']").filter({ hasText: `${label} renamed` }).filter({ has: page.getByRole("button", { name: "Test" }) }).last();
  await expect(renamed).toBeVisible();
  await expect(renamed.getByText("Secret set")).toBeVisible();

  // The login page offers it; starting the flow leaves for the IdP URL.
  await logout(page);
  await expect(page.getByRole("button", { name: `Continue with ${label} renamed` })).toBeVisible();
});
