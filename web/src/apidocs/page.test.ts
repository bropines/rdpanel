import { afterEach, describe, expect, it, vi } from "vitest";

import { csrfInterceptor, docsLanguage, mountApiDocs, readCookie, relaxCsrfParam } from "./page";

const sources = import.meta.glob<string>(["/src/**/*.{ts,tsx}", "!/src/apidocs/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

afterEach(() => {
  document.body.innerHTML = "";
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("api docs page", () => {
  it("keeps Swagger UI out of the SPA bundle (only the api-docs entry imports it)", () => {
    expect(Object.keys(sources).length).toBeGreaterThan(20);
    const offenders = Object.entries(sources)
      .filter(([, src]) => src.includes("swagger-ui"))
      .map(([p]) => p);
    expect(offenders).toEqual([]);
  });

  it("mounts Swagger UI with the fetched spec and the panel chrome", async () => {
    document.body.innerHTML = `<a id="apidocs-back" href="/"></a><strong id="apidocs-title"></strong><span id="apidocs-hint"></span><main id="apidocs-root"></main>`;
    localStorage.setItem("rdpanel-lang", "ru");
    localStorage.setItem("rdpanel-theme", "dark");
    const spec = { openapi: "3.1.0", components: { parameters: { CsrfHeader: { name: "X-CSRF-Token", in: "header", required: true } } } };
    const fetchMock = vi.fn((url: string) => Promise.resolve(new Response(url === "openapi.json" ? JSON.stringify(spec) : "{}")));
    vi.stubGlobal("fetch", fetchMock);
    const ui = vi.fn(() => ({}));

    await mountApiDocs(ui);

    expect(ui).toHaveBeenCalledTimes(1);
    const opts = (ui.mock.calls[0] as unknown[])[0] as Record<string, unknown>;
    expect(opts.spec).toMatchObject({ components: { parameters: { CsrfHeader: { required: false } } } });
    expect(opts.validatorUrl).toBeNull();
    expect(opts.domNode).toBe(document.getElementById("apidocs-root"));
    expect(typeof opts.requestInterceptor).toBe("function");
    expect(document.getElementById("apidocs-title")?.textContent).toBe("Документация API");
    expect(document.documentElement.lang).toBe("ru");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith("admin/auth/csrf", { credentials: "same-origin" });
  });

  it("shows an error instead of Swagger UI when the spec cannot be loaded", async () => {
    document.body.innerHTML = `<main id="apidocs-root"></main>`;
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("", { status: 401 }))));
    const ui = vi.fn();
    await mountApiDocs(ui);
    expect(ui).not.toHaveBeenCalled();
    expect(document.getElementById("apidocs-root")?.textContent).toContain("/api/v1/openapi.json");
  });

  it("relaxes only the CSRF header parameter", () => {
    const spec = relaxCsrfParam(
      { components: { parameters: { C: { name: "X-CSRF-Token", in: "header", required: true }, P: { name: "page", in: "query", required: true } } } },
      "auto",
    );
    expect(spec.components?.parameters?.C).toMatchObject({ required: false, description: "auto" });
    expect(spec.components?.parameters?.P?.required).toBe(true);
  });

  it("adds X-CSRF-Token from the rdpanel_csrf cookie to non-GET requests only", () => {
    const cookie = "a=1; rdpanel_csrf=tok%3D1; b=2";
    expect(readCookie("rdpanel_csrf", cookie)).toBe("tok=1");
    expect(csrfInterceptor({ method: "POST", headers: { Accept: "*/*" } }, cookie).headers).toEqual({
      Accept: "*/*",
      "X-CSRF-Token": "tok=1",
    });
    expect(csrfInterceptor({ method: "get", headers: {} }, cookie).headers).toEqual({});
    expect(csrfInterceptor({ method: "DELETE", headers: {} }, "x=1").headers).toEqual({});
  });

  it("picks the panel language", () => {
    expect(docsLanguage(null, "ru-RU")).toBe("ru");
    expect(docsLanguage("en", "ru-RU")).toBe("en");
    expect(docsLanguage(null, "de")).toBe("en");
  });
});
