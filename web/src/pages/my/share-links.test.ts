import type { ShareLink } from "@/api/share";
import { buildShareInput, defaultShareForm, downloadsText, parseLocalInput, sortLinks, toLocalInput } from "./share-links";

const now = new Date(2026, 9, 21, 12, 0, 0);

describe("share link form", () => {
  it("builds permanent and preset links", () => {
    const f = defaultShareForm(now);
    expect(f.expiry).toBe("permanent");
    expect(buildShareInput(f, now)).toEqual({ ok: true, body: {} });
    expect(buildShareInput({ ...f, expiry: "24h", label: "  Front desk ", maxDownloads: " 5 " }, now)).toEqual({
      ok: true,
      body: { expires_in: "24h", label: "Front desk", max_downloads: 5 },
    });
  });

  it("converts a custom local date to UTC", () => {
    const at = new Date(2026, 9, 25, 18, 30);
    const r = buildShareInput({ ...defaultShareForm(now), expiry: "custom", customDate: toLocalInput(at) }, now);
    expect(r).toEqual({ ok: true, body: { expires_at: at.toISOString() } });
    expect(toLocalInput(at)).toBe("2026-10-25T18:30");
    expect(parseLocalInput("2026-10-25T18:30")?.getTime()).toBe(at.getTime());
    expect(parseLocalInput("2026-02-30T10:00")).toBeNull();
    expect(parseLocalInput("tomorrow")).toBeNull();
  });

  it("rejects what the server would reject", () => {
    const f = defaultShareForm(now);
    const past = toLocalInput(new Date(now.getTime() - 60_000));
    const far = toLocalInput(new Date(now.getTime() + 400 * 24 * 3600_000));
    expect(buildShareInput({ ...f, expiry: "custom", customDate: past }, now)).toMatchObject({ ok: false, field: "customDate" });
    expect(buildShareInput({ ...f, expiry: "custom", customDate: far }, now)).toMatchObject({ ok: false, field: "customDate" });
    expect(buildShareInput({ ...f, expiry: "custom", customDate: "" }, now)).toMatchObject({ ok: false, field: "customDate" });
    for (const bad of ["0", "-1", "1.5", "abc", "100001"]) {
      expect(buildShareInput({ ...f, maxDownloads: bad }, now)).toMatchObject({ ok: false, field: "maxDownloads" });
    }
    expect(buildShareInput({ ...f, label: "x".repeat(65) }, now)).toMatchObject({ ok: false, field: "label" });
    expect(buildShareInput({ ...f, label: "ж".repeat(64) }, now).ok).toBe(true);
  });
});

describe("share link list", () => {
  const link = (id: number, status: ShareLink["status"], created: string, extra: Partial<ShareLink> = {}): ShareLink => ({
    id,
    label: "",
    url: null,
    status,
    expires_at: null,
    max_downloads: null,
    views: 0,
    downloads: 0,
    last_used_at: null,
    created_at: created,
    owner: { id: 1, username: "admin", display_name: "" },
    ...extra,
  });

  it("shows downloads with their limit", () => {
    expect(downloadsText({ downloads: 3, max_downloads: null })).toBe("3");
    expect(downloadsText({ downloads: 3, max_downloads: 10 })).toBe("3 / 10");
  });

  it("lists active links first, newest first", () => {
    const sorted = sortLinks([
      link(1, "expired", "2026-10-20T10:00:00Z"),
      link(2, "active", "2026-10-01T10:00:00Z"),
      link(3, "active", "2026-10-10T10:00:00Z"),
      link(4, "exhausted", "2026-10-21T10:00:00Z"),
    ]);
    expect(sorted.map((l) => l.id)).toEqual([3, 2, 4, 1]);
  });
});
