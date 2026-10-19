import { api, type Schemas } from "@/api/client";

/* ── Relays (Server → Relays) ─────────────────────────────────────────── */

export type Relay = Schemas["Relay"];
export type RelayList = Schemas["RelayList"];
export type RelayRotation = Schemas["RelayRotation"];
export type RelayCreate = Schemas["RelayCreate"];
export type RelayCreated = Schemas["RelayCreated"];
export type RelayPatch = Schemas["RelayPatch"];
export type RelayEnrollment = Schemas["RelayEnrollment"];
export type RelayOptions = Schemas["RelayOptions"];
export type RelaySettingsValues = Schemas["RelaySettingsValues"];
export type RelaySettingsState = Schemas["RelaySettingsState"];
export type RelayTrafficSummary = Schemas["RelayTrafficSummary"];
export type RelayTrafficPoint = Schemas["RelayTrafficPoint"];
export type RelayPrefixRule = Schemas["RelayPrefixRule"];
export type RelayPrefixRuleInput = Schemas["RelayPrefixRuleInput"];

export const relayKeys = {
  all: ["relays"] as const,
  list: ["relays", "list"] as const,
  options: ["relays", "options"] as const,
  prefixRules: ["relays", "prefix-rules"] as const,
  settings: (id: number) => ["relays", "settings", id] as const,
  usage: (id: number) => ["relays", "usage", id] as const,
  traffic: (id: number, hours?: number) => ["relays", "traffic", id, hours ?? 24] as const,
};

export const relaysApi = {
  list: () => api.request<RelayList>("/relays"),
  rotation: () => api.request<RelayRotation>("/relays/rotation"),
  create: (body: RelayCreate) => api.request<RelayCreated>("/relays", { method: "POST", body }),
  get: (id: number) => api.request<Relay>(`/relays/${id}`),
  update: (id: number, body: RelayPatch) => api.request<Relay>(`/relays/${id}`, { method: "PATCH", body }),
  remove: (id: number) => api.request<void>(`/relays/${id}`, { method: "DELETE" }),
  resetEnrollment: (id: number) => api.request<RelayEnrollment>(`/relays/${id}/enroll-token`, { method: "POST" }),
  approve: (id: number) => api.request<Relay>(`/relays/${id}/approve`, { method: "POST" }),
  revoke: (id: number) => api.request<Relay>(`/relays/${id}/revoke`, { method: "POST" }),
  probe: (id: number) => api.request<Relay>(`/relays/${id}/probe`, { method: "POST" }),
  settings: (id: number) => api.request<RelaySettingsState>(`/relays/${id}/settings`),
  updateSettings: (id: number, body: RelaySettingsValues) =>
    api.request<RelaySettingsState>(`/relays/${id}/settings`, { method: "PUT", body }),
  usage: (id: number) => api.request<{ checked_at: string; items: Schemas["RelayUsage"][] }>(`/relays/${id}/usage`),
  traffic: (id: number, hours = 24) => api.request<RelayTrafficSummary>(`/relays/${id}/traffic?hours=${hours}`),
  options: () => api.request<RelayOptions>("/relays/options"),
  updateOptions: (body: RelayOptions) => api.request<RelayOptions>("/relays/options", { method: "PUT", body }),
  prefixRules: () => api.request<RelayPrefixRule[]>("/relays/prefix-rules"),
  createPrefixRule: (body: RelayPrefixRuleInput) =>
    api.request<RelayPrefixRule>("/relays/prefix-rules", { method: "POST", body }),
  deletePrefixRule: (id: number) =>
    api.request<void>(`/relays/prefix-rules/${id}`, { method: "DELETE" }),
};

/** The relay keys of the per-relay override form (same as the hbbr part of server settings). */
export const RELAY_NUM_KEYS = [
  "total_bandwidth_mbps",
  "single_bandwidth_mbps",
  "limit_speed_mbps",
  "downgrade_threshold",
  "downgrade_start_check_s",
] as const;
export type RelayNumKey = (typeof RELAY_NUM_KEYS)[number];

export interface OverrideDraft {
  nums: Record<RelayNumKey, string>;
  blacklist: string | null;
  blocklist: string | null;
}

/** Overrides -> editable draft ("" / null = inherit). */
export function toOverrideDraft(v: RelaySettingsValues): OverrideDraft {
  const n = (x: number | null | undefined) => (x == null ? "" : String(x));
  return {
    nums: {
      total_bandwidth_mbps: n(v.total_bandwidth_mbps),
      single_bandwidth_mbps: n(v.single_bandwidth_mbps),
      limit_speed_mbps: n(v.limit_speed_mbps),
      downgrade_threshold: n(v.downgrade_threshold),
      downgrade_start_check_s: n(v.downgrade_start_check_s),
    },
    blacklist: v.blacklist == null ? null : v.blacklist.join("\n"),
    blocklist: v.blocklist == null ? null : v.blocklist.join("\n"),
  };
}

/** Draft -> PUT body; returns null when a number is invalid. Exported for tests. */
export function fromOverrideDraft(d: OverrideDraft): RelaySettingsValues | null {
  const out: RelaySettingsValues = {};
  for (const k of RELAY_NUM_KEYS) {
    const raw = d.nums[k].trim();
    if (raw === "") {
      out[k] = null;
      continue;
    }
    const v = Number(raw);
    if (!Number.isFinite(v) || v <= 0 || (k === "downgrade_start_check_s" && !Number.isInteger(v))) return null;
    out[k] = v;
  }
  const lines = (s: string | null) =>
    s == null
      ? null
      : s
          .split(/[\n,]/)
          .map((x) => x.trim())
          .filter(Boolean);
  out.blacklist = lines(d.blacklist);
  out.blocklist = lines(d.blocklist);
  return out;
}

/** Megabits per second from bit/s, for the UI. */
export function mbps(bps: number): string {
  return (bps / 1_000_000).toFixed(bps >= 10_000_000 ? 0 : 1);
}
