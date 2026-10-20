// Public share page (/s/<token>): served by the backend from web/share.html
// with its assets under /s/assets/, outside the signed-in panel.
import "@/index.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { TooltipProvider } from "@/components/ui/misc";
import { Toaster } from "@/components/ui/sonner";
import { initI18n } from "@/i18n";
import { applyTheme, readStoredTheme } from "@/lib/theme";
import { ThemeProvider } from "@/lib/theme-provider";
import { SharePage } from "@/share/SharePage";
import { tokenFromLocation } from "@/share/page";

// Before the first paint (no inline script: CSP is script-src 'self').
applyTheme(readStoredTheme());
initI18n();

async function start() {
  // Mock Service Worker: only in `--mode mock` builds; removed from production bundles.
  if (import.meta.env.VITE_MOCK === "1") {
    const { startMocks } = await import("@/mocks/browser");
    await startMocks();
  }
  const root = document.getElementById("root");
  if (!root) throw new Error("#root missing");
  createRoot(root).render(
    <StrictMode>
      <ThemeProvider>
        <TooltipProvider delayDuration={300}>
          <SharePage token={tokenFromLocation(window.location.pathname, window.location.search)} />
          <Toaster richColors closeButton />
        </TooltipProvider>
      </ThemeProvider>
    </StrictMode>,
  );
}

void start();
