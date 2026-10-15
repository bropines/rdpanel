import "@/index.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { ApiError } from "@/api/client";
import { initI18n } from "@/i18n";
import { applyTheme, readStoredTheme } from "@/lib/theme";
import { ThemeProvider } from "@/lib/theme-provider";
import { router } from "@/router";

// Apply the theme before the first paint (no inline script: CSP is script-src 'self').
applyTheme(readStoredTheme());
initI18n();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      // Never retry auth/permission/validation failures.
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
});

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
