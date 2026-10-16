/**
 * Deterministic in-memory dataset for the mock API (npm run dev:mock).
 * ≈50 devices (mixed online), 3 users, groups, books with tags, a week of audit.
 */
import type { Schemas } from "@/api/client";
import type { IdentityProvider } from "@/api/identityProviders";

type S = Schemas;

/** Small seeded PRNG (mulberry32) so screenshots are stable. */
export function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const rand = rng(20261015);
const pick = <T>(arr: readonly T[]): T => arr[Math.floor(rand() * arr.length)]!;
const int = (min: number, max: number) => min + Math.floor(rand() * (max - min + 1));

export const NOW = Date.now();
export const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString();
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

/* ── Users & groups ───────────────────────────────────────────────────── */

export const userGroups: S["UserGroup"][] = [
  { id: 1, name: "Engineering", visibility: "shared", note: "Dev & ops team", member_count: 2, created_at: iso(200 * DAY), updated_at: iso(30 * DAY) },
  { id: 2, name: "Support", visibility: "self", note: "First-line support", member_count: 1, created_at: iso(180 * DAY), updated_at: iso(60 * DAY) },
];

export const deviceGroups: S["DeviceGroup"][] = [
  { id: 1, name: "Office", note: "Workstations in the Moscow office", device_count: 0, created_at: iso(190 * DAY), updated_at: iso(20 * DAY) },
  { id: 2, name: "Servers", note: "Datacenter hosts", device_count: 0, created_at: iso(170 * DAY), updated_at: iso(10 * DAY) },
  { id: 3, name: "Home", note: "", device_count: 0, created_at: iso(90 * DAY), updated_at: iso(90 * DAY) },
];

export type MockUser = S["UserDetail"] & { password: string; locale?: "en" | "ru" | null };

const userBase = (u: Partial<MockUser> & Pick<MockUser, "id" | "username" | "display_name" | "role">): MockUser => ({
  email: null,
  avatar_url: "",
  note: "",
  status: "active",
  user_group_id: null,
  user_group: null,
  has_password: true,
  totp_enabled: false,
  totp_required: false,
  email_verified: false,
  email_2fa_enabled: false,
  last_login_at: iso(int(1, 72) * HOUR),
  created_at: iso(int(100, 300) * DAY),
  updated_at: iso(int(1, 30) * DAY),
  device_count: 0,
  is_last_admin: false,
  identities: [],
  address_book_count: 0,
  password: "password",
  ...u,
});

export const users: MockUser[] = [
  userBase({
    id: 1,
    username: "admin",
    display_name: "Alexei Volkov",
    email: "alexei@example.com",
    role: "admin",
    user_group_id: 1,
    user_group: { id: 1, name: "Engineering" },
    totp_enabled: true,
    password: "admin",
    last_login_at: iso(5 * MIN),
    identities: [
      {
        id: 1,
        provider_id: 1,
        provider_slug: "github",
        provider_kind: "github",
        provider_name: "GitHub",
        subject: "1048576",
        email: "alexei@example.com",
        email_verified: true,
        username: "avolkov",
        display_name: "Alexei Volkov",
        avatar_url: "",
        last_login_at: iso(3 * DAY),
        created_at: iso(120 * DAY),
      },
    ],
  }),
  userBase({
    id: 2,
    username: "ivan",
    display_name: "Ivan Sokolov",
    email: "ivan@example.com",
    role: "user",
    user_group_id: 1,
    user_group: { id: 1, name: "Engineering" },
    totp_enabled: true,
    password: "ivan",
  }),
  userBase({
    id: 3,
    username: "maria",
    display_name: "Maria Kuznetsova",
    email: "maria@example.com",
    role: "user",
    user_group_id: 2,
    user_group: { id: 2, name: "Support" },
    totp_required: true,
    password: "maria",
    identities: [
      {
        id: 2,
        provider_id: 2,
        provider_slug: "google",
        provider_kind: "google",
        provider_name: "Google",
        subject: "112233445566778899",
        email: "maria@example.com",
        email_verified: true,
        username: null,
        display_name: "Maria Kuznetsova",
        avatar_url: "",
        last_login_at: iso(1 * DAY),
        created_at: iso(40 * DAY),
      },
    ],
  }),
];

export const userRef = (id: number | null | undefined): S["UserRef"] | null => {
  const u = users.find((x) => x.id === id);
  return u ? { id: u.id, username: u.username, display_name: u.display_name } : null;
};

/* ── Devices ──────────────────────────────────────────────────────────── */

const OS: { family: S["OsFamily"]; names: string[] }[] = [
  { family: "windows", names: ["Windows 11 Pro 23H2", "Windows 10 Pro 22H2", "Windows Server 2022", "Windows 11 Home 24H2"] },
  { family: "linux", names: ["Ubuntu 24.04 LTS", "Debian GNU/Linux 12", "Fedora 40", "Arch Linux"] },
  { family: "macos", names: ["macOS 14.6 Sonoma", "macOS 15.1 Sequoia"] },
  { family: "android", names: ["Android 14", "Android 13"] },
];
const VERSIONS = ["1.4.2", "1.4.2", "1.4.1", "1.4.1", "1.4.0", "1.3.9", "1.3.8"];
const HOST_PREFIX = ["ws", "lab", "srv", "pc", "nb", "kiosk", "build", "db", "gw", "media"];
const CPUS = ["Intel Core i7-12700", "AMD Ryzen 7 5800X", "Intel Xeon E-2388G", "Apple M2", "AMD EPYC 7313P", "Snapdragon 8 Gen 2", "Intel Core i5-1135G7"];
const ALIASES = ["Reception", "Accounting PC", "Build agent", "NAS", "CEO laptop", "Meeting room", "Warehouse terminal", "Backup host", "Grafana", "Printer server"];

export type MockDevice = S["Device"] & { kickRequested?: boolean };

export const devices: MockDevice[] = Array.from({ length: 50 }, (_, i) => {
  const id = i + 1;
  const os = pick(OS);
  const online = rand() < 0.58;
  const owner = rand() < 0.82 ? pick([1, 1, 2, 2, 3]) : null;
  const group = rand() < 0.7 ? pick([1, 1, 2, 3]) : null;
  const host = `${pick(HOST_PREFIX)}-${String(int(1, 99)).padStart(2, "0")}${os.family === "android" ? "-mobile" : ""}`;
  const lastSeenAgo = online ? int(0, 40) * 1000 : pick([int(2, 59) * MIN, int(1, 23) * HOUR, int(1, 20) * DAY, int(25, 120) * DAY]);
  const rid = String(int(100_000_000, 1_999_999_999));
  const d: MockDevice = {
    id,
    rustdesk_id: rid,
    uuid: btoa(`uuid-${rid}-${id}`).replace(/=+$/, ""),
    hostname: host,
    os: pick(os.names),
    os_family: os.family,
    cpu: pick(CPUS),
    memory: `${pick([4, 8, 16, 32, 64])} GB`,
    os_username: pick(["user", "admin", "ivan", "maria", "operator", "root", "Администратор"]),
    client_version: pick(VERSIONS),
    alias: rand() < 0.35 ? pick(ALIASES) : "",
    note: rand() < 0.2 ? pick(["Rack B4, slot 12", "Replace SSD in Q1", "Shared kiosk — do not reboot", "VPN only"]) : "",
    owner_user_id: owner,
    owner: userRef(owner),
    device_group_id: group,
    device_group: group ? { id: group, name: deviceGroups[group - 1]!.name } : null,
    online,
    online_since: iso(online ? int(5, 600) * MIN : lastSeenAgo),
    last_seen_at: iso(lastSeenAgo),
    last_seen_ip: `${pick(["185.12", "91.203", "10.0", "192.168", "46.39"])}.${int(0, 255)}.${int(1, 254)}`,
    last_heartbeat_at: iso(lastSeenAgo + int(0, 20) * 1000),
    sysinfo_at: iso(int(1, 30) * DAY),
    created_at: iso(int(30, 400) * DAY),
    updated_at: iso(int(0, 30) * DAY),
  };
  return d;
});

export function refreshCounts() {
  for (const u of users) {
    u.device_count = devices.filter((d) => d.owner_user_id === u.id).length;
    u.address_book_count = books.filter((b) => b.owner_user_id === u.id).length;
  }
  for (const g of deviceGroups) g.device_count = devices.filter((d) => d.device_group_id === g.id).length;
  for (const g of userGroups) g.member_count = users.filter((u) => u.user_group_id === g.id).length;
  const admins = users.filter((u) => u.role === "admin" && u.status === "active");
  for (const u of users) u.is_last_admin = admins.length === 1 && admins[0]!.id === u.id;
  for (const b of books) {
    b.entry_count = entries.filter((e) => e.address_book_id === b.id).length;
    b.share_count = shares.filter((s) => s.address_book_id === b.id).length;
  }
  for (const t of tags) t.entry_count = entries.filter((e) => e.address_book_id === t.address_book_id && e.tags.includes(t.name)).length;
}

/* ── Address books ────────────────────────────────────────────────────── */

const bookBase = (b: Partial<S["AddressBook"]> & Pick<S["AddressBook"], "id" | "name" | "owner_user_id" | "is_personal">): S["AddressBook"] => ({
  guid: `ab-${b.id}-${(b.id * 7919).toString(36)}`,
  owner: userRef(b.owner_user_id)!,
  note: "",
  permission: "full",
  entry_count: 0,
  share_count: 0,
  has_default_password: false,
  created_at: iso(int(30, 200) * DAY),
  updated_at: iso(int(0, 20) * DAY),
  ...b,
});

export const books: S["AddressBook"][] = [
  bookBase({ id: 1, name: "My address book", owner_user_id: 1, is_personal: true }),
  bookBase({ id: 2, name: "My address book", owner_user_id: 2, is_personal: true }),
  bookBase({ id: 3, name: "My address book", owner_user_id: 3, is_personal: true }),
  bookBase({ id: 4, name: "Datacenter", owner_user_id: 1, is_personal: false, note: "Production hosts", has_default_password: true }),
  bookBase({ id: 5, name: "Support clients", owner_user_id: 3, is_personal: false, note: "Customer machines" }),
  bookBase({ id: 6, name: "Family", owner_user_id: 2, is_personal: false }),
];

export const shares: S["AddressBookShare"][] = [
  { id: 1, address_book_id: 4, grantee_type: "group", grantee: { id: 1, name: "Engineering" }, permission: "read_write", created_at: iso(50 * DAY) },
  { id: 2, address_book_id: 4, grantee_type: "user", grantee: { id: 3, name: "maria" }, permission: "read", created_at: iso(20 * DAY) },
  { id: 3, address_book_id: 5, grantee_type: "group", grantee: { id: 2, name: "Support" }, permission: "full", created_at: iso(40 * DAY) },
  { id: 4, address_book_id: 5, grantee_type: "user", grantee: { id: 1, name: "admin" }, permission: "read_write", created_at: iso(10 * DAY) },
];

const TAG_DEFS: [number, string, number][] = [
  [1, "work", 0xff2196f3],
  [1, "home", 0xff4caf50],
  [1, "linux", 0xffff9800],
  [2, "lab", 0xff9c27b0],
  [3, "vip", 0xfff44336],
  [4, "prod", 0xfff44336],
  [4, "staging", 0xffffc107],
  [4, "db", 0xff3f51b5],
  [4, "edge", 0xff009688],
  [5, "priority", 0xffe91e63],
  [5, "windows", 0xff03a9f4],
  [5, "pending", 0xff9e9e9e],
  [6, "parents", 0xff8bc34a],
];

export const tags: S["Tag"][] = TAG_DEFS.map(([bookId, name, color], i) => ({
  id: i + 1,
  address_book_id: bookId,
  name,
  color_argb: color,
  entry_count: 0,
}));

export const entries: S["Entry"][] = [];
{
  let id = 1;
  const counts: Record<number, number> = { 1: 12, 2: 6, 3: 5, 4: 18, 5: 14, 6: 4 };
  for (const book of books) {
    const bookTags = tags.filter((t) => t.address_book_id === book.id).map((t) => t.name);
    const pool = [...devices].sort(() => rand() - 0.5).slice(0, counts[book.id] ?? 5);
    for (const d of pool) {
      const tagCount = bookTags.length ? int(0, Math.min(2, bookTags.length)) : 0;
      const etags = [...bookTags].sort(() => rand() - 0.5).slice(0, tagCount);
      entries.push({
        id: id++,
        address_book_id: book.id,
        rustdesk_id: d.rustdesk_id,
        alias: d.alias || (rand() < 0.4 ? pick(ALIASES) : ""),
        os_username: d.os_username ?? "",
        hostname: d.hostname ?? "",
        platform: d.os_family === "windows" ? "Windows" : d.os_family === "macos" ? "Mac OS" : d.os_family === "android" ? "Android" : "Linux",
        login_name: "",
        note: rand() < 0.15 ? "Ask before connecting" : null,
        tags: etags,
        has_password: rand() < 0.45,
        force_always_relay: rand() < 0.1,
        rdp_port: "",
        rdp_username: "",
        same_server: false,
        online: d.online,
        device_id: d.id,
        created_at: iso(int(10, 200) * DAY),
        updated_at: iso(int(0, 10) * DAY + int(0, 23) * HOUR),
      });
    }
  }
}

/* ── Audit ────────────────────────────────────────────────────────────── */

const CONTROLLERS = [
  { id: "128 744 019", name: "Alexei Volkov", ip: "185.12.40.11" },
  { id: "301 552 870", name: "Ivan Sokolov", ip: "91.203.7.88" },
  { id: "722 019 443", name: "Maria Kuznetsova", ip: "46.39.12.201" },
  { id: "999 102 554", name: "helpdesk", ip: "10.0.4.12" },
].map((c) => ({ ...c, id: c.id.replace(/\s/g, "") }));

export const connections: S["ConnectionAudit"][] = Array.from({ length: 140 }, (_, i) => {
  const d = pick(devices);
  const c = pick(CONTROLLERS);
  const started = i < 3 ? int(2, 40) * MIN : int(10, 7 * 24 * 60) * MIN;
  const dur = int(1, 120) * MIN;
  const active = i < 3;
  return {
    id: 140 - i,
    guid: `c0ffee${(1000 + i).toString(16)}-${i}`,
    device_id: d.id,
    device_rustdesk_id: d.rustdesk_id,
    device_alias: d.alias || d.hostname || null,
    controller_rustdesk_id: c.id,
    controller_name: c.name,
    controller_ip: c.ip,
    conn_type: pick([0, 0, 0, 0, 1, 1, 2, 3, 4] as const),
    session_id: String(int(1e9, 9e9)) + String(int(1e8, 9e8)),
    note: rand() < 0.1 ? "Scheduled maintenance" : null,
    started_at: iso(started),
    ended_at: active ? null : iso(Math.max(0, started - dur)),
  };
}).sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at));

const PATHS = ["C:\\Users\\user\\Desktop\\report.xlsx", "/home/ivan/backup.tar.gz", "D:\\Projects\\invoice-2026-10.pdf", "/var/log/syslog", "C:\\Temp\\drivers", "/Users/maria/Pictures"];
export const fileAudits: S["FileTransferAudit"][] = Array.from({ length: 48 }, (_, i) => {
  const d = pick(devices);
  const c = pick(CONTROLLERS);
  const path = pick(PATHS);
  const isFile = !/drivers|Pictures/.test(path);
  return {
    id: 48 - i,
    device_id: d.id,
    device_rustdesk_id: d.rustdesk_id,
    device_alias: d.alias || d.hostname || null,
    controller_rustdesk_id: c.id,
    controller_name: c.name,
    controller_ip: c.ip,
    direction: pick([0, 1] as const),
    is_file: isFile,
    path,
    file_count: isFile ? 1 : int(2, 40),
    info: { files: isFile ? [[path.split(/[\\/]/).pop(), int(10_000, 90_000_000)]] : [] },
    created_at: iso(int(5, 7 * 24 * 60) * MIN),
  };
}).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));

export const alarms: S["AlarmAudit"][] = Array.from({ length: 7 }, (_, i) => {
  const d = pick(devices);
  return {
    id: 7 - i,
    device_id: d.id,
    device_rustdesk_id: d.rustdesk_id,
    alarm_type: pick([0, 1, 2, 6]),
    conn_id: int(1, 30),
    ip: `${int(1, 223)}.${int(0, 255)}.${int(0, 255)}.${int(1, 254)}`,
    info: { ip: "198.51.100.23", attempts: int(5, 40) },
    created_at: iso(int(1, 6 * 24) * HOUR),
  };
});

export const logins: S["LoginEvent"][] = Array.from({ length: 70 }, (_, i) => {
  const failed = rand() < 0.15;
  const u = pick(users);
  const method = pick(["password", "password", "password", "passkey", "oidc", "webauth"] as const);
  const kind = pick(["web", "client", "client"] as const);
  return {
    id: 70 - i,
    user: failed && rand() < 0.5 ? null : userRef(u.id),
    username_attempted: failed ? pick([u.username, "root", "administrator"]) : null,
    method,
    provider_slug: method === "oidc" ? pick(["github", "google"]) : null,
    result: failed ? ("failure" as const) : ("success" as const),
    failure_reason: failed ? pick(["invalid_credentials", "totp_invalid", "rate_limited"]) : null,
    client_kind: kind,
    device_rustdesk_id: kind === "client" ? pick(devices).rustdesk_id : null,
    platform: kind === "client" ? pick(["Windows", "Linux", "Mac OS", "Android"]) : null,
    ip: pick(CONTROLLERS).ip,
    user_agent: kind === "web" ? "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36" : null,
    created_at: iso(i < 2 ? int(1, 30) * MIN : int(30, 7 * 24 * 60) * MIN),
  };
}).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));

const ACTIONS: [string, string, () => string, Record<string, unknown>][] = [
  ["device.update", "device", () => pick(devices).rustdesk_id, { alias: "Reception" }],
  ["device.delete", "device", () => String(int(100_000_000, 999_999_999)), {}],
  ["device.kick", "device", () => pick(devices).rustdesk_id, {}],
  ["user.update", "user", () => pick(users).username, { role: "user" }],
  ["user.password_reset", "user", () => pick(users).username, { revoke_sessions: true }],
  ["address_book.share", "address_book", () => pick(books).name, { permission: "read" }],
  ["server.settings", "server", () => "hbbr", { total_bandwidth_mbps: 1024 }],
  ["settings.update", "settings", () => "branding", { title: "RD Panel" }],
  ["identity_provider.update", "identity_provider", () => "github", { enabled: true }],
];
export const adminAudit: S["AdminAuditEvent"][] = Array.from({ length: 45 }, (_, i) => {
  const [action, type, target, details] = pick(ACTIONS);
  const label = target();
  return {
    id: 45 - i,
    actor_user_id: i % 9 === 0 ? null : 1,
    actor_name: i % 9 === 0 ? "cli" : "admin",
    actor_ip: i % 9 === 0 ? null : "185.12.40.11",
    action,
    target_type: type,
    target_id: label,
    target_label: label,
    details,
    created_at: iso(int(10, 10 * 24 * 60) * MIN),
  };
}).sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));

/* ── Sessions ─────────────────────────────────────────────────────────── */

export const sessionsByUser: Record<number, S["Session"][]> = {
  1: [
    { id: 101, kind: "web", user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0 Safari/537.36", ip: "185.12.40.11", created_at: iso(2 * HOUR), last_used_at: iso(1 * MIN), expires_at: iso(-10 * HOUR), current: true },
    { id: 102, kind: "web", user_agent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) Safari/604.1", ip: "46.39.12.7", created_at: iso(2 * DAY), last_used_at: iso(5 * HOUR), expires_at: iso(-7 * HOUR), current: false },
    { id: 103, kind: "client", device_rustdesk_id: devices[0]!.rustdesk_id, client_platform: "Windows", ip: "185.12.40.11", created_at: iso(30 * DAY), last_used_at: iso(20 * MIN), expires_at: iso(-150 * DAY), current: false },
    { id: 104, kind: "client", device_rustdesk_id: devices[3]!.rustdesk_id, client_platform: "Linux", ip: "10.0.4.12", created_at: iso(12 * DAY), last_used_at: iso(3 * DAY), expires_at: iso(-170 * DAY), current: false },
  ],
  2: [
    { id: 201, kind: "client", device_rustdesk_id: devices[5]!.rustdesk_id, client_platform: "Mac OS", ip: "91.203.7.88", created_at: iso(8 * DAY), last_used_at: iso(2 * HOUR), expires_at: iso(-170 * DAY), current: false },
  ],
  3: [],
};

/* ── Identity providers ───────────────────────────────────────────────── */

export const providers: IdentityProvider[] = [
  {
    id: 1,
    slug: "github",
    kind: "github",
    display_name: "GitHub",
    client_id: "Iv1.8a61f9b3a7aba766",
    has_client_secret: true,
    issuer: null,
    scopes: ["read:user", "user:email"],
    pkce_enabled: true,
    pkce_method: "S256",
    auto_register: false,
    link_by_verified_email: true,
    enabled: true,
    sort_order: 1,
    callback_url: "https://desk.example.com/api/oidc/callback",
    created_at: iso(200 * DAY),
    updated_at: iso(10 * DAY),
  },
  {
    id: 2,
    slug: "google",
    kind: "google",
    display_name: "Google",
    client_id: "1234567890-abcdefghijk.apps.googleusercontent.com",
    has_client_secret: true,
    issuer: null,
    scopes: ["openid", "email", "profile"],
    pkce_enabled: true,
    pkce_method: "S256",
    auto_register: true,
    link_by_verified_email: true,
    enabled: true,
    sort_order: 2,
    callback_url: "https://desk.example.com/api/oidc/callback",
    created_at: iso(150 * DAY),
    updated_at: iso(40 * DAY),
  },
  {
    id: 3,
    slug: "keycloak",
    kind: "oidc",
    display_name: "Company SSO",
    client_id: "rdpanel",
    has_client_secret: false,
    issuer: "https://sso.example.com/realms/main",
    scopes: ["openid", "email", "profile"],
    pkce_enabled: true,
    pkce_method: "S256",
    auto_register: false,
    link_by_verified_email: false,
    enabled: false,
    sort_order: 3,
    callback_url: "https://desk.example.com/api/oidc/callback",
    created_at: iso(20 * DAY),
    updated_at: iso(20 * DAY),
  },
];

/* ── Server (hbbs/hbbr) ───────────────────────────────────────────────── */

export const serverDesired: S["ServerSettingsValues"] = {
  relay_servers: ["relay.example.com:21117"],
  always_use_relay: false,
  must_login: true,
  tcp_keepalive: true,
  webrtc: true,
  blacklist: ["203.0.113.50"],
  blocklist: ["198.51.100.0/24", "192.0.2.77"],
  limit_speed_mbps: 32,
  total_bandwidth_mbps: 1024,
  single_bandwidth_mbps: 128,
  downgrade_threshold: 0.66,
  downgrade_start_check_s: 1800,
};
export const serverApplied: S["ServerSettingsValues"] = { ...serverDesired, total_bandwidth_mbps: 512, blacklist: [] };

/** Client options pushed in heartbeats (mutated by the mock PUT). */
export const clientOptions: S["ClientOptions"] = {
  ice_servers: "stun:stun.example.com:3478\nturn://rdpanel:example-secret@turn.example.com:3478",
  webrtc_congestion_control: null,
  pushed: true,
  modified_at: "2026-10-14T09:30:00Z",
};

export const consoleCommands: S["ConsoleCommand"][] = [
  { target: "hbbs", cmd: "relay-servers", alias: "rs", args_hint: "[host:port,…]", arg_pattern: "[A-Za-z0-9.:,\\[\\]-]+", max_args: 1, description: "Show or set relay servers", mutating: true },
  { target: "hbbs", cmd: "ip-blocker", alias: "ib", args_hint: "[<ip>|<offset>] [-]", arg_pattern: "[0-9a-fA-F.:]+|-", max_args: 2, description: "Show or unblock rate-limited IPs", mutating: true },
  { target: "hbbs", cmd: "ip-changes", alias: "ic", args_hint: "[<id>|<offset>] [-]", arg_pattern: "[A-Za-z0-9_-]+", max_args: 2, description: "IDs registered from several IPs", mutating: false },
  { target: "hbbs", cmd: "punch-requests", alias: "pr", args_hint: "[start] [count]", arg_pattern: "\\d+", max_args: 2, description: "Recent punch requests", mutating: false },
  { target: "hbbs", cmd: "always-use-relay", alias: "aur", args_hint: "[Y|N]", arg_pattern: "[YyNn]", max_args: 1, description: "Force relayed connections", mutating: true },
  { target: "hbbs", cmd: "must-login", alias: "ml", args_hint: "[Y|N]", arg_pattern: "[YyNn]", max_args: 1, description: "Require login to connect", mutating: true },
  { target: "hbbs", cmd: "tcp-keepalive", alias: "tk", args_hint: "[Y|N]", arg_pattern: "[YyNn]", max_args: 1, description: "Keepalive for TCP/WebSocket clients", mutating: true },
  { target: "hbbs", cmd: "webrtc", alias: "wr", args_hint: "[Y|N]", arg_pattern: "[YyNn]", max_args: 1, description: "WebRTC signaling for RustDesk 1.5+ clients", mutating: true },
  { target: "hbbr", cmd: "usage", alias: "u", args_hint: "", max_args: 0, description: "Active relays with bandwidth", mutating: false },
  { target: "hbbr", cmd: "blacklist", alias: "b", args_hint: "[ip]", arg_pattern: "[0-9a-fA-F.:/]+", max_args: 1, description: "Throttled IPs", mutating: false },
  { target: "hbbr", cmd: "blocklist", alias: "B", args_hint: "[ip]", arg_pattern: "[0-9a-fA-F.:/]+", max_args: 1, description: "Refused IPs", mutating: false },
  { target: "hbbr", cmd: "total-bandwidth", alias: "tb", args_hint: "[Mb/s]", arg_pattern: "\\d+(\\.\\d+)?", max_args: 1, description: "Global bandwidth limit", mutating: true },
  { target: "hbbr", cmd: "single-bandwidth", alias: "sb", args_hint: "[Mb/s]", arg_pattern: "\\d+(\\.\\d+)?", max_args: 1, description: "Per-relay bandwidth limit", mutating: true },
  { target: "hbbr", cmd: "limit-speed", alias: "ls", args_hint: "[Mb/s]", arg_pattern: "\\d+(\\.\\d+)?", max_args: 1, description: "Speed for blacklisted relays", mutating: true },
];

export const consoleHistory: S["ConsoleHistoryItem"][] = [
  { id: 3, actor_name: "admin", target: "hbbr", cmd: "usage", args: [], output_excerpt: "185.12.40.11:51234: 312s 85.20MB …", error: null, created_at: iso(40 * MIN) },
  { id: 2, actor_name: "admin", target: "hbbs", cmd: "ip-blocker", args: [], output_excerpt: "2", error: null, created_at: iso(3 * HOUR) },
  { id: 1, actor_name: "admin", target: "hbbs", cmd: "must-login", args: ["Y"], output_excerpt: "", error: null, created_at: iso(2 * DAY) },
];

export const ipBlocker: S["IpBlockerEntry"][] = [
  { ip: "203.0.113.17", attempts: 41, attempts_window_s: 60, ids: 12, ids_window_s: 86400, blocked: true },
  { ip: "198.51.100.200", attempts: 7, attempts_window_s: 60, ids: 312, ids_window_s: 86400, blocked: true },
  { ip: "192.0.2.10", attempts: 3, attempts_window_s: 60, ids: 1, ids_window_s: 86400, blocked: false },
];

export const ipChanges: S["IpChangesEntry"][] = devices.slice(0, 6).map((d, i) => ({
  rustdesk_id: d.rustdesk_id,
  seconds: (i + 1) * 3600 + 120,
  ips: { [d.last_seen_ip ?? "10.0.0.1"]: int(1, 8), [`100.64.${i}.${int(2, 200)}`]: int(1, 4) },
}));

export const punchRequests: S["PunchRequest"][] = Array.from({ length: 64 }, (_, i) => {
  const d = pick(devices);
  return {
    id: 64 - i,
    at: iso(i * int(3, 25) * MIN),
    from_ip: pick(CONTROLLERS).ip,
    to_rustdesk_id: d.rustdesk_id,
    to_ip: d.last_seen_ip ?? null,
  };
});

/* ── Panel settings ───────────────────────────────────────────────────── */

export const panelSettings: S["PanelSettings"] = {
  public_url: "https://desk.example.com",
  admin_url: "https://desk-admin.example.com",
  branding: {
    title: "RD Panel",
    welcome_markdown:
      "Welcome back, **{{username}}**!\n\n- Download clients: [rustdesk.com](https://rustdesk.com/)\n- ID server: `desk.example.com`, key in the *Server* page\n- Questions? Write to `#it-support`.",
    has_logo: false,
    web_client_url: null,
  },
  auth: {
    password_login_enabled: true,
    require_admin_2fa: true,
    require_email_2fa: false,
    session_idle_hours: 12,
    session_absolute_days: 7,
    client_token_lifetime_hours: 4380,
  },
  retention: {
    login_events_days: 180,
    connection_audits_days: 365,
    file_transfer_audits_days: 365,
    alarm_audits_days: 365,
    admin_audit_days: 0,
    punch_requests_days: 30,
  },
  rate_limits: { login_free_attempts: 3, login_base_delay_seconds: 1, login_max_delay_seconds: 900, device_requests_per_second: 50, device_burst: 300 },
  trusted_proxies: ["127.0.0.1", "::1"],
};

refreshCounts();
