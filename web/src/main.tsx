import "@/index.css";

import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { meQueryKey } from "@/api/auth";
import { setUnauthorizedHandler } from "@/api/client";
import { initI18n } from "@/i18n";
import { loginRedirect } from "@/lib/guards";
import { queryClient } from "@/lib/query";
import { applyTheme, readStoredTheme } from "@/lib/theme";
import { ThemeProvider } from "@/lib/theme-provider";
import { createAppRouter } from "@/router";

// Apply the theme before the first paint (no inline script: CSP is script-src 'self').
applyTheme(readStoredTheme());
initI18n();

const router = createAppRouter(queryClient);

// A 401 from any data endpoint means the session expired: drop cached data and go to /login.
setUnauthorizedHandler(() => {
  const { pathname, href } = router.state.location;
  if (pathname === "/login") return;
  queryClient.removeQueries({ queryKey: meQueryKey });
  queryClient.clear();
  void router.navigate(loginRedirect(href).options as Parameters<typeof router.navigate>[0]);
});

async function start() {
  // Mock Service Worker: only in `--mode mock` builds; the branch is removed from production bundles.
  if (import.meta.env.VITE_MOCK === "1") {
    const { startMocks } = await import("@/mocks/browser");
    await startMocks();
  }

  const root = document.getElementById("root");
  if (!root) throw new Error("#root missing");

  createRoot(root).render(
    <StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </ThemeProvider>
    </StrictMode>,
  );
}

void start();
