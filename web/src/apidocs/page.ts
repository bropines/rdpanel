/**
 * Standalone Swagger UI page served by the backend at /api/v1/docs
 * (web/api-docs.html, a separate Vite entry: nothing here lands in the SPA bundle).
 */
import { apiDocs as enStrings } from "@/i18n/locales/en.json";
import { apiDocs as ruStrings } from "@/i18n/locales/ru.json";
import { applyTheme, readStoredTheme } from "@/lib/theme";

export type DocsStrings = typeof enStrings;

/** Panel language: the stored choice (i18next key), else the browser language. */
export function docsLanguage(stored: string | null, navigatorLang: string): "en" | "ru" {
  const lang = (stored || navigatorLang || "en").toLowerCase();
  return lang.startsWith("ru") ? "ru" : "en";
}

export function docsStrings(lang: "en" | "ru"): DocsStrings {
  return lang === "ru" ? ruStrings : enStrings;
}

export function readCookie(name: string, cookie: string = document.cookie): string | null {
  for (const part of cookie.split(";")) {
    const [k, ...v] = part.trim().split("=");
    if (k === name) return decodeURIComponent(v.join("="));
  }
  return null;
}

interface SwaggerRequest {
  method?: string;
  headers: Record<string, string>;
  [k: string]: unknown;
}

/** Adds the double-submit CSRF header (rdpanel_csrf cookie) to non-GET "Try it out" calls. */
export function csrfInterceptor(req: SwaggerRequest, cookie?: string): SwaggerRequest {
  const method = (req.method ?? "GET").toUpperCase();
  if (method === "GET" || method === "HEAD" || method === "OPTIONS") return req;
  const token = readCookie("rdpanel_csrf", cookie);
  if (token) req.headers = { ...req.headers, "X-CSRF-Token": token };
  return req;
}

type Spec = { components?: { parameters?: Record<string, { in?: string; name?: string; required?: boolean; description?: string }> } };

/**
 * The contract marks X-CSRF-Token as a required header; the interceptor fills it, so
 * make it optional here or Swagger UI refuses to execute until it is typed in.
 */
export function relaxCsrfParam(spec: Spec, note: string): Spec {
  for (const p of Object.values(spec.components?.parameters ?? {})) {
    if (p.in === "header" && p.name?.toLowerCase() === "x-csrf-token") {
      p.required = false;
      p.description = note;
    }
  }
  return spec;
}

export type SwaggerUIFactory = (opts: Record<string, unknown>) => unknown;

function safeGet(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** Fills the page chrome, applies the panel theme and mounts Swagger UI. */
export async function mountApiDocs(SwaggerUI: SwaggerUIFactory, doc: Document = document) {
  applyTheme(readStoredTheme({ getItem: safeGet }));
  const lang = docsLanguage(safeGet("rdpanel-lang"), navigator.language);
  const s = docsStrings(lang);
  doc.documentElement.lang = lang;
  doc.title = `${s.title} · RD Panel`;
  const back = doc.getElementById("apidocs-back");
  if (back) back.textContent = `← ${s.back}`;
  const title = doc.getElementById("apidocs-title");
  if (title) title.textContent = s.title;
  const hint = doc.getElementById("apidocs-hint");
  if (hint) hint.textContent = s.pageHint;

  // Make sure the CSRF cookie exists before the first "Try it out" (signed-in users).
  if (!readCookie("rdpanel_csrf")) void fetch("admin/auth/csrf", { credentials: "same-origin" }).catch(() => undefined);

  const root = doc.getElementById("apidocs-root");
  // Relative: /api/v1/docs → /api/v1/openapi.json, whatever host serves it.
  let spec: Spec;
  try {
    const res = await fetch("openapi.json", { credentials: "same-origin", headers: { Accept: "application/json" } });
    if (!res.ok) throw new Error(String(res.status));
    spec = (await res.json()) as Spec;
  } catch {
    if (root) root.textContent = s.loadFailed;
    return undefined;
  }
  return SwaggerUI({
    domNode: root,
    spec: relaxCsrfParam(spec, s.csrfAuto),
    deepLinking: true,
    // Never call the public validator (offline, strict CSP).
    validatorUrl: null,
    withCredentials: true,
    persistAuthorization: false,
    displayRequestDuration: true,
    tryItOutEnabled: false,
    filter: true,
    requestInterceptor: (req: SwaggerRequest) => csrfInterceptor(req),
  });
}
