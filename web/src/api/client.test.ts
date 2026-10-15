import { describe, expect, it, vi } from "vitest";

import { ApiError, createClient, CSRF_HEADER, readCookie } from "@/api/client";

function jsonResponse(status: number, body?: unknown): Response {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("readCookie", () => {
  it("finds a cookie among others and decodes it", () => {
    expect(readCookie("rdpanel_csrf", "a=1; rdpanel_csrf=abc%3D%3D; b=2")).toBe("abc==");
    expect(readCookie("missing", "a=1")).toBeUndefined();
  });
});

describe("createClient", () => {
  it("sends GET without CSRF header and parses JSON", async () => {
    const fetch = vi.fn().mockResolvedValue(jsonResponse(200, { ok: true }));
    const client = createClient({ fetch, getCookie: () => "tok", base: "/b" });
    await expect(client.request("/x", { query: { a: 1, skip: undefined } })).resolves.toEqual({ ok: true });
    const [url, init] = fetch.mock.calls[0]!;
    expect(url).toBe("/b/x?a=1");
    expect(init.headers[CSRF_HEADER]).toBeUndefined();
    expect(init.credentials).toBe("same-origin");
  });

  it("adds the CSRF header from the cookie on unsafe methods", async () => {
    const fetch = vi.fn().mockResolvedValue(jsonResponse(204));
    const client = createClient({ fetch, getCookie: () => "cookie-token", base: "" });
    await client.request("/auth/logout", { method: "POST" });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls[0]![1].headers[CSRF_HEADER]).toBe("cookie-token");
  });

  it("fetches a CSRF token when the cookie is missing", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(200, { csrf_token: "fresh" }))
      .mockResolvedValueOnce(jsonResponse(200, { user: {} }));
    const client = createClient({ fetch, getCookie: () => undefined, base: "" });
    await client.request("/auth/login", { method: "POST", body: { username: "a", password: "b" } });
    expect(fetch.mock.calls[0]![0]).toBe("/auth/csrf");
    const init = fetch.mock.calls[1]![1];
    expect(init.headers[CSRF_HEADER]).toBe("fresh");
    expect(init.headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body)).toEqual({ username: "a", password: "b" });
  });

  it("refreshes a stale CSRF cookie once and retries the request", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(403, { error: { code: "csrf_failed", message: "bad token" } }))
      .mockResolvedValueOnce(jsonResponse(200, { csrf_token: "fresh" }))
      .mockResolvedValueOnce(jsonResponse(200, { ok: true }));
    const client = createClient({ fetch, getCookie: () => "stale", base: "" });
    await expect(client.request("/auth/login", { method: "POST", body: { username: "a" } })).resolves.toEqual({
      ok: true,
    });
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(fetch.mock.calls[0]![1].headers[CSRF_HEADER]).toBe("stale");
    expect(fetch.mock.calls[1]![0]).toBe("/auth/csrf");
    expect(fetch.mock.calls[2]![1].headers[CSRF_HEADER]).toBe("fresh");
  });

  it("does not retry a CSRF failure twice", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse(403, { error: { code: "csrf_failed", message: "bad" } }))
      .mockResolvedValueOnce(jsonResponse(200, { csrf_token: "fresh" }))
      .mockResolvedValueOnce(jsonResponse(403, { error: { code: "csrf_failed", message: "still bad" } }));
    const client = createClient({ fetch, getCookie: () => "stale", base: "" });
    await expect(client.request("/x", { method: "POST" })).rejects.toMatchObject({ code: "csrf_failed" });
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it("maps the error envelope to ApiError", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(jsonResponse(401, { error: { code: "totp_required", message: "enter your code" } }));
    const client = createClient({ fetch, getCookie: () => "t", base: "" });
    const err = await client.request("/auth/login", { method: "POST" }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, code: "totp_required", message: "enter your code" });
  });

  it("falls back to http_<status> for non-JSON errors", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("boom", { status: 502, statusText: "Bad Gateway" }));
    const client = createClient({ fetch, getCookie: () => "t", base: "" });
    await expect(client.request("/x")).rejects.toMatchObject({ status: 502, code: "http_502" });
  });
});
