/** Audit time range presets ↔ API `from`/`to` (RFC 3339). */
export const RANGE_PRESETS = ["1h", "24h", "7d", "30d", "90d", "all", "custom"] as const;
export type RangePreset = (typeof RANGE_PRESETS)[number];

const HOURS: Record<string, number> = { "1h": 1, "24h": 24, "7d": 168, "30d": 720, "90d": 2160 };

export function isRangePreset(v: unknown): v is RangePreset {
  return typeof v === "string" && (RANGE_PRESETS as readonly string[]).includes(v);
}

/** `datetime-local` value (local time, no zone) → ISO UTC; undefined when empty/invalid. */
export function localInputToIso(v: unknown): string | undefined {
  if (typeof v !== "string" || !v) return undefined;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** ISO → `datetime-local` input value in local time. */
export function isoToLocalInput(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Resolves a preset (or custom bounds) to API parameters. */
export function rangeToQuery(
  range: unknown,
  custom: { from?: unknown; to?: unknown } = {},
  now: number = Date.now(),
): { from?: string; to?: string } {
  const preset: RangePreset = isRangePreset(range) ? range : "7d";
  if (preset === "all") return {};
  if (preset === "custom") {
    const out: { from?: string; to?: string } = {};
    const f = localInputToIso(custom.from);
    const t = localInputToIso(custom.to);
    if (f) out.from = f;
    if (t) out.to = t;
    return out;
  }
  return { from: new Date(now - (HOURS[preset] ?? 168) * 3600_000).toISOString() };
}
