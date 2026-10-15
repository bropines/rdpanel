import { describe, expect, it } from "vitest";

import en from "@/i18n/locales/en.json";
import { NAV_ITEMS, visibleSections } from "@/nav";

function lookup(key: string): unknown {
  return key.split(".").reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], en);
}

describe("nav", () => {
  it("hides the System section from non-admins", () => {
    expect(visibleSections(false).map((s) => s.titleKey)).toEqual(["nav.overview", "nav.my"]);
    expect(visibleSections(true).map((s) => s.titleKey)).toContain("nav.system");
  });

  it("has unique paths and translated labels", () => {
    const paths = NAV_ITEMS.map((i) => i.path);
    expect(new Set(paths).size).toBe(paths.length);
    for (const item of NAV_ITEMS) {
      expect(typeof lookup(item.labelKey)).toBe("string");
    }
  });
});
