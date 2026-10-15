/**
 * RustDesk tag colors are Flutter ARGB uint32 values (e.g. 0xFF9E9E9E = 4288585374).
 * Helpers to convert to/from CSS hex and to pick a readable foreground.
 */

export const DEFAULT_TAG_COLOR = 0xff9e9e9e;

/** Palette offered in the tag manager (Material 500 tones, fully opaque). */
export const TAG_PALETTE: number[] = [
  0xfff44336, 0xffe91e63, 0xff9c27b0, 0xff673ab7, 0xff3f51b5, 0xff2196f3, 0xff03a9f4, 0xff00bcd4,
  0xff009688, 0xff4caf50, 0xff8bc34a, 0xffcddc39, 0xffffc107, 0xffff9800, 0xffff5722, 0xff795548,
  0xff9e9e9e, 0xff607d8b,
];

function clampArgb(argb: number): number {
  if (!Number.isFinite(argb)) return DEFAULT_TAG_COLOR;
  return Math.min(0xffffffff, Math.max(0, Math.trunc(argb)));
}

/** ARGB uint32 → `{r,g,b,a}` with a in 0..1. */
export function argbToRgba(argb: number): { r: number; g: number; b: number; a: number } {
  const v = clampArgb(argb);
  const a = Math.floor(v / 0x1000000) & 0xff;
  return { a: a / 255, r: (v >>> 16) & 0xff, g: (v >>> 8) & 0xff, b: v & 0xff };
}

/** ARGB uint32 → CSS `rgba(...)`. Alpha 0 is treated as opaque (legacy rows store 0x00RRGGBB). */
export function argbToCss(argb: number): string {
  const { r, g, b, a } = argbToRgba(argb);
  return `rgba(${r}, ${g}, ${b}, ${a === 0 ? 1 : Number(a.toFixed(3))})`;
}

/** ARGB uint32 → `#rrggbb` (alpha dropped), for `<input type=color>`. */
export function argbToHex(argb: number): string {
  const { r, g, b } = argbToRgba(argb);
  return `#${[r, g, b].map((x) => x.toString(16).padStart(2, "0")).join("")}`;
}

/** `#rrggbb` / `#rgb` → opaque ARGB uint32; undefined when invalid. */
export function hexToArgb(hex: string): number | undefined {
  let h = hex.trim().replace(/^#/, "");
  if (/^[0-9a-f]{3}$/i.test(h)) h = h.replace(/./g, (c) => c + c);
  if (!/^[0-9a-f]{6}$/i.test(h)) return undefined;
  return 0xff000000 + parseInt(h, 16);
}

/** Black or white text for best contrast on the given background (WCAG relative luminance). */
export function readableTextColor(argb: number): "#000000" | "#ffffff" {
  const { r, g, b } = argbToRgba(argb);
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  const L = 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
  return L > 0.4 ? "#000000" : "#ffffff";
}
