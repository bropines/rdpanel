/** Locale-aware formatting helpers (all inputs are RFC 3339 strings from the API). */

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

/** "3 minutes ago" / "in 2 hours"; "just now" under 10 s. */
export function relativeTime(iso: string | null | undefined, lang: string, now: number = Date.now()): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const diff = Math.round((t - now) / 1000);
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: "auto" });
  if (abs < 10) return rtf.format(0, "second");
  for (const [unit, secs] of UNITS) {
    if (abs >= secs || unit === "second") return rtf.format(Math.trunc(diff / secs), unit);
  }
  return rtf.format(diff, "second");
}

export function formatDateTime(iso: string | null | undefined, lang: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat(lang, { dateStyle: "medium", timeStyle: "short" }).format(d);
}

export function formatDate(iso: string | null | undefined, lang: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat(lang, { dateStyle: "medium" }).format(d);
}

export function formatNumber(n: number, lang: string, maxFractionDigits = 0): string {
  return new Intl.NumberFormat(lang, { maximumFractionDigits: maxFractionDigits }).format(n);
}

/** "1h 02m" style duration between two instants (or until now). */
export function formatDuration(fromIso: string, toIso: string | null | undefined, now: number = Date.now()): string {
  const from = Date.parse(fromIso);
  const to = toIso ? Date.parse(toIso) : now;
  if (Number.isNaN(from) || Number.isNaN(to)) return "—";
  let s = Math.max(0, Math.round((to - from) / 1000));
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  s -= m * 60;
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

/** Short human label for a user reference. */
export function userLabel(u: { username: string; display_name?: string } | null | undefined): string {
  if (!u) return "—";
  return u.display_name && u.display_name !== u.username ? `${u.display_name} (${u.username})` : u.username;
}

/** RustDesk ids are shown grouped in threes for readability: 123 456 789. */
export function formatRustdeskId(id: string): string {
  return /^\d{7,}$/.test(id) ? id.replace(/\B(?=(\d{3})+(?!\d))/g, " ") : id;
}
