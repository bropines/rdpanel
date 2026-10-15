import { describe, expect, it } from "vitest";

import { nextTheme, readStoredTheme, resolveTheme, THEME_STORAGE_KEY } from "@/lib/theme";

describe("theme", () => {
  it("reads only valid stored values", () => {
    const store = (v: string | null) => ({ getItem: (k: string) => (k === THEME_STORAGE_KEY ? v : null) });
    expect(readStoredTheme(store("dark"))).toBe("dark");
    expect(readStoredTheme(store("bogus"))).toBe("system");
    expect(readStoredTheme(store(null))).toBe("system");
  });

  it("resolves system against the OS preference", () => {
    expect(resolveTheme("system", true)).toBe("dark");
    expect(resolveTheme("system", false)).toBe("light");
    expect(resolveTheme("light", true)).toBe("light");
  });

  it("cycles light → dark → system", () => {
    expect(nextTheme("light")).toBe("dark");
    expect(nextTheme("dark")).toBe("system");
    expect(nextTheme("system")).toBe("light");
  });
});
