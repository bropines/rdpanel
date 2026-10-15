/**
 * Starts the Mock Service Worker. Only imported when `import.meta.env.VITE_MOCK === "1"`
 * (`npm run dev:mock` / `npm run build:mock`); production bundles never include it.
 */
import { setupWorker } from "msw/browser";

import { handlers } from "@/mocks/handlers";

export async function startMocks() {
  // The real server sets this cookie; the client echoes it in X-CSRF-Token.
  document.cookie = "rdpanel_csrf=mock-csrf; path=/; SameSite=Lax";
  const worker = setupWorker(...handlers);
  await worker.start({
    onUnhandledRequest: "bypass",
    quiet: true,
    serviceWorker: { url: "/mockServiceWorker.js" },
  });
}
