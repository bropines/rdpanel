import { describe, expect, it } from "vitest";

import en from "@/i18n/locales/en.json";
import ru from "@/i18n/locales/ru.json";

type Tree = { [k: string]: string | Tree };

function keys(tree: Tree, prefix = ""): string[] {
  return Object.entries(tree).flatMap(([k, v]) =>
    typeof v === "string" ? [prefix + k] : keys(v, `${prefix}${k}.`),
  );
}

describe("locales", () => {
  it("ru has exactly the same keys as en", () => {
    expect(keys(ru as Tree).sort()).toEqual(keys(en as Tree).sort());
  });

  it("has no empty strings", () => {
    for (const tree of [en, ru] as Tree[]) {
      const flat = keys(tree);
      expect(flat.length).toBeGreaterThan(0);
    }
    expect(JSON.stringify(en)).not.toContain('""');
    expect(JSON.stringify(ru)).not.toContain('""');
  });
});
