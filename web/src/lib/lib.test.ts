import { describe, expect, it } from "vitest";

import { argbToCss, argbToHex, hexToArgb, readableTextColor } from "@/lib/argb";
import { formatDuration, formatRustdeskId, formatUptime, relativeTime } from "@/lib/format";
import { applyPresence, mergeDevice, patchDevicePage } from "@/lib/live-cache";
import { safeNext } from "@/lib/redirect";
import { rangeToQuery } from "@/lib/time-range";
import { isHostPort, isIpOrCidr, isIPv6, splitLines } from "@/lib/validate";
import type { Schemas } from "@/api/client";

describe("safeNext", () => {
  it("keeps same-origin relative paths with query and hash", () => {
    expect(safeNext("/devices?online=true#x")).toBe("/devices?online=true#x");
    expect(safeNext("/my/profile")).toBe("/my/profile");
  });

  it.each([
    "https://evil.example/",
    "//evil.example/x",
    "/\\evil.example",
    "javascript:alert(1)",
    "devices",
    "",
    "/login?next=/x",
    "/ok\u0000",
    42,
    undefined,
  ])("rejects %j", (v) => {
    expect(safeNext(v)).toBe("/");
  });

  it("uses the fallback", () => {
    expect(safeNext("//x", "/home")).toBe("/home");
  });
});

describe("argb", () => {
  it("converts Flutter ARGB to CSS and back", () => {
    expect(argbToHex(4288585374)).toBe("#9e9e9e");
    expect(argbToCss(0xff2196f3)).toBe("rgba(33, 150, 243, 1)");
    expect(argbToCss(0x802196f3)).toBe("rgba(33, 150, 243, 0.502)");
    expect(hexToArgb("#2196f3")).toBe(0xff2196f3);
    expect(hexToArgb("#fff")).toBe(0xffffffff);
    expect(hexToArgb("nope")).toBeUndefined();
  });

  it("treats alpha 0 (legacy 0x00RRGGBB) as opaque", () => {
    expect(argbToCss(0x00ff0000)).toBe("rgba(255, 0, 0, 1)");
  });

  it("picks a readable text color", () => {
    expect(readableTextColor(0xffffc107)).toBe("#000000");
    expect(readableTextColor(0xff3f51b5)).toBe("#ffffff");
  });
});

describe("format", () => {
  const now = Date.parse("2026-10-15T12:00:00Z");
  it("formats relative times", () => {
    expect(relativeTime("2026-10-15T11:57:00Z", "en", now)).toBe("3 minutes ago");
    expect(relativeTime("2026-10-15T11:59:58Z", "en", now)).toBe("now");
    expect(relativeTime(null, "en", now)).toBe("—");
    expect(relativeTime("2026-10-14T12:00:00Z", "ru", now)).toBe("вчера");
  });

  it("formats durations and ids", () => {
    expect(formatDuration("2026-10-15T10:00:00Z", "2026-10-15T11:02:00Z")).toBe("1h 02m");
    expect(formatDuration("2026-10-15T10:00:00Z", "2026-10-15T10:00:42Z")).toBe("42s");
    expect(formatUptime(42)).toBe("0m");
    expect(formatUptime(5 * 3600 + 12 * 60)).toBe("5h 12m");
    expect(formatUptime(3 * 86400 + 4 * 3600 + 59)).toBe("3d 04h");
    expect(formatRustdeskId("123456789")).toBe("123 456 789");
    expect(formatRustdeskId("my-id")).toBe("my-id");
  });
});

describe("validate", () => {
  it("accepts IPs and CIDRs", () => {
    for (const ok of ["1.2.3.4", "10.0.0.0/8", "::1", "2001:db8::/32", "::ffff:1.2.3.4", "fe80::1%".slice(0, -1)]) {
      expect(isIpOrCidr(ok), ok).toBe(true);
    }
    for (const bad of ["1.2.3", "256.1.1.1", "10.0.0.0/33", "2001:db8::/129", "1:2:3", "a.b.c.d", "1.2.3.4/8/1", ""]) {
      expect(isIpOrCidr(bad), bad).toBe(false);
    }
    expect(isIPv6("1:2:3:4:5:6:7:8")).toBe(true);
    expect(isIPv6("1::2::3")).toBe(false);
  });

  it("validates relay host[:port]", () => {
    expect(isHostPort("relay.example.com")).toBe(true);
    expect(isHostPort("relay.example.com:21117")).toBe(true);
    expect(isHostPort("[::1]:21117")).toBe(true);
    expect(isHostPort("a b")).toBe(false);
    expect(isHostPort("x:70000")).toBe(false);
    expect(isHostPort("a,b")).toBe(false);
  });

  it("splits textarea lines", () => {
    expect(splitLines(" a \n\nb, c\na")).toEqual(["a", "b", "c"]);
  });
});

describe("rangeToQuery", () => {
  const now = Date.parse("2026-10-15T12:00:00Z");
  it("resolves presets", () => {
    expect(rangeToQuery("24h", {}, now)).toEqual({ from: "2026-10-14T12:00:00.000Z" });
    expect(rangeToQuery(undefined, {}, now)).toEqual({ from: "2026-10-08T12:00:00.000Z" });
    expect(rangeToQuery("all", {}, now)).toEqual({});
  });
  it("uses custom bounds", () => {
    const q = rangeToQuery("custom", { from: "2026-10-01T00:00", to: "" }, now);
    expect(q.from).toMatch(/^2026-09-30T|^2026-10-01T/);
    expect(q.to).toBeUndefined();
  });
});

describe("live cache", () => {
  const dev = { id: 7, rustdesk_id: "123", online: false, created_at: "x", updated_at: "x", last_seen_at: null } as Schemas["Device"];
  const page = { items: [dev, { ...dev, id: 8 }], total: 2, page: 1, page_size: 50 };

  it("applies presence to the matching device only", () => {
    const next = patchDevicePage(page, 7, (d) => applyPresence(d, { id: 7, rustdesk_id: "123", online: true, since: "2026-10-15T12:00:00Z" }));
    expect(next?.items[0]).toMatchObject({ online: true, online_since: "2026-10-15T12:00:00Z", last_seen_at: "2026-10-15T12:00:00Z" });
    expect(next?.items[1]).toBe(page.items[1]);
  });

  it("returns the same page object when the device is not on it", () => {
    expect(patchDevicePage(page, 99, (d) => ({ ...d, online: true }))).toBe(page);
  });

  it("merges device.updated payloads", () => {
    expect(mergeDevice({ ...dev, alias: "a" }, { ...dev, alias: "b" }).alias).toBe("b");
    expect(mergeDevice(dev, { ...dev, id: 1, alias: "b" })).toBe(dev);
  });
});
