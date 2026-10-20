/**
 * MSW handlers for server share links: /api/v1/admin/share-links and the public
 * page data /s/{token}/info. Demo pages: /s/demo-share-link-token0 (valid) and
 * /s/expired-share-link-tok (used up → "no longer valid").
 */
import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import * as db from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";
const HOUR = 3600_000;
const DAY = 24 * HOUR;

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const err = (status: number, code: string, message: string, fields?: Record<string, string>) =>
  HttpResponse.json({ error: { code, message, ...(fields ? { fields } : {}) } }, { status });

interface MockLink {
  id: number;
  token: string;
  user_id: number;
  label: string;
  expires_at: string | null;
  max_downloads: number | null;
  views: number;
  downloads: number;
  last_used_at: string | null;
  created_at: string;
  revoked: boolean;
}

const future = (ms: number) => new Date(Date.now() + ms).toISOString();

const links: MockLink[] = [
  { id: 1, token: "demo-share-link-token0", user_id: 1, label: "Front desk PC", expires_at: null, max_downloads: null, views: 12, downloads: 3, last_used_at: db.iso(2 * HOUR), created_at: db.iso(20 * DAY), revoked: false },
  { id: 2, token: "contractor-q4-audit-ab", user_id: 1, label: "Contractor (Q4 audit)", expires_at: future(5 * DAY), max_downloads: 5, views: 4, downloads: 2, last_used_at: db.iso(DAY), created_at: db.iso(2 * DAY), revoked: false },
  { id: 3, token: "expired-share-link-tok", user_id: 1, label: "New laptop", expires_at: null, max_downloads: 1, views: 2, downloads: 1, last_used_at: db.iso(3 * DAY), created_at: db.iso(4 * DAY), revoked: false },
  { id: 4, token: "ivan-support-handoff-1", user_id: 2, label: "Support hand-off", expires_at: future(20 * HOUR), max_downloads: null, views: 1, downloads: 0, last_used_at: db.iso(HOUR), created_at: db.iso(4 * HOUR), revoked: false },
];
let nextId = 5;

/** Admins, and users whose group allows it. */
export function canShareMock(u: db.MockUser): boolean {
  if (u.status !== "active") return false;
  if (u.role === "admin") return true;
  return db.userGroups.some((g) => g.id === u.user_group_id && g.can_share_links);
}

function statusOf(l: MockLink): S["ShareLinkStatus"] {
  if (l.revoked) return "revoked";
  if (l.expires_at && Date.parse(l.expires_at) <= Date.now()) return "expired";
  if (l.max_downloads !== null && l.downloads >= l.max_downloads) return "exhausted";
  const owner = db.users.find((u) => u.id === l.user_id);
  return owner && canShareMock(owner) ? "active" : "suspended";
}

function dto(l: MockLink): S["ShareLink"] {
  const owner = db.users.find((u) => u.id === l.user_id);
  return {
    id: l.id,
    label: l.label,
    url: `${location.origin}/s/${l.token}`,
    status: statusOf(l),
    expires_at: l.expires_at,
    max_downloads: l.max_downloads,
    views: l.views,
    downloads: l.downloads,
    last_used_at: l.last_used_at,
    created_at: l.created_at,
    owner: { id: l.user_id, username: owner?.username ?? "deleted", display_name: owner?.display_name ?? "" },
  };
}

function newToken(): string {
  const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const bytes = crypto.getRandomValues(new Uint8Array(22));
  return Array.from(bytes, (b) => abc[b % 64]).join("");
}

const PUBLIC_ASSETS: [string, string, number][] = [
  ["macos-arm64", "rustdesk-1.5.0-aarch64.dmg", 29874038],
  ["macos-x64", "rustdesk-1.5.0-x86_64.dmg", 36878402],
  ["linux-deb-x64", "rustdesk-1.5.0-x86_64.deb", 24940796],
  ["linux-rpm-x64", "rustdesk-1.5.0-0.x86_64.rpm", 33732946],
  ["linux-appimage-x64", "rustdesk-1.5.0-x86_64.AppImage", 87992824],
  ["android-universal", "rustdesk-1.5.0-universal-signed.apk", 77451974],
];

export function shareHandlers(currentUser: () => db.MockUser | undefined) {
  const guard = (): Response | db.MockUser => {
    const u = currentUser();
    if (!u) return err(401, "unauthorized", "authentication required");
    if (!canShareMock(u)) return err(403, "forbidden", "your user group may not share server links");
    return u;
  };
  return [
    http.get(`${B}/share-links`, ({ request }) => {
      const u = guard();
      if (u instanceof Response) return u;
      const all = new URL(request.url).searchParams.get("scope") === "all";
      if (all && u.role !== "admin") return err(403, "forbidden", "administrator role required");
      const items = links
        .filter((l) => !l.revoked && (all || l.user_id === u.id))
        .sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at))
        .map(dto);
      return ok({ items });
    }),
    http.post(`${B}/share-links`, async ({ request }) => {
      const u = guard();
      if (u instanceof Response) return u;
      const b = (await request.json().catch(() => ({}))) as S["ShareLinkInput"];
      let expires: string | null = null;
      if (b.expires_in) expires = future({ "1h": HOUR, "24h": DAY, "7d": 7 * DAY, "30d": 30 * DAY }[b.expires_in]);
      if (b.expires_at) {
        const t = Date.parse(b.expires_at);
        if (Number.isNaN(t) || t < Date.now() + 60_000 || t > Date.now() + 366 * DAY)
          return err(400, "validation_failed", "invalid share link", { expires_at: "must be between one minute and one year from now" });
        expires = new Date(t).toISOString();
      }
      const l: MockLink = {
        id: nextId++,
        token: newToken(),
        user_id: u.id,
        label: (b.label ?? "").trim(),
        expires_at: expires,
        max_downloads: b.max_downloads ?? null,
        views: 0,
        downloads: 0,
        last_used_at: null,
        created_at: new Date().toISOString(),
        revoked: false,
      };
      links.push(l);
      return ok(dto(l), 201);
    }),
    http.delete(`${B}/share-links/:id`, ({ params }) => {
      const u = guard();
      if (u instanceof Response) return u;
      const l = links.find((x) => x.id === Number(params.id) && !x.revoked);
      if (!l || (u.role !== "admin" && l.user_id !== u.id)) return err(404, "not_found", "no such share link");
      l.revoked = true;
      return new HttpResponse(null, { status: 204 });
    }),

    // Public page data (no session).
    http.get("/s/:token/info", ({ params }) => {
      const l = links.find((x) => x.token === params.token);
      if (!l) return err(404, "not_found", "this link is not valid");
      if (statusOf(l) !== "active") return err(410, "gone", "this link is no longer valid");
      l.views++;
      l.last_used_at = new Date().toISOString();
      const info: S["SharePublicInfo"] = {
        title: db.panelSettings.share_links.page_title,
        description: db.panelSettings.share_links.description,
        id_server: "desk.example.com",
        relay_server: "desk.example.com",
        api_server: "https://desk.example.com",
        public_key: "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=",
        config_string: "=0nI9Q2bu5yZsFGbi5SaoBnL0VmZ6ISdy5yckVGZiojIp1WYiwiI",
        windows_file_name: "rustdesk-licensed-=0nI9Q2bu5yZsFGbi5SaoBnL0VmZ6ISdy5yckVGZiojIp1WYiwiI.exe",
        windows: [
          { arch: "x64", size: 25887600 },
          { arch: "arm64", size: 23857008 },
        ],
        version: "1.5.0",
        release_url: "https://github.com/rustdesk/rustdesk/releases/tag/1.5.0",
        assets: PUBLIC_ASSETS.map(([id, name, size]) => ({
          id,
          name,
          size,
          url: `https://github.com/rustdesk/rustdesk/releases/download/1.5.0/${name}`,
        })),
      };
      return ok(info);
    }),
  ];
}
