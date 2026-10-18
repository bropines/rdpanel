import type { components, operations } from "@/api/schema";

/** Panel API base path (see api/openapi.yaml `servers`). */
export const API_BASE = "/api/v1/admin";
/** SSE endpoint (outside the admin base path). */
export const EVENTS_URL = "/api/v1/events";
/** CSRF double-submit cookie set by the server (readable by JS by design). */
export const CSRF_COOKIE = "rdpanel_csrf";
/** Header that must echo the CSRF cookie on every non-GET request. */
export const CSRF_HEADER = "X-CSRF-Token";

export type Schemas = components["schemas"];
export type Operations = operations;
export type ErrorBody = Schemas["Error"];

/** Query parameters of an operation (from the generated OpenAPI types). */
export type QueryOf<K extends keyof operations> = operations[K] extends { parameters: { query?: infer Q } }
  ? NonNullable<Q>
  : never;

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

export type QueryValue = string | number | boolean | undefined | null | readonly (string | number)[];
export type Query = Record<string, QueryValue>;

/** Serialises a query object: undefined/null/"" are dropped, arrays repeat the key. */
export function buildQuery(query: Query | undefined): string {
  if (!query) return "";
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === "") continue;
    if (Array.isArray(v)) {
      for (const item of v) qs.append(k, String(item));
    } else {
      qs.set(k, String(v));
    }
  }
  return qs.toString();
}

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  /** JSON-serialised request body. */
  body?: unknown;
  /** Raw body (e.g. an uploaded image) sent with `contentType`. */
  rawBody?: Blob;
  contentType?: string;
  query?: Query;
  signal?: AbortSignal;
}

export interface ClientDeps {
  fetch: typeof fetch;
  getCookie: (name: string) => string | undefined;
  base: string;
}

/** Paths whose 401 is an expected answer (login flow), not an expired session. */
const AUTH_FLOW = /^\/auth\/(login|mfa|me|csrf|providers)/;

type UnauthorizedHandler = () => void;
let onUnauthorized: UnauthorizedHandler | undefined;

/** Registers the global handler for "session expired" 401s (redirect to /login). */
export function setUnauthorizedHandler(fn: UnauthorizedHandler | undefined) {
  onUnauthorized = fn;
}

/**
 * Creates the typed fetch wrapper. Same-origin cookies are always sent; unsafe
 * methods carry the CSRF header, fetching a token first when the cookie is absent.
 */
export function createClient(deps: Partial<ClientDeps> = {}) {
  const doFetch = deps.fetch ?? ((...args: Parameters<typeof fetch>) => fetch(...args));
  const getCookie = deps.getCookie ?? ((name: string) => readCookie(name));
  const base = deps.base ?? API_BASE;

  /** Returns the CSRF token; `refresh` ignores the cookie and asks the server for a new one. */
  async function csrfToken(refresh = false): Promise<string> {
    const fromCookie = refresh ? undefined : getCookie(CSRF_COOKIE);
    if (fromCookie) return fromCookie;
    const res = await request<Schemas["CsrfToken"]>("/auth/csrf");
    return res.csrf_token;
  }

  function url(path: string, query?: Query): string {
    let u = base + path;
    const s = buildQuery(query);
    if (s) u += (u.includes("?") ? "&" : "?") + s;
    return u;
  }

  async function send(
    path: string,
    opts: RequestOptions,
    accept: string,
    refreshCsrf = false,
  ): Promise<Response> {
    const method = opts.method ?? "GET";
    const headers: Record<string, string> = { Accept: accept };
    let body: BodyInit | undefined;
    if (opts.rawBody !== undefined) {
      headers["Content-Type"] = opts.contentType ?? (opts.rawBody.type || "application/octet-stream");
      body = opts.rawBody;
    } else if (opts.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.body);
    }
    if (!SAFE_METHODS.has(method)) {
      headers[CSRF_HEADER] = await csrfToken(refreshCsrf);
    }
    return doFetch(url(path, opts.query), {
      method,
      headers,
      body,
      credentials: "same-origin",
      ...(opts.signal ? { signal: opts.signal } : {}),
    });
  }

  async function fail(res: Response, path: string, data: unknown): Promise<never> {
    const err = (data as Partial<ErrorBody> | undefined)?.error;
    if (res.status === 401 && !AUTH_FLOW.test(path)) onUnauthorized?.();
    throw new ApiError(res.status, err?.code ?? `http_${res.status}`, err?.message ?? res.statusText, err?.fields);
  }

  async function request<T>(path: string, opts: RequestOptions = {}, csrfRetried = false): Promise<T> {
    const res = await send(path, opts, "application/json", csrfRetried);
    if (res.status === 204 || res.status === 202) return undefined as T;

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
      // A stale CSRF cookie (key rotation, another deployment on the same host)
      // is rejected once; fetch a fresh token and retry the request a single time.
      const code = (data as Partial<ErrorBody> | undefined)?.error?.code;
      const method = opts.method ?? "GET";
      if (res.status === 403 && code === "csrf_failed" && !csrfRetried && !SAFE_METHODS.has(method)) {
        return request<T>(path, opts, true);
      }
      return fail(res, path, data);
    }
    return data as T;
  }

  /** GET a non-JSON resource (CSV export): returns the blob and the suggested filename. */
  async function download(path: string, query?: Query): Promise<{ blob: Blob; filename: string | undefined }> {
    const res = await send(path, { query }, "*/*");
    if (!res.ok) {
      let data: unknown;
      try {
        data = JSON.parse(await res.text());
      } catch {
        data = undefined;
      }
      return fail(res, path, data);
    }
    const cd = res.headers.get("Content-Disposition") ?? "";
    const m = /filename="?([^";]+)"?/i.exec(cd);
    return { blob: await res.blob(), filename: m?.[1] };
  }

  return { request, download, csrfToken, url };
}

export type ApiClient = ReturnType<typeof createClient>;

/** Shared client instance for the app. */
export const api = createClient();

/** Triggers a browser download of a blob. */
export function saveBlob(blob: Blob, filename: string) {
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(href), 1000);
}
