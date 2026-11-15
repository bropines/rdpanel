import { describe, expect, it } from "vitest";

import de from "@/i18n/locales/de.json";
import en from "@/i18n/locales/en.json";
import ru from "@/i18n/locales/ru.json";

type Tree = { [k: string]: string | Tree };

const PLURAL = /_(zero|one|two|few|many|other)$/;

function keys(tree: Tree, prefix = ""): string[] {
  return Object.entries(tree).flatMap(([k, v]) =>
    typeof v === "string" ? [prefix + k] : keys(v, `${prefix}${k}.`),
  );
}

/** Keys with plural suffixes collapsed (en and de have one/other, ru has one/few/many/other). */
function baseKeys(tree: Tree): string[] {
  return [...new Set(keys(tree).map((k) => k.replace(PLURAL, "")))].sort();
}

/** Translations checked against en, with the plural forms each language needs. */
const LOCALES: { name: string; tree: Tree; forms: string[] }[] = [
  { name: "ru", tree: ru as Tree, forms: ["one", "few", "many", "other"] },
  { name: "de", tree: de as Tree, forms: ["one", "other"] },
];

describe("locales", () => {
  it.each(LOCALES)("$name has exactly the same keys as en (modulo plural forms)", ({ tree }) => {
    expect(baseKeys(tree)).toEqual(baseKeys(en as Tree));
  });

  it.each(LOCALES)("$name plural keys have every form the language needs", ({ tree, forms }) => {
    const enKeys = keys(en as Tree);
    const locKeys = new Set(keys(tree));
    const plurals = [...new Set(enKeys.filter((k) => PLURAL.test(k)).map((k) => k.replace(PLURAL, "")))];
    expect(plurals.length).toBeGreaterThan(0);
    for (const base of plurals) {
      expect(enKeys).toContain(`${base}_one`);
      expect(enKeys).toContain(`${base}_other`);
      for (const form of forms) expect(locKeys.has(`${base}_${form}`), `${base}_${form}`).toBe(true);
    }
  });

  it.each(LOCALES)("$name has no empty strings and keeps interpolation variables", ({ tree }) => {
    expect(JSON.stringify(en)).not.toContain('""');
    expect(JSON.stringify(tree)).not.toContain('""');
    const vars = (s: string) => [...s.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();
    const flat = (t: Tree, p = ""): Record<string, string> =>
      Object.entries(t).reduce<Record<string, string>>((acc, [k, v]) => {
        if (typeof v === "string") acc[p + k] = v;
        else Object.assign(acc, flat(v, `${p}${k}.`));
        return acc;
      }, {});
    const e = flat(en as Tree);
    const r = flat(tree);
    for (const [k, v] of Object.entries(r)) {
      const enValue = e[k] ?? e[k.replace(PLURAL, "_other")];
      if (enValue === undefined) continue;
      expect(vars(v), k).toEqual(vars(enValue));
    }
  });
});
