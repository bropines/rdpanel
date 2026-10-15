/**
 * MSW handlers implementing api/openapi.yaml against the in-memory dataset.
 * Mock credentials: admin/admin (TOTP 123456 or recovery code AAAA-BBBB),
 * ivan/ivan (TOTP 123456), maria/maria (no 2FA; must enrol).
 */
import { delay, http, HttpResponse, type JsonBodyType } from "msw";

import type { Schemas } from "@/api/client";
import type { IdentityProvider, IdentityProviderInput } from "@/api/identityProviders";
import * as db from "@/mocks/data";
import { liveStream } from "@/mocks/sse";

type S = Schemas;
const B = "/api/v1/admin";
const SESSION_KEY = "rdpanel-mock-user";

/* ── helpers ──────────────────────────────────────────────────────────── */

function err(status: number, code: string, message: string, fields?: Record<string, string>) {
  return HttpResponse.json({ error: { code, message, ...(fields ? { fields } : {}) } }, { status });
}
const ok = (body: JsonBodyType, status = 200) => HttpResponse.json(body, { status });
const noContent = () => new HttpResponse(null, { status: 204 });
const nowIso = () => new Date().toISOString();

function currentUserId(): number | null {
  try {
    const v = localStorage.getItem(SESSION_KEY);
    if (v === null) return 1; // logged in as admin by default in mock mode
    return v === "" ? null : Number(v);
  } catch {
    return 1;
  }
}
function setCurrentUser(id: number | null) {
  try {
    localStorage.setItem(SESSION_KEY, id === null ? "" : String(id));
  } catch {
    /* ignore */
  }
}
function me() {
  const id = currentUserId();
  return id === null ? undefined : db.users.find((u) => u.id === id);
}
let pendingMfaUser: number | null = null;

function requireAuth(): Response | db.MockUser {
  const u = me();
  if (!u) return err(401, "unauthorized", "authentication required");
  return u;
}
function requireAdmin(): Response | db.MockUser {
  const u = requireAuth();
  if (u instanceof Response) return u;
  if (u.role !== "admin") return err(403, "forbidden", "admin only");
  return u;
}

function publicUser(u: db.MockUser): S["User"] {
  const { password: _p, identities: _i, address_book_count: _a, device_count: _d, is_last_admin: _l, user_group: _g, ...rest } = u;
  return rest;
}
function listItem(u: db.MockUser): S["UserListItem"] {
  const { password: _p, identities: _i, address_book_count: _a, ...rest } = u;
  return rest;
}
function meBody(u: db.MockUser): S["Me"] {
  return { user: publicUser(u), csrf_token: "mock-csrf", must_enroll_totp: !!u.totp_required && !u.totp_enabled };
}

function qs(request: Request) {
  return new URL(request.url).searchParams;
}

function paginate<T>(items: T[], params: URLSearchParams) {
  const page = Math.max(1, Number(params.get("page") ?? 1) || 1);
  const pageSize = Math.min(500, Math.max(1, Number(params.get("page_size") ?? 50) || 50));
  return { items: items.slice((page - 1) * pageSize, page * pageSize), total: items.length, page, page_size: pageSize };
}

function sortBy<T>(items: T[], sort: string | null, getters: Record<string, (x: T) => string | number | boolean | null | undefined>): T[] {
  if (!sort) return items;
  const desc = sort.startsWith("-");
  const key = desc ? sort.slice(1) : sort;
  const get = getters[key];
  if (!get) return items;
  return [...items].sort((a, b) => {
    const va = get(a);
    const vb = get(b);
    if (va == null && vb == null) return 0;
    if (va == null) return 1;
    if (vb == null) return -1;
    const c = typeof va === "string" && typeof vb === "string" ? va.localeCompare(vb) : Number(va) - Number(vb);
    return desc ? -c : c;
  });
}

const contains = (q: string, ...fields: (string | null | undefined)[]) => {
  const n = q.toLowerCase();
  return fields.some((f) => f?.toLowerCase().includes(n));
};

function inRange(isoTime: string, params: URLSearchParams) {
  const t = Date.parse(isoTime);
  const from = params.get("from");
  const to = params.get("to");
  if (from && t < Date.parse(from)) return false;
  if (to && t >= Date.parse(to)) return false;
  return true;
}

async function body<T>(request: Request): Promise<T> {
  try {
    return (await request.json()) as T;
  } catch {
    return {} as T;
  }
}

function bookPermission(b: S["AddressBook"], u: db.MockUser): S["Permission"] | null {
  if (u.role === "admin" || b.owner_user_id === u.id) return "full";
  const rank = { read: 1, read_write: 2, full: 3 } as const;
  let best: S["Permission"] | null = null;
  for (const s of db.shares.filter((x) => x.address_book_id === b.id)) {
    const match = (s.grantee_type === "user" && s.grantee.id === u.id) || (s.grantee_type === "group" && s.grantee.id === u.user_group_id);
    if (match && (!best || rank[s.permission] > rank[best])) best = s.permission;
  }
  return best;
}

function bookView(b: S["AddressBook"], u: db.MockUser): S["AddressBook"] {
  return { ...b, owner: db.userRef(b.owner_user_id)!, permission: bookPermission(b, u) ?? "read" };
}

function deviceVisible(d: db.MockDevice, u: db.MockUser) {
  return u.role === "admin" || d.owner_user_id === u.id;
}

function filterDevices(params: URLSearchParams, u: db.MockUser) {
  const q = params.get("q");
  const online = params.get("online");
  const owner = params.get("owner");
  const group = params.get("device_group");
  const os = params.get("os");
  const version = params.get("version");
  const seenAfter = params.get("seen_after");
  return db.devices.filter((d) => {
    if (!deviceVisible(d, u)) return false;
    if (q && !contains(q, d.rustdesk_id, d.hostname, d.alias, d.os_username, d.last_seen_ip, d.note)) return false;
    if (online !== null && String(d.online) !== online) return false;
    if (owner === "me" && d.owner_user_id !== u.id) return false;
    if (owner === "none" && d.owner_user_id !== null) return false;
    if (owner && /^\d+$/.test(owner) && d.owner_user_id !== Number(owner)) return false;
    if (group === "none" && d.device_group_id !== null) return false;
    if (group && /^\d+$/.test(group) && d.device_group_id !== Number(group)) return false;
    if (os && d.os_family !== os) return false;
    if (version && d.client_version !== version) return false;
    if (seenAfter && (!d.last_seen_at || Date.parse(d.last_seen_at) < Date.parse(seenAfter))) return false;
    return true;
  });
}

const deviceSort: Record<string, (d: db.MockDevice) => string | number | boolean | null | undefined> = {
  rustdesk_id: (d) => d.rustdesk_id,
  hostname: (d) => d.hostname,
  alias: (d) => d.alias || d.hostname,
  os: (d) => d.os,
  client_version: (d) => d.client_version,
  last_seen_at: (d) => (d.online ? Date.now() : d.last_seen_at ? Date.parse(d.last_seen_at) : null),
  created_at: (d) => Date.parse(d.created_at),
  online: (d) => (d.online ? 1 : 0),
};

function series(times: string[], buckets = 24): S["SeriesPoint"][] {
  const now = Date.now();
  const start = now - buckets * 3600_000;
  const out = Array.from({ length: buckets }, (_, i) => ({ t: new Date(start + i * 3600_000).toISOString(), v: 0 }));
  for (const t of times) {
    const ms = Date.parse(t);
    if (ms < start) continue;
    const idx = Math.min(buckets - 1, Math.floor((ms - start) / 3600_000));
    out[idx]!.v += 1;
  }
  return out;
}

function health(): S["ServerHealth"] {
  return {
    hbbs: { status: "up", address: "127.0.0.1:21115", latency_ms: 2, checked_at: nowIso(), up_since: new Date(db.NOW - 6 * 86400_000).toISOString(), error: null },
    hbbr: { status: "up", address: "127.0.0.1:21117", latency_ms: 3, checked_at: nowIso(), up_since: new Date(db.NOW - 6 * 86400_000).toISOString(), error: null },
    presence: { source: "hbbs", last_poll_at: new Date(Date.now() - 3000).toISOString(), poll_duration_ms: 14, devices_online: db.devices.filter((d) => d.online).length },
    settings_in_sync: serverState().keys.every((k) => k.in_sync || !k.managed),
  };
}

const HBBS_KEYS = new Set(["relay_servers", "always_use_relay", "must_login"]);
function serverState(): S["ServerSettingsState"] {
  const keys = Object.keys(db.serverDesired) as S["ServerSettingKey"][];
  return {
    desired: db.serverDesired,
    applied: db.serverApplied,
    checked_at: new Date(Date.now() - 12_000).toISOString(),
    keys: keys.map((k) => ({
      key: k,
      target: HBBS_KEYS.has(k) ? "hbbs" : "hbbr",
      in_sync: JSON.stringify(db.serverDesired[k]) === JSON.stringify(db.serverApplied[k]),
      managed: true,
      updated_at: new Date(db.NOW - 3 * 86400_000).toISOString(),
      updated_by: "admin",
      applied_at: new Date(Date.now() - 50_000).toISOString(),
      last_error: k === "total_bandwidth_mbps" ? null : null,
    })),
  };
}

function audit(actor: db.MockUser, action: string, targetType: string, target: string, details: Record<string, unknown> = {}) {
  db.adminAudit.unshift({
    id: (db.adminAudit[0]?.id ?? 0) + 1,
    actor_user_id: actor.id,
    actor_name: actor.username,
    actor_ip: "127.0.0.1",
    action,
    target_type: targetType,
    target_id: target,
    target_label: target,
    details,
    created_at: nowIso(),
  });
}

let nextId = 10_000;
const newId = () => ++nextId;

/* ── handlers ─────────────────────────────────────────────────────────── */

export const handlers = [
  // Small latency so skeletons are visible but the UI stays snappy.
  http.all(`${B}/*`, async () => {
    await delay(120);
  }),

  /* auth (stream B1 contract as assumed by web/src/api/auth.ts) */
  http.get(`${B}/auth/csrf`, () => ok({ csrf_token: "mock-csrf" })),
  http.get(`${B}/auth/providers`, () =>
    ok(db.providers.filter((p) => p.enabled).map((p) => ({ slug: p.slug, display_name: p.display_name, kind: p.kind }))),
  ),
  http.get(`${B}/auth/me`, () => {
    const u = me();
    return u ? ok(meBody(u)) : err(401, "unauthorized", "authentication required");
  }),
  http.post(`${B}/auth/login`, async ({ request }) => {
    const b = await body<{ username?: string; password?: string }>(request);
    const u = db.users.find((x) => x.username.toLowerCase() === (b.username ?? "").toLowerCase());
    if (!u || u.password !== b.password) return err(401, "unauthorized", "invalid username or password");
    if (u.status !== "active") return err(403, "account_disabled", "account disabled");
    if (u.totp_enabled) {
      pendingMfaUser = u.id;
      return ok({ mfa_required: true, methods: ["totp", "recovery"] });
    }
    setCurrentUser(u.id);
    return ok(meBody(u));
  }),
  http.post(`${B}/auth/mfa`, async ({ request }) => {
    const b = await body<{ code?: string }>(request);
    if (pendingMfaUser === null) return err(401, "unauthorized", "no pending login");
    if (b.code !== "123456" && b.code?.toUpperCase() !== "AAAA-BBBB") return err(401, "totp_invalid", "invalid code");
    const u = db.users.find((x) => x.id === pendingMfaUser)!;
    pendingMfaUser = null;
    setCurrentUser(u.id);
    return ok(meBody(u));
  }),
  http.post(`${B}/auth/logout`, () => {
    setCurrentUser(null);
    return noContent();
  }),
  http.get(`${B}/auth/oidc/:slug/start`, () => {
    setCurrentUser(1);
    return HttpResponse.redirect("/", 302);
  }),

  /* my security (stream B1 contract as assumed by web/src/api/security.ts) */
  http.patch(`${B}/me`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<{ display_name?: string; email?: string | null }>(request);
    if (b.display_name !== undefined) u.display_name = b.display_name;
    if (b.email !== undefined) u.email = b.email;
    return ok(publicUser(u));
  }),
  http.post(`${B}/me/password`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<{ current_password: string; new_password: string }>(request);
    if (b.current_password !== u.password) return err(401, "invalid_password", "current password is wrong");
    u.password = b.new_password;
    return noContent();
  }),
  http.post(`${B}/me/totp/enroll`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP";
    return ok({ secret, otpauth_uri: `otpauth://totp/RustDesk%20Panel:${u.username}?secret=${secret}&issuer=RustDesk%20Panel&digits=6&period=30` });
  }),
  http.post(`${B}/me/totp/confirm`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<{ code?: string }>(request);
    if (b.code !== "123456") return err(400, "totp_invalid", "invalid code (mock: use 123456)");
    u.totp_enabled = true;
    return ok({ recovery_codes: ["7KQ2-MX4P", "H9TR-2LWD", "QZ8C-VN3B", "4FJD-PE7S", "WB6N-K2RX", "M3YH-9QAT", "C8LP-ZT5E", "R2VG-6NUJ"] });
  }),
  http.post(`${B}/me/totp/disable`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<{ password?: string; code?: string }>(request);
    if (b.password !== u.password) return err(401, "invalid_password", "wrong password");
    if (b.code !== "123456") return err(400, "totp_invalid", "invalid code");
    u.totp_enabled = false;
    return noContent();
  }),
  http.post(`${B}/me/recovery-codes`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<{ code?: string }>(request);
    if (b.code !== "123456") return err(400, "totp_invalid", "invalid code");
    return ok({ recovery_codes: ["P4XW-7DNA", "JE2K-QR8V", "Z6TB-M3HC", "L9GS-4WYF", "N2CU-8KPE", "T7RM-5JZQ", "V3HD-6XBL", "F8QN-2AEW"] });
  }),
  http.get(`${B}/me/sessions`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    return ok({ items: db.sessionsByUser[u.id] ?? [] });
  }),
  http.delete(`${B}/me/sessions/:id`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    db.sessionsByUser[u.id] = (db.sessionsByUser[u.id] ?? []).filter((s) => s.id !== Number(params.id));
    return noContent();
  }),
  http.post(`${B}/me/sessions/revoke-others`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const list = db.sessionsByUser[u.id] ?? [];
    db.sessionsByUser[u.id] = list.filter((s) => s.current);
    return ok({ revoked: list.length - (db.sessionsByUser[u.id]?.length ?? 0) });
  }),
  http.get(`${B}/me/identities`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    return ok({ items: u.identities });
  }),
  http.delete(`${B}/me/identities/:id`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    u.identities = u.identities.filter((i) => i.id !== Number(params.id));
    return noContent();
  }),

  /* branding / dashboard / lookups */
  http.get(`${B}/branding`, () =>
    ok({
      title: db.panelSettings.branding.title,
      logo_url: null,
      welcome_markdown: db.panelSettings.branding.welcome_markdown,
      web_client_url: db.panelSettings.branding.web_client_url ?? null,
    } satisfies S["Branding"]),
  ),
  http.get(`${B}/dashboard`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const admin = u.role === "admin";
    const devs = db.devices.filter((d) => deviceVisible(d, u));
    const dayAgo = Date.now() - 86400_000;
    const conns = db.connections.filter((c) => admin || devs.some((d) => d.id === c.device_id));
    const logins = db.logins.filter((l) => admin || l.user?.id === u.id);
    const onlineSeries = series([]).map((p, i, arr) => ({ ...p, v: Math.round(devs.filter((d) => d.online).length * (0.7 + 0.3 * Math.sin((i / arr.length) * Math.PI * 2 - 1.2) ** 2)) }));
    return ok({
      devices_total: devs.length,
      devices_online: devs.filter((d) => d.online).length,
      users_total: db.users.length,
      address_books_total: admin ? db.books.length : db.books.filter((b) => bookPermission(b, u)).length,
      logins_24h: logins.filter((l) => Date.parse(l.created_at) > dayAgo && l.result === "success").length,
      failed_logins_24h: logins.filter((l) => Date.parse(l.created_at) > dayAgo && l.result === "failure").length,
      connections_24h: conns.filter((c) => Date.parse(c.started_at) > dayAgo).length,
      active_connections: conns.filter((c) => !c.ended_at).length,
      series: {
        logins: series(logins.map((l) => l.created_at)),
        connections: series(conns.map((c) => c.started_at)),
        online: onlineSeries,
      },
      health: admin ? health() : null,
      version: { panel: "0.1.0-mock", commit: "6045ece0b1", build_date: "2026-10-15", go: "go1.23" },
    } satisfies S["Dashboard"]);
  }),
  http.get(`${B}/lookup/users`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const q = qs(request).get("q") ?? "";
    return ok({ items: db.users.filter((x) => x.status === "active" && (!q || contains(q, x.username, x.display_name))).slice(0, 20).map((x) => db.userRef(x.id)!) });
  }),
  http.get(`${B}/lookup/user-groups`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const q = qs(request).get("q") ?? "";
    return ok({ items: db.userGroups.filter((g) => !q || contains(q, g.name)).map((g) => ({ id: g.id, name: g.name })) });
  }),

  /* devices */
  http.get(`${B}/devices/facets`, () => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const devs = db.devices.filter((d) => deviceVisible(d, u));
    const count = <K extends string>(vals: K[]) => {
      const m = new Map<K, number>();
      for (const v of vals) m.set(v, (m.get(v) ?? 0) + 1);
      return [...m.entries()].map(([value, c]) => ({ value, count: c })).sort((a, b) => b.count - a.count);
    };
    return ok({
      total: devs.length,
      os: count(devs.map((d) => d.os_family ?? "other")),
      versions: count(devs.map((d) => d.client_version ?? "")).filter((v) => v.value),
    });
  }),
  http.get(`${B}/devices/export.csv`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const rows = filterDevices(qs(request), u);
    const esc = (v: unknown) => {
      let s = v == null ? "" : String(v);
      if (/^[=+\-@]/.test(s)) s = `'${s}`;
      return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
    };
    const lines = [
      "rustdesk_id,alias,hostname,os,client_version,owner,group,online,last_seen_at,last_seen_ip,note",
      ...rows.map((d) =>
        [d.rustdesk_id, d.alias, d.hostname, d.os, d.client_version, d.owner?.username, d.device_group?.name, d.online, d.last_seen_at, d.last_seen_ip, d.note].map(esc).join(","),
      ),
    ];
    return new HttpResponse(lines.join("\n"), {
      headers: { "Content-Type": "text/csv", "Content-Disposition": `attachment; filename="devices-${nowIso().slice(0, 10)}.csv"` },
    });
  }),
  http.post(`${B}/devices/bulk`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<S["DeviceBulkRequest"]>(request);
    const ids = new Set(b.device_ids ?? []);
    let affected = 0;
    for (const d of [...db.devices]) {
      if (!ids.has(d.id)) continue;
      affected++;
      if (b.action === "assign_owner") {
        d.owner_user_id = b.owner_user_id ?? null;
        d.owner = db.userRef(d.owner_user_id);
      } else if (b.action === "assign_group") {
        d.device_group_id = b.device_group_id ?? null;
        const g = db.deviceGroups.find((x) => x.id === d.device_group_id);
        d.device_group = g ? { id: g.id, name: g.name } : null;
      } else if (b.action === "kick") {
        d.kickRequested = true;
      } else if (b.action === "delete") {
        db.devices.splice(db.devices.indexOf(d), 1);
      }
    }
    audit(u, `device.bulk_${b.action}`, "device", `${affected} devices`);
    db.refreshCounts();
    return ok({ affected });
  }),
  http.get(`${B}/devices`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    return ok(paginate(sortBy(filterDevices(p, u), p.get("sort") ?? "-last_seen_at", deviceSort), p));
  }),
  http.get(`${B}/devices/:id`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const d = db.devices.find((x) => x.id === Number(params.id));
    if (!d || !deviceVisible(d, u)) return err(404, "not_found", "device not found");
    const inBooks = db.books.filter((b) => bookPermission(b, u) && db.entries.some((e) => e.address_book_id === b.id && e.rustdesk_id === d.rustdesk_id));
    return ok({
      ...d,
      recent_connections: db.connections.filter((c) => c.device_id === d.id).slice(0, 10),
      address_books: inBooks.map((b) => ({ id: b.id, name: b.is_personal ? `${b.name} · ${b.owner.username}` : b.name })),
    } satisfies S["DeviceDetail"]);
  }),
  http.patch(`${B}/devices/:id`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const d = db.devices.find((x) => x.id === Number(params.id));
    if (!d || !deviceVisible(d, u)) return err(404, "not_found", "device not found");
    const b = await body<S["DeviceUpdate"]>(request);
    if (u.role !== "admin" && (b.owner_user_id !== undefined || b.device_group_id !== undefined)) return err(403, "forbidden", "admin only");
    if (b.alias !== undefined) d.alias = b.alias;
    if (b.note !== undefined) d.note = b.note;
    if (b.owner_user_id !== undefined) {
      d.owner_user_id = b.owner_user_id;
      d.owner = db.userRef(b.owner_user_id);
    }
    if (b.device_group_id !== undefined) {
      d.device_group_id = b.device_group_id;
      const g = db.deviceGroups.find((x) => x.id === b.device_group_id);
      d.device_group = g ? { id: g.id, name: g.name } : null;
    }
    d.updated_at = nowIso();
    audit(u, "device.update", "device", d.rustdesk_id, b as Record<string, unknown>);
    db.refreshCounts();
    return ok(d);
  }),
  http.delete(`${B}/devices/:id`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const i = db.devices.findIndex((x) => x.id === Number(params.id));
    if (i < 0) return err(404, "not_found", "device not found");
    audit(u, "device.delete", "device", db.devices[i]!.rustdesk_id);
    db.devices.splice(i, 1);
    db.refreshCounts();
    return noContent();
  }),
  http.post(`${B}/devices/:id/kick`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const d = db.devices.find((x) => x.id === Number(params.id));
    if (!d) return err(404, "not_found", "device not found");
    d.kickRequested = true;
    audit(u, "device.kick", "device", d.rustdesk_id);
    return new HttpResponse(null, { status: 202 });
  }),

  /* users */
  http.get(`${B}/users`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const list = db.users.filter(
      (x) =>
        (!q || contains(q, x.username, x.display_name, x.email)) &&
        (!p.get("role") || x.role === p.get("role")) &&
        (!p.get("status") || x.status === p.get("status")) &&
        (!p.get("user_group_id") || x.user_group_id === Number(p.get("user_group_id"))),
    );
    const sorted = sortBy(list, p.get("sort") ?? "username", {
      username: (x) => x.username,
      created_at: (x) => Date.parse(x.created_at),
      last_login_at: (x) => (x.last_login_at ? Date.parse(x.last_login_at) : null),
      role: (x) => x.role,
    });
    const page = paginate(sorted, p);
    return ok({ ...page, items: page.items.map(listItem) });
  }),
  http.post(`${B}/users`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<S["UserCreate"]>(request);
    if (db.users.some((x) => x.username.toLowerCase() === b.username.toLowerCase())) return err(409, "conflict", "username taken");
    const g = db.userGroups.find((x) => x.id === b.user_group_id);
    const nu: db.MockUser = {
      id: Math.max(...db.users.map((x) => x.id)) + 1,
      username: b.username,
      display_name: b.display_name || b.username,
      email: b.email ?? null,
      avatar_url: "",
      note: b.note ?? "",
      role: b.role,
      status: b.status ?? "active",
      user_group_id: b.user_group_id ?? null,
      user_group: g ? { id: g.id, name: g.name } : null,
      has_password: !!b.password,
      totp_enabled: false,
      totp_required: !!b.totp_required,
      last_login_at: null,
      created_at: nowIso(),
      updated_at: nowIso(),
      device_count: 0,
      is_last_admin: false,
      identities: [],
      address_book_count: 1,
      password: b.password ?? "",
    };
    db.users.push(nu);
    db.books.push({ ...db.books[0]!, id: newId(), guid: `ab-${nextId}`, owner_user_id: nu.id, owner: db.userRef(nu.id)!, entry_count: 0, share_count: 0, created_at: nowIso(), updated_at: nowIso() });
    db.sessionsByUser[nu.id] = [];
    audit(u, "user.create", "user", nu.username);
    db.refreshCounts();
    return ok(listItem(nu), 201);
  }),
  http.get(`${B}/users/:id`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const x = db.users.find((y) => y.id === Number(params.id));
    if (!x) return err(404, "not_found", "user not found");
    const { password: _p, ...detail } = x;
    return ok(detail);
  }),
  http.patch(`${B}/users/:id`, async ({ params, request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const x = db.users.find((y) => y.id === Number(params.id));
    if (!x) return err(404, "not_found", "user not found");
    const b = await body<S["UserUpdate"]>(request);
    if (x.is_last_admin && ((b.role && b.role !== "admin") || (b.status && b.status !== "active")))
      return err(409, "last_admin", "cannot demote or disable the last admin");
    if (b.username && db.users.some((y) => y.id !== x.id && y.username.toLowerCase() === b.username!.toLowerCase())) return err(409, "conflict", "username taken");
    Object.assign(x, b);
    if (b.user_group_id !== undefined) {
      const g = db.userGroups.find((y) => y.id === b.user_group_id);
      x.user_group = g ? { id: g.id, name: g.name } : null;
    }
    x.updated_at = nowIso();
    audit(u, "user.update", "user", x.username, b as Record<string, unknown>);
    db.refreshCounts();
    return ok(listItem(x));
  }),
  http.delete(`${B}/users/:id`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const i = db.users.findIndex((y) => y.id === Number(params.id));
    if (i < 0) return err(404, "not_found", "user not found");
    const x = db.users[i]!;
    if (x.is_last_admin) return err(409, "last_admin", "cannot delete the last admin");
    if (x.id === u.id) return err(409, "self", "cannot delete yourself");
    db.users.splice(i, 1);
    audit(u, "user.delete", "user", x.username);
    db.refreshCounts();
    return noContent();
  }),
  http.post(`${B}/users/:id/password`, async ({ params, request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const x = db.users.find((y) => y.id === Number(params.id));
    if (!x) return err(404, "not_found", "user not found");
    const b = await body<S["PasswordReset"]>(request);
    if (!b.password || b.password.length < 8) return err(400, "validation_failed", "password too short", { password: "min 8" });
    x.password = b.password;
    x.has_password = true;
    if (b.revoke_sessions !== false) db.sessionsByUser[x.id] = [];
    audit(u, "user.password_reset", "user", x.username, { revoke_sessions: b.revoke_sessions !== false });
    return noContent();
  }),
  http.post(`${B}/users/:id/totp/reset`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const x = db.users.find((y) => y.id === Number(params.id));
    if (!x) return err(404, "not_found", "user not found");
    x.totp_enabled = false;
    audit(u, "user.totp_reset", "user", x.username);
    return noContent();
  }),
  http.get(`${B}/users/:id/sessions`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok({ items: (db.sessionsByUser[Number(params.id)] ?? []).map((s) => ({ ...s, current: s.current && Number(params.id) === u.id })) });
  }),
  http.post(`${B}/users/:id/sessions/revoke`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const id = Number(params.id);
    const before = db.sessionsByUser[id]?.length ?? 0;
    db.sessionsByUser[id] = (db.sessionsByUser[id] ?? []).filter((s) => s.current && id === u.id);
    return ok({ revoked: before - (db.sessionsByUser[id]?.length ?? 0) });
  }),
  http.delete(`${B}/users/:id/sessions/:sid`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const id = Number(params.id);
    db.sessionsByUser[id] = (db.sessionsByUser[id] ?? []).filter((s) => s.id !== Number(params.sid));
    return noContent();
  }),
  http.delete(`${B}/users/:id/identities/:iid`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const x = db.users.find((y) => y.id === Number(params.id));
    if (!x) return err(404, "not_found", "user not found");
    x.identities = x.identities.filter((i) => i.id !== Number(params.iid));
    return noContent();
  }),

  /* groups */
  ...(["user-groups", "device-groups"] as const).flatMap((path) => {
    const list = () => (path === "user-groups" ? db.userGroups : db.deviceGroups) as (S["UserGroup"] | S["DeviceGroup"])[];
    return [
      http.get(`${B}/${path}`, ({ request }) => {
        const u = requireAdmin();
        if (u instanceof Response) return u;
        const p = qs(request);
        const q = p.get("q");
        const items = list().filter((g) => !q || contains(q, g.name, g.note));
        return ok(
          paginate(
            sortBy(items, p.get("sort") ?? "name", {
              name: (g) => g.name,
              member_count: (g) => ("member_count" in g ? g.member_count : 0),
              device_count: (g) => ("device_count" in g ? g.device_count : 0),
              created_at: (g) => Date.parse(g.created_at),
            }),
            p,
          ),
        );
      }),
      http.post(`${B}/${path}`, async ({ request }) => {
        const u = requireAdmin();
        if (u instanceof Response) return u;
        const b = await body<{ name: string; note?: string; visibility?: "self" | "shared" }>(request);
        if (list().some((g) => g.name.toLowerCase() === b.name.toLowerCase())) return err(409, "conflict", "name taken");
        const base = { id: newId(), name: b.name, note: b.note ?? "", created_at: nowIso(), updated_at: nowIso() };
        const g = path === "user-groups" ? { ...base, visibility: b.visibility ?? "self", member_count: 0 } : { ...base, device_count: 0 };
        (list() as unknown[]).push(g);
        audit(u, `${path === "user-groups" ? "user_group" : "device_group"}.create`, "group", b.name);
        return ok(g, 201);
      }),
      http.patch(`${B}/${path}/:id`, async ({ params, request }) => {
        const u = requireAdmin();
        if (u instanceof Response) return u;
        const g = list().find((x) => x.id === Number(params.id));
        if (!g) return err(404, "not_found", "group not found");
        const b = await body<Record<string, unknown>>(request);
        if (typeof b.name === "string" && list().some((x) => x.id !== g.id && x.name.toLowerCase() === (b.name as string).toLowerCase()))
          return err(409, "conflict", "name taken");
        Object.assign(g, b, { updated_at: nowIso() });
        return ok(g);
      }),
      http.delete(`${B}/${path}/:id`, ({ params }) => {
        const u = requireAdmin();
        if (u instanceof Response) return u;
        const arr = list();
        const i = arr.findIndex((x) => x.id === Number(params.id));
        if (i < 0) return err(404, "not_found", "group not found");
        arr.splice(i, 1);
        return noContent();
      }),
    ];
  }),

  /* address books */
  http.get(`${B}/address-books`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    const scope = p.get("scope") ?? "accessible";
    if (scope === "all" && u.role !== "admin") return err(403, "forbidden", "admin only");
    const q = p.get("q");
    const owner = p.get("owner_user_id");
    const kind = p.get("kind");
    const items = db.books
      .filter((b) => (scope === "all" ? true : b.owner_user_id === u.id || bookPermission(b, { ...u, role: "user" })))
      .filter((b) => (!q || contains(q, b.name, b.note, b.owner.username)) && (!owner || b.owner_user_id === Number(owner)))
      .filter((b) => !kind || (kind === "personal") === b.is_personal)
      .map((b) => bookView(b, u));
    return ok(paginate(sortBy(items, p.get("sort") ?? "name", { name: (b) => `${b.is_personal ? 0 : 1}${b.name}${b.owner.username}`, entry_count: (b) => b.entry_count, updated_at: (b) => Date.parse(b.updated_at) }), p));
  }),
  http.post(`${B}/address-books`, async ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["AddressBookCreate"]>(request);
    const owner = u.role === "admin" && b.owner_user_id ? b.owner_user_id : u.id;
    if (db.books.some((x) => x.owner_user_id === owner && x.name.toLowerCase() === b.name.toLowerCase())) return err(409, "conflict", "name taken");
    const nb: S["AddressBook"] = {
      id: newId(),
      guid: `ab-${nextId}`,
      owner_user_id: owner,
      owner: db.userRef(owner)!,
      name: b.name,
      note: b.note ?? "",
      is_personal: false,
      permission: "full",
      entry_count: 0,
      share_count: 0,
      has_default_password: !!b.default_password,
      created_at: nowIso(),
      updated_at: nowIso(),
    };
    db.books.push(nb);
    audit(u, "address_book.create", "address_book", nb.name);
    return ok(bookView(nb, u), 201);
  }),
  http.get(`${B}/address-books/:id`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = db.books.find((x) => x.id === Number(params.id));
    if (!b || !bookPermission(b, u)) return err(404, "not_found", "address book not found");
    db.refreshCounts();
    return ok(bookView(b, u));
  }),
  http.patch(`${B}/address-books/:id`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = db.books.find((x) => x.id === Number(params.id));
    if (!b || !bookPermission(b, u)) return err(404, "not_found", "address book not found");
    if (bookPermission(b, u) !== "full") return err(403, "forbidden", "full permission required");
    const p = await body<S["AddressBookUpdate"]>(request);
    if (p.name !== undefined) b.name = p.name;
    if (p.note !== undefined) b.note = p.note;
    if (p.default_password !== undefined) b.has_default_password = p.default_password !== null && p.default_password !== "";
    b.updated_at = nowIso();
    return ok(bookView(b, u));
  }),
  http.delete(`${B}/address-books/:id`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const i = db.books.findIndex((x) => x.id === Number(params.id));
    const b = db.books[i];
    if (!b || !bookPermission(b, u)) return err(404, "not_found", "address book not found");
    if (b.is_personal) return err(409, "personal_book", "personal books cannot be deleted");
    if (b.owner_user_id !== u.id && u.role !== "admin") return err(403, "forbidden", "owner only");
    db.books.splice(i, 1);
    audit(u, "address_book.delete", "address_book", b.name);
    return noContent();
  }),
  http.get(`${B}/address-books/:id/shares`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    return ok({ items: db.shares.filter((s) => s.address_book_id === Number(params.id)) });
  }),
  http.post(`${B}/address-books/:id/shares`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["AddressBookShareCreate"]>(request);
    const bookId = Number(params.id);
    if (db.shares.some((s) => s.address_book_id === bookId && s.grantee_type === b.grantee_type && s.grantee.id === b.grantee_id))
      return err(409, "conflict", "already shared");
    const name = b.grantee_type === "user" ? db.users.find((x) => x.id === b.grantee_id)?.username : db.userGroups.find((g) => g.id === b.grantee_id)?.name;
    const s: S["AddressBookShare"] = { id: newId(), address_book_id: bookId, grantee_type: b.grantee_type, grantee: { id: b.grantee_id, name: name ?? `#${b.grantee_id}` }, permission: b.permission, created_at: nowIso() };
    db.shares.push(s);
    db.refreshCounts();
    audit(u, "address_book.share", "address_book", String(bookId), { grantee: s.grantee.name, permission: s.permission });
    return ok(s, 201);
  }),
  http.patch(`${B}/address-books/:id/shares/:sid`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const s = db.shares.find((x) => x.id === Number(params.sid));
    if (!s) return err(404, "not_found", "share not found");
    s.permission = (await body<{ permission: S["Permission"] }>(request)).permission;
    return ok(s);
  }),
  http.delete(`${B}/address-books/:id/shares/:sid`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const i = db.shares.findIndex((x) => x.id === Number(params.sid));
    if (i >= 0) db.shares.splice(i, 1);
    db.refreshCounts();
    return noContent();
  }),
  http.get(`${B}/address-books/:id/entries`, ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const tagFilter = p.getAll("tag");
    const items = db.entries
      .filter((e) => e.address_book_id === Number(params.id))
      .filter((e) => !q || contains(q, e.rustdesk_id, e.alias, e.hostname, e.os_username, e.note))
      .filter((e) => tagFilter.every((t) => e.tags.includes(t)))
      .map((e) => {
        const d = db.devices.find((x) => x.rustdesk_id === e.rustdesk_id);
        return { ...e, online: d ? d.online : null, device_id: d?.id ?? null };
      });
    return ok(
      paginate(
        sortBy(items, p.get("sort") ?? "alias", {
          alias: (e) => (e.alias || e.hostname || e.rustdesk_id).toLowerCase(),
          rustdesk_id: (e) => e.rustdesk_id,
          hostname: (e) => e.hostname,
          updated_at: (e) => Date.parse(e.updated_at),
        }),
        p,
      ),
    );
  }),
  http.post(`${B}/address-books/:id/entries/bulk`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["EntryBulkRequest"]>(request);
    const bookId = Number(params.id);
    const ids = new Set(b.entry_ids);
    let affected = 0;
    for (const e of [...db.entries]) {
      if (!ids.has(e.id) || e.address_book_id !== bookId) continue;
      affected++;
      const tags = b.tags ?? [];
      if (b.action === "delete") db.entries.splice(db.entries.indexOf(e), 1);
      else if (b.action === "add_tags") e.tags = [...new Set([...e.tags, ...tags])];
      else if (b.action === "remove_tags") e.tags = e.tags.filter((t) => !tags.includes(t));
      else if (b.action === "set_tags") e.tags = [...tags];
      e.updated_at = nowIso();
    }
    ensureTags(bookId, b.tags ?? []);
    db.refreshCounts();
    return ok({ affected });
  }),
  http.post(`${B}/address-books/:id/entries/from-devices`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["EntriesFromDevicesRequest"]>(request);
    const bookId = Number(params.id);
    let affected = 0;
    let skipped = 0;
    for (const id of b.device_ids) {
      const d = db.devices.find((x) => x.id === id);
      if (!d) continue;
      if (db.entries.some((e) => e.address_book_id === bookId && e.rustdesk_id === d.rustdesk_id)) {
        skipped++;
        continue;
      }
      affected++;
      db.entries.push({
        id: newId(),
        address_book_id: bookId,
        rustdesk_id: d.rustdesk_id,
        alias: d.alias || d.hostname || "",
        hostname: d.hostname ?? "",
        os_username: d.os_username ?? "",
        platform: d.os_family === "windows" ? "Windows" : d.os_family === "macos" ? "Mac OS" : "Linux",
        login_name: "",
        note: null,
        tags: b.tags ?? [],
        has_password: false,
        force_always_relay: false,
        rdp_port: "",
        rdp_username: "",
        same_server: false,
        created_at: nowIso(),
        updated_at: nowIso(),
      });
    }
    ensureTags(bookId, b.tags ?? []);
    db.refreshCounts();
    return ok({ affected, skipped });
  }),
  http.post(`${B}/address-books/:id/entries`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["EntryCreate"]>(request);
    const bookId = Number(params.id);
    if (db.entries.some((e) => e.address_book_id === bookId && e.rustdesk_id === b.rustdesk_id)) return err(409, "conflict", "id already in book");
    const e: S["Entry"] = {
      id: newId(),
      address_book_id: bookId,
      rustdesk_id: b.rustdesk_id,
      alias: b.alias ?? "",
      os_username: b.os_username ?? "",
      hostname: b.hostname ?? "",
      platform: b.platform ?? "",
      login_name: b.login_name ?? "",
      note: b.note ?? null,
      tags: b.tags ?? [],
      has_password: !!b.password,
      force_always_relay: !!b.force_always_relay,
      rdp_port: b.rdp_port ?? "",
      rdp_username: b.rdp_username ?? "",
      same_server: !!b.same_server,
      created_at: nowIso(),
      updated_at: nowIso(),
    };
    db.entries.push(e);
    ensureTags(bookId, e.tags);
    db.refreshCounts();
    return ok(e, 201);
  }),
  http.patch(`${B}/address-books/:id/entries/:eid`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const e = db.entries.find((x) => x.id === Number(params.eid));
    if (!e) return err(404, "not_found", "entry not found");
    const { password, ...rest } = await body<S["EntryUpdate"]>(request);
    Object.assign(e, rest, { updated_at: nowIso() });
    if (password !== undefined) e.has_password = password !== null && password !== "";
    ensureTags(e.address_book_id, e.tags);
    db.refreshCounts();
    return ok(e);
  }),
  http.delete(`${B}/address-books/:id/entries/:eid`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const i = db.entries.findIndex((x) => x.id === Number(params.eid));
    if (i >= 0) db.entries.splice(i, 1);
    db.refreshCounts();
    return noContent();
  }),
  http.get(`${B}/address-books/:id/tags`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    db.refreshCounts();
    return ok({ items: db.tags.filter((t) => t.address_book_id === Number(params.id)).sort((a, b) => a.name.localeCompare(b.name)) });
  }),
  http.post(`${B}/address-books/:id/tags`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const b = await body<S["TagInput"]>(request);
    const bookId = Number(params.id);
    if (db.tags.some((t) => t.address_book_id === bookId && t.name === b.name)) return err(409, "conflict", "tag exists");
    const t: S["Tag"] = { id: newId(), address_book_id: bookId, name: b.name, color_argb: b.color_argb, entry_count: 0 };
    db.tags.push(t);
    return ok(t, 201);
  }),
  http.patch(`${B}/address-books/:id/tags/:tid`, async ({ params, request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const t = db.tags.find((x) => x.id === Number(params.tid));
    if (!t) return err(404, "not_found", "tag not found");
    const b = await body<S["TagPatch"]>(request);
    if (b.name && b.name !== t.name) {
      for (const e of db.entries.filter((x) => x.address_book_id === t.address_book_id)) e.tags = e.tags.map((n) => (n === t.name ? b.name! : n));
      t.name = b.name;
    }
    if (b.color_argb !== undefined) t.color_argb = b.color_argb;
    db.refreshCounts();
    return ok(t);
  }),
  http.delete(`${B}/address-books/:id/tags/:tid`, ({ params }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const i = db.tags.findIndex((x) => x.id === Number(params.tid));
    if (i >= 0) {
      const t = db.tags[i]!;
      for (const e of db.entries.filter((x) => x.address_book_id === t.address_book_id)) e.tags = e.tags.filter((n) => n !== t.name);
      db.tags.splice(i, 1);
    }
    return noContent();
  }),

  /* audit */
  http.get(`${B}/audit/connections`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const items = db.connections.filter(
      (c) =>
        (u.role === "admin" || db.devices.some((d) => d.id === c.device_id && d.owner_user_id === u.id)) &&
        inRange(c.started_at, p) &&
        (!q || contains(q, c.device_rustdesk_id, c.device_alias, c.controller_name, c.controller_rustdesk_id, c.controller_ip)) &&
        (!p.get("conn_type") || c.conn_type === Number(p.get("conn_type"))) &&
        (!p.get("device_id") || c.device_id === Number(p.get("device_id"))) &&
        (p.get("active") !== "true" || !c.ended_at),
    );
    return ok(paginate(sortBy(items, p.get("sort") ?? "-started_at", { started_at: (c) => Date.parse(c.started_at), ended_at: (c) => (c.ended_at ? Date.parse(c.ended_at) : null) }), p));
  }),
  http.get(`${B}/audit/files`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const items = db.fileAudits.filter(
      (f) =>
        inRange(f.created_at, p) &&
        (!q || contains(q, f.device_rustdesk_id, f.path, f.controller_name, f.controller_ip)) &&
        (!p.get("direction") || f.direction === Number(p.get("direction"))),
    );
    return ok(paginate(sortBy(items, p.get("sort") ?? "-created_at", { created_at: (f) => Date.parse(f.created_at) }), p));
  }),
  http.get(`${B}/audit/alarms`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const items = db.alarms.filter((a) => inRange(a.created_at, p) && (!q || contains(q, a.device_rustdesk_id, a.ip)));
    return ok(paginate(sortBy(items, p.get("sort") ?? "-created_at", { created_at: (a) => Date.parse(a.created_at) }), p));
  }),
  http.get(`${B}/audit/logins`, ({ request }) => {
    const u = requireAuth();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const items = db.logins.filter(
      (l) =>
        (u.role === "admin" || l.user?.id === u.id) &&
        inRange(l.created_at, p) &&
        (!q || contains(q, l.user?.username, l.username_attempted, l.ip, l.device_rustdesk_id)) &&
        (!p.get("result") || l.result === p.get("result")) &&
        (!p.get("method") || l.method === p.get("method")) &&
        (!p.get("client_kind") || l.client_kind === p.get("client_kind")),
    );
    return ok(paginate(sortBy(items, p.get("sort") ?? "-created_at", { created_at: (l) => Date.parse(l.created_at) }), p));
  }),
  http.get(`${B}/audit/admin`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    const action = p.get("action");
    const items = db.adminAudit.filter(
      (a) => inRange(a.created_at, p) && (!q || contains(q, a.actor_name, a.action, a.target_label)) && (!action || a.action.startsWith(action)),
    );
    return ok(paginate(sortBy(items, p.get("sort") ?? "-created_at", { created_at: (a) => Date.parse(a.created_at) }), p));
  }),

  /* server */
  http.get(`${B}/server/health`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(health());
  }),
  http.get(`${B}/server/settings`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(serverState());
  }),
  http.patch(`${B}/server/settings`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<S["ServerSettingsPatch"]>(request);
    Object.assign(db.serverDesired, b);
    Object.assign(db.serverApplied, b);
    audit(u, "server.settings", "server", Object.keys(b).join(","), b as Record<string, unknown>);
    return ok(serverState());
  }),
  http.post(`${B}/server/settings/reapply`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    Object.assign(db.serverApplied, db.serverDesired);
    return ok(serverState());
  }),
  http.get(`${B}/server/ip-blocker`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(paginate(db.ipBlocker, qs(request)));
  }),
  http.delete(`${B}/server/ip-blocker/:ip`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const i = db.ipBlocker.findIndex((x) => x.ip === params.ip);
    if (i >= 0) db.ipBlocker.splice(i, 1);
    return noContent();
  }),
  http.get(`${B}/server/ip-changes`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(paginate(db.ipChanges, qs(request)));
  }),
  http.delete(`${B}/server/ip-changes/:id`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const i = db.ipChanges.findIndex((x) => x.rustdesk_id === params.id);
    if (i >= 0) db.ipChanges.splice(i, 1);
    return noContent();
  }),
  http.get(`${B}/server/punch-requests`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = qs(request);
    const q = p.get("q");
    return ok(paginate(db.punchRequests.filter((r) => inRange(r.at, p) && (!q || contains(q, r.from_ip, r.to_rustdesk_id, r.to_ip))), p));
  }),
  http.get(`${B}/server/relay-usage`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const t = Date.now() / 1000;
    return ok({
      checked_at: nowIso(),
      items: [
        { peer: "185.12.40.11:51234", elapsed_s: 312 + Math.floor(t % 60), total_mb: 85.2, highest_kbps: 9830, avg_kbps: 2185, current_kbps: 1500 + Math.round(Math.sin(t) * 400) },
        { peer: "91.203.7.88:40022", elapsed_s: 1460, total_mb: 41.7, highest_kbps: 4410, avg_kbps: 228, current_kbps: 96 },
        { peer: "46.39.12.201:61007", elapsed_s: 75, total_mb: 3.1, highest_kbps: 1220, avg_kbps: 330, current_kbps: 410 },
      ],
    });
  }),
  http.get(`${B}/server/console/commands`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok({ items: db.consoleCommands });
  }),
  http.post(`${B}/server/console`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<S["ConsoleRequest"]>(request);
    const c = db.consoleCommands.find((x) => x.target === b.target && x.cmd === b.cmd);
    if (!c) return err(400, "validation_failed", "command not allowed");
    const outputs: Record<string, string> = {
      usage: "185.12.40.11:51234: 312s 85.20MB 9830kb/s 2185kb/s 1500kb/s\n91.203.7.88:40022: 1460s 41.70MB 4410kb/s 228kb/s 96kb/s\n",
      "ip-blocker": `${db.ipBlocker.length}\n${db.ipBlocker.map((x) => `${x.ip}: ${x.attempts}/60s ${x.ids}/86400s`).join("\n")}\n`,
      "relay-servers": `${(db.serverApplied.relay_servers ?? []).join("\n")}\n`,
      "must-login": b.args?.length ? "" : `MUST_LOGIN: ${db.serverApplied.must_login}\n`,
      "always-use-relay": b.args?.length ? "" : `ALWAYS_USE_RELAY: ${db.serverApplied.always_use_relay}\n`,
      blacklist: `${(db.serverApplied.blacklist ?? []).join("\n")}\n`,
      blocklist: `${(db.serverApplied.blocklist ?? []).join("\n")}\n`,
      "total-bandwidth": b.args?.length ? "" : `${db.serverApplied.total_bandwidth_mbps}Mb/s\n`,
    };
    const output = outputs[b.cmd] ?? "";
    db.consoleHistory.unshift({ id: newId(), actor_name: u.username, target: b.target, cmd: b.cmd, args: b.args ?? [], output_excerpt: output.slice(0, 80), error: null, created_at: nowIso() });
    return ok({ output, truncated: false, duration_ms: 4, executed_at: nowIso() });
  }),
  http.get(`${B}/server/console/history`, ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(paginate(db.consoleHistory, qs(request)));
  }),

  /* settings */
  http.get(`${B}/settings`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok(db.panelSettings);
  }),
  http.patch(`${B}/settings`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<S["PanelSettingsPatch"]>(request);
    const s = db.panelSettings;
    if (b.public_url !== undefined) s.public_url = b.public_url;
    if (b.branding) Object.assign(s.branding, b.branding);
    if (b.auth) Object.assign(s.auth, b.auth);
    if (b.retention) Object.assign(s.retention, b.retention);
    if (b.rate_limits) Object.assign(s.rate_limits, b.rate_limits);
    if (b.trusted_proxies) s.trusted_proxies = b.trusted_proxies;
    audit(u, "settings.update", "settings", Object.keys(b).join(","));
    return ok(s);
  }),
  http.put(`${B}/settings/logo`, () => err(400, "validation_failed", "logo upload is not supported in mock mode")),
  http.delete(`${B}/settings/logo`, () => noContent()),

  /* identity providers (stream B1 contract as assumed by web/src/api/identityProviders.ts) */
  http.get(`${B}/identity-providers`, () => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    return ok({ items: db.providers });
  }),
  http.post(`${B}/identity-providers`, async ({ request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const b = await body<IdentityProviderInput>(request);
    if (db.providers.some((p) => p.slug === b.slug)) return err(409, "conflict", "slug taken");
    const { client_secret, ...rest } = b;
    const p: IdentityProvider = {
      id: newId(),
      slug: b.slug ?? "oidc",
      kind: b.kind ?? "oidc",
      display_name: b.display_name ?? "",
      client_id: b.client_id ?? "",
      has_client_secret: !!client_secret,
      issuer: b.issuer ?? null,
      scopes: b.scopes ?? [],
      pkce_enabled: b.pkce_enabled ?? true,
      pkce_method: b.pkce_method ?? "S256",
      auto_register: !!b.auto_register,
      link_by_verified_email: !!b.link_by_verified_email,
      enabled: b.enabled ?? true,
      sort_order: b.sort_order ?? 0,
      callback_url: "https://desk.example.com/api/oidc/callback",
      created_at: nowIso(),
      updated_at: nowIso(),
      ...(rest as Partial<IdentityProvider>),
    };
    db.providers.push(p);
    return ok(p, 201);
  }),
  http.patch(`${B}/identity-providers/:id`, async ({ params, request }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = db.providers.find((x) => x.id === Number(params.id));
    if (!p) return err(404, "not_found", "provider not found");
    const { client_secret, ...rest } = await body<IdentityProviderInput>(request);
    Object.assign(p, rest, { updated_at: nowIso() });
    if (client_secret) p.has_client_secret = true;
    return ok(p);
  }),
  http.delete(`${B}/identity-providers/:id`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const i = db.providers.findIndex((x) => x.id === Number(params.id));
    if (i >= 0) db.providers.splice(i, 1);
    return noContent();
  }),
  http.post(`${B}/identity-providers/:id/test`, ({ params }) => {
    const u = requireAdmin();
    if (u instanceof Response) return u;
    const p = db.providers.find((x) => x.id === Number(params.id));
    if (!p) return err(404, "not_found", "provider not found");
    if (!p.has_client_secret) return ok({ ok: false, message: "Client secret is not configured." });
    return ok({
      ok: true,
      message: p.kind === "oidc" ? "Discovery document loaded; endpoints reachable." : `${p.display_name} OAuth endpoints reachable.`,
      issuer: p.issuer ?? undefined,
      authorization_endpoint: p.kind === "github" ? "https://github.com/login/oauth/authorize" : p.kind === "google" ? "https://accounts.google.com/o/oauth2/v2/auth" : `${p.issuer}/protocol/openid-connect/auth`,
    });
  }),

  /* SSE */
  http.get("/api/v1/events", () => {
    if (!me()) return err(401, "unauthorized", "authentication required");
    return new HttpResponse(liveStream(), {
      headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache", Connection: "keep-alive" },
    });
  }),
];

function ensureTags(bookId: number, names: string[]) {
  for (const n of names) {
    if (!db.tags.some((t) => t.address_book_id === bookId && t.name === n)) {
      db.tags.push({ id: newId(), address_book_id: bookId, name: n, color_argb: 0xff9e9e9e, entry_count: 0 });
    }
  }
}
