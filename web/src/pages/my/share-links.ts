// Helpers of the "Share this server" card: the create form → ShareLinkInput,
// counters and status display.

import type { ShareLink, ShareLinkInput, ShareLinkStatus } from "@/api/share";

export const SHARE_DURATIONS = ["1h", "24h", "7d", "30d"] as const;
export type ShareDuration = (typeof SHARE_DURATIONS)[number];
export type ShareExpiry = "permanent" | ShareDuration | "custom";

export interface ShareForm {
  expiry: ShareExpiry;
  /** `<input type="datetime-local">` value (local time) for "custom". */
  customDate: string;
  label: string;
  maxDownloads: string;
}

export type ShareFormResult =
  | { ok: true; body: ShareLinkInput }
  | { ok: false; field: "label" | "customDate" | "maxDownloads"; error: string };

const MINUTE = 60_000;
const DAY = 24 * 60 * MINUTE;
export const MAX_SHARE_DOWNLOADS = 100000;

/** Formats d as a datetime-local value ("YYYY-MM-DDTHH:mm", local time). */
export function toLocalInput(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** Parses a datetime-local value as local time; null when malformed. */
export function parseLocalInput(v: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(v.trim());
  if (!m) return null;
  const n = (i: number) => Number(m[i] ?? 0);
  const out = new Date(n(1), n(2) - 1, n(3), n(4), n(5), n(6));
  return out.getMonth() === n(2) - 1 && out.getDate() === n(3) ? out : null;
}

export function defaultShareForm(now = new Date()): ShareForm {
  return { expiry: "permanent", customDate: toLocalInput(new Date(now.getTime() + 7 * DAY)), label: "", maxDownloads: "" };
}

/** Validates the form like the server and builds the POST body. */
export function buildShareInput(f: ShareForm, now = new Date()): ShareFormResult {
  const body: ShareLinkInput = {};
  const label = f.label.trim();
  if ([...label].length > 64) return { ok: false, field: "label", error: "validation.tooLong" };
  if (label) body.label = label;
  if (f.expiry === "custom") {
    const t = parseLocalInput(f.customDate);
    if (!t || t.getTime() < now.getTime() + MINUTE || t.getTime() > now.getTime() + 365 * DAY) {
      return { ok: false, field: "customDate", error: "share.card.errors.date" };
    }
    body.expires_at = t.toISOString();
  } else if (f.expiry !== "permanent") {
    body.expires_in = f.expiry;
  }
  const md = f.maxDownloads.trim();
  if (md) {
    if (!/^\d+$/.test(md) || Number(md) < 1 || Number(md) > MAX_SHARE_DOWNLOADS) {
      return { ok: false, field: "maxDownloads", error: "share.card.errors.downloads" };
    }
    body.max_downloads = Number(md);
  }
  return { ok: true, body };
}

/** "3" or "3 / 10" (with a download limit). */
export function downloadsText(l: Pick<ShareLink, "downloads" | "max_downloads">): string {
  return l.max_downloads ? `${l.downloads} / ${l.max_downloads}` : String(l.downloads);
}

export const STATUS_VARIANT: Record<ShareLinkStatus, "success" | "secondary" | "destructive" | "outline"> = {
  active: "success",
  expired: "secondary",
  exhausted: "secondary",
  revoked: "destructive",
  suspended: "outline",
};

/** Active links first, then the rest; newest first within each group. */
export function sortLinks(links: ShareLink[]): ShareLink[] {
  return [...links].sort(
    (a, b) => Number(b.status === "active") - Number(a.status === "active") || Date.parse(b.created_at) - Date.parse(a.created_at),
  );
}
