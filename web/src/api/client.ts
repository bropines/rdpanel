import type { components } from "@/api/schema";

/** Panel API base path (see api/openapi.yaml `servers`). */
export const API_BASE = "/api/v1/admin";
/** CSRF double-submit cookie set by the server (readable by JS by design). */
export const CSRF_COOKIE = "rdpanel_csrf";
/** Header that must echo the CSRF cookie on every non-GET request. */
export const CSRF_HEADER = "X-CSRF-Token";

export type Schemas = components["schemas"];
export type ErrorBody = Schemas["Error"];

/** Error thrown for every non-2xx response; mirrors `{"error":{"code","message"}}`. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string> | undefined;

  constructor(status: number, code: string, message: string, fields?: Record<string, string>) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/** Reads a cookie value by name from a `document.cookie` string. */
export function readCookie(name: string, cookieString: string = document.cookie): string | undefined {
  for (const part of cookieString.split(";")) {
    const [k, ...rest] = part.trim().split("=");
    if (k === name) return decodeURIComponent(rest.join("="));
  }
  return undefined;
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  /** JSON-serialised request body. */
  body?: unknown;
  query?: Record<string, string | number | boolean | undefined>;
  signal?: AbortSignal;
}

export interface ClientDeps {
  fetch: typeof fetch;
  getCookie: (name: string) => string | undefined;
  base: string;
}

/**
 * Creates the typed fetch wrapper. Same-origin cookies are always sent; unsafe
 * methods carry the CSRF header, fetching a token first when the cookie is absent.
 */
export function createClient(deps: Partial<ClientDeps> = {}) {
  const doFetch = deps.fetch ?? ((...args: Parameters<typeof fetch>) => fetch(...args));
  const getCookie = deps.getCookie ?? ((name: string) => readCookie(name));
  const base = deps.base ?? API_BASE;

  async function csrfToken(): Promise<string> {
    const fromCookie = getCookie(CSRF_COOKIE);
    if (fromCookie) return fromCookie;
    const res = await request<Schemas["CsrfToken"]>("/auth/csrf");
    return res.csrf_token;
  }

  async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const method = opts.method ?? "GET";
    const headers: Record<string, string> = { Accept: "application/json" };
    let body: string | undefined;
    if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
    if (!SAFE_METHODS.has(method)) {
      headers[CSRF_HEADER] = await csrfToken();
    }
    let url = base + path;
    if (opts.query) {
      const qs = new URLSearchParams();
      for (const [k, v] of Object.entries(opts.query)) {
        if (v !== undefined) qs.set(k, String(v));
      }
      const s = qs.toString();
      if (s) url += (url.includes("?") ? "&" : "?") + s;
    }

    const res = await doFetch(url, {
      method,
      headers,
      body,
      credentials: "same-origin",
      signal: opts.signal ?? null,
    });
    if (res.status === 204) return undefined as T;

    const text = await res.text();
    let data: unknown = undefined;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        if (res.ok) throw new ApiError(res.status, "invalid_response", "invalid JSON from server");
      }
    }
    if (!res.ok) {
      const err = (data as Partial<ErrorBody> | undefined)?.error;
      throw new ApiError(res.status, err?.code ?? `http_${res.status}`, err?.message ?? res.statusText, err?.fields);
    }
    return data as T;
  }

  return { request, csrfToken };
}

export type ApiClient = ReturnType<typeof createClient>;

/** Shared client instance for the app. */
export const api = createClient();
