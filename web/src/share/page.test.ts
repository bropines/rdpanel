import { fetchShareInfo, type SharePublicInfo } from "@/api/share";
import { customTitle, platformDownloads, tokenFromLocation } from "@/share/page";

const TOKEN = "demo-share-link-token0";

const info: SharePublicInfo = {
  title: "  ",
  description: "",
  id_server: "desk.example.com",
  relay_server: "desk.example.com",
  api_server: "https://desk.example.com",
  public_key: "KEY",
  config_string: "CFG",
  windows_file_name: "rustdesk-licensed-CFG.exe",
  windows: [
    { arch: "x64", size: 10 },
    { arch: "arm64", size: 9 },
  ],
  assets: [
    { id: "macos-arm64", name: "a.dmg", size: 5, url: "https://github.com/a.dmg" },
    { id: "linux-deb-x64", name: "a.deb", size: 4, url: "https://github.com/a.deb" },
    { id: "android-universal", name: "a.apk", size: 3, url: "https://github.com/a.apk" },
  ],
};

describe("share page helpers", () => {
  it("reads the token from the path", () => {
    expect(tokenFromLocation(`/s/${TOKEN}`)).toBe(TOKEN);
    expect(tokenFromLocation(`/s/${TOKEN}/`)).toBe(TOKEN);
    expect(tokenFromLocation("/share.html", `?token=${TOKEN}`)).toBe(TOKEN);
    expect(tokenFromLocation("/s/short")).toBeNull();
    expect(tokenFromLocation(`/s/${TOKEN}/info`)).toBeNull();
    expect(tokenFromLocation("/s/../../etc/passwd/xxxxxx")).toBeNull();
    expect(tokenFromLocation("/s/" + "a".repeat(21) + "!")).toBeNull();
  });

  it("offers the preconfigured Windows installer from the panel", () => {
    const win = platformDownloads(info, "windows", TOKEN);
    expect(win.map((d) => d.href)).toEqual([`/s/${TOKEN}/windows/x64`, `/s/${TOKEN}/windows/arm64`]);
    expect(win[0]).toMatchObject({ label: "share.public.windows.download", preconfigured: true, size: 10 });
    expect(platformDownloads({ ...info, windows_file_name: undefined }, "windows", TOKEN)).toEqual([]);
  });

  it("links the official packages for the other platforms", () => {
    expect(platformDownloads(info, "macos", TOKEN)).toEqual([
      { label: "install.macos.arm", literal: false, href: "https://github.com/a.dmg", size: 5, external: true },
    ]);
    expect(platformDownloads(info, "linux", TOKEN).map((d) => d.label)).toEqual([".deb"]);
    expect(platformDownloads(info, "android", TOKEN)[0]?.href).toBe("https://github.com/a.apk");
    expect(platformDownloads(info, "ios", TOKEN)[0]?.href).toContain("apps.apple.com");
    expect(platformDownloads({ ...info, assets: [] }, "macos", TOKEN)).toEqual([]);
  });

  it("uses the admin's title or the default", () => {
    expect(customTitle(info)).toBeNull();
    expect(customTitle({ title: " Acme " })).toBe("Acme");
  });
});

describe("fetchShareInfo", () => {
  const respond = (status: number, body: unknown = {}) =>
    vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));

  it("loads the page data without credentials", async () => {
    const f = respond(200, info);
    expect(await fetchShareInfo(TOKEN, f)).toEqual({ kind: "ok", info });
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(`/s/${TOKEN}/info`);
    expect(init.credentials).toBe("omit");
  });

  it("maps invalid links, rate limits and failures", async () => {
    expect(await fetchShareInfo(TOKEN, respond(404))).toEqual({ kind: "invalid" });
    expect(await fetchShareInfo(TOKEN, respond(410))).toEqual({ kind: "invalid" });
    expect(await fetchShareInfo(TOKEN, respond(429))).toEqual({ kind: "rate_limited" });
    expect(await fetchShareInfo(TOKEN, respond(500))).toEqual({ kind: "error" });
    expect(await fetchShareInfo(TOKEN, vi.fn(async () => Promise.reject(new Error("offline"))))).toEqual({ kind: "error" });
  });
});
