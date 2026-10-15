/**
 * Integration tests: real router + query client + MSW handlers (the same mock API
 * used by `npm run dev:mock`) running in Node.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, RouterProvider } from "@tanstack/react-router";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import { setUnauthorizedHandler } from "@/api/client";
import { initI18n } from "@/i18n";
import { loginRedirect } from "@/lib/guards";
import { createQueryClient } from "@/lib/query";
import { ThemeProvider } from "@/lib/theme-provider";
import { handlers } from "@/mocks/handlers";
import { createAppRouter } from "@/router";

const server = setupServer(...handlers);

vi.setConfig({ testTimeout: 20_000 });

beforeAll(() => {
  localStorage.setItem("rdpanel-lang", "en");
  initI18n();
  document.cookie = "rdpanel_csrf=mock-csrf; path=/";
  server.listen({ onUnhandledRequest: "error" });
  // Node's fetch rejects relative URLs; resolve them against the jsdom origin.
  const patched = globalThis.fetch;
  vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) =>
    patched(typeof input === "string" && input.startsWith("/") ? new URL(input, window.location.origin) : input, init),
  );
});
afterEach(() => server.resetHandlers());
afterAll(() => {
  server.close();
  vi.unstubAllGlobals();
});

function loginAs(userId: number | null) {
  localStorage.setItem("rdpanel-mock-user", userId === null ? "" : String(userId));
}

function renderApp(path: string) {
  const qc = createQueryClient();
  qc.setDefaultOptions({ queries: { retry: false, staleTime: 0 } });
  const router = createAppRouter(qc, createMemoryHistory({ initialEntries: [path] }));
  setUnauthorizedHandler(() => {
    qc.clear();
    void router.navigate(loginRedirect(router.state.location.href).options as Parameters<typeof router.navigate>[0]);
  });
  render(
    <ThemeProvider>
      <QueryClientProvider client={qc}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ThemeProvider>,
  );
  return router;
}

describe("auth guard", () => {
  beforeEach(() => loginAs(1));

  it("sends anonymous visitors to /login and keeps a relative next", async () => {
    loginAs(null);
    const router = renderApp("/devices?online=true");
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).toEqual({ next: "/devices?online=true" });
    // Identity providers from /auth/providers are offered.
    expect(await screen.findByRole("link", { name: /Continue with GitHub/ })).toHaveAttribute(
      "href",
      expect.stringContaining("/auth/oidc/github/start?next=%2Fdevices%3Fonline%3Dtrue"),
    );
  });

  it("signs in with password + TOTP and returns to next", async () => {
    loginAs(null);
    const user = userEvent.setup();
    const router = renderApp("/login?next=%2Fusers");
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "admin");
    await user.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByText("Two-factor authentication")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Authentication code"), "000000");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The code is wrong or expired");

    await user.type(screen.getByLabelText("Authentication code"), "123456");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/users"));
    expect(await screen.findByText("Maria Kuznetsova")).toBeInTheDocument();
  });

  it("shows a validation error on wrong credentials", async () => {
    loginAs(null);
    const user = userEvent.setup();
    renderApp("/login");
    await user.type(await screen.findByLabelText("Username"), "admin");
    await user.type(screen.getByLabelText("Password"), "wrong");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid username or password");
  });

  it("never follows an absolute next after login", async () => {
    loginAs(null);
    const user = userEvent.setup();
    const router = renderApp("/login?next=%2F%2Fevil.example%2F");
    await user.type(await screen.findByLabelText("Username"), "ivan");
    await user.type(screen.getByLabelText("Password"), "ivan");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await user.type(await screen.findByLabelText("Authentication code"), "123456");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/"));
    expect(router.state.location.href).toBe("/");
  });

  it("forces 2FA enrolment when the account requires it", async () => {
    loginAs(null);
    const user = userEvent.setup();
    const router = renderApp("/login?next=%2Fmy%2Fdevices");
    await user.type(await screen.findByLabelText("Username"), "maria");
    await user.type(screen.getByLabelText("Password"), "maria");
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/my/profile"));
    expect(await screen.findByText(/requires two-factor authentication/)).toBeInTheDocument();
  });

  it("keeps non-admins out of System pages and hides the System nav", async () => {
    loginAs(2);
    const router = renderApp("/users");
    expect(await screen.findByText("Access denied")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/forbidden");
    expect(screen.queryByRole("link", { name: "Users" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "My devices" })).toBeInTheDocument();
  });

  it("redirects to /login when the session expires mid-use", async () => {
    const router = renderApp("/devices");
    expect(await screen.findByRole("table", undefined, { timeout: 5000 })).toBeInTheDocument();
    loginAs(null);
    act(() => {
      void router.navigate({ to: "/users" });
    });
    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
  });
});

describe("devices page", () => {
  beforeEach(() => loginAs(1));

  it("reflects table filters in the URL and the API query", async () => {
    const user = userEvent.setup();
    const seen: string[] = [];
    server.events.on("request:start", ({ request }) => {
      if (new URL(request.url).pathname === "/api/v1/admin/devices") seen.push(new URL(request.url).search);
    });
    const router = renderApp("/devices?online=false&page_size=25");
    expect(await screen.findByRole("table", undefined, { timeout: 5000 })).toBeInTheDocument();
    await waitFor(() => expect(seen.some((s) => s.includes("online=false") && s.includes("page_size=25"))).toBe(true));

    await user.type(screen.getByRole("searchbox"), "srv");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ q: "srv", online: false, page_size: 25 }));
    await waitFor(() => expect(seen.some((s) => s.includes("q=srv"))).toBe(true));
    server.events.removeAllListeners();
  });
});
