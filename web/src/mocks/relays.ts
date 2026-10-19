/**
 * MSW handlers for Server → Relays (api/openapi.yaml `relays` tag): an in-memory
 * relay table with a computed rotation. A new agent "enrolls" ~4 s after its
 * token is created and connects right after approval.
 */
import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import { iso } from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";
const KEY_FP = "OeVuKk5n";

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const err = (status: number, code: string, message: string) => HttpResponse.json({ error: { code, message } }, { status });
const nowIso = () => new Date().toISOString();

function agentRow(over: Partial<S["RelayAgent"]> = {}): S["RelayAgent"] {
  return {
    enrolled: true,
    token_pending: false,
    require_approval: true,
    approved: true,
    approved_at: iso(86_400_000 * 3),
    enrolled_at: iso(86_400_000 * 3),
    enrolled_from_ip: "203.0.113.20",
    hostname: "relay-host",
    agent_version: "0.3.0",
    hbbr_version: "hbbr 1.1.16",
    arch: "amd64",
    connected: true,
    connected_at: iso(3_600_000),
    remote_ip: "203.0.113.20",
    last_seen_at: iso(5_000),
    desired_version: 4,
    acked_version: 4,
    acked_at: iso(3_600_000),
    ack_errors: {},
    status: {
      reported_at: iso(5_000),
      hbbr_up: true,
      hbbr_restarts: 0,
      key_fp: KEY_FP,
      key_matches: true,
      admin_rtt_ms: 0.4,
      config_version: 4,
      apply_errors: {},
      relays_active: 9,
      total_mb: 812.4,
      current_kbps: 120_000,
      rx_bps: 121_000_000,
      tx_bps: 119_500_000,
      load1: 0.31,
      mem_used_pct: 21,
    },
    ...over,
  };
}

function relay(over: Partial<S["Relay"]> & Pick<S["Relay"], "id" | "name" | "kind" | "public_host">): S["Relay"] {
  return {
    mgmt_host: null,
    region: null,
    weight: 1,
    in_rotation: true,
    admin_state: "enabled",
    health: "active",
    health_since: iso(86_400_000),
    last_probe_at: iso(20_000),
    last_probe_ms: 23.4,
    last_probe_error: null,
    public_reachable: null,
    rotation_weight: 0,
    created_at: iso(86_400_000 * 10),
    updated_at: iso(86_400_000),
    agent: null,
    ...over,
  };
}

export const relays: S["Relay"][] = [
  relay({ id: 1, name: "local", kind: "local", public_host: "rustdesk.example.com", region: "RU", last_probe_ms: 0.9 }),
  relay({ id: 2, name: "fra-1", kind: "agent", public_host: "relay-de.example.com", region: "DE", weight: 3, agent: agentRow({ hostname: "fra-1" }) }),
  relay({
    id: 3,
    name: "hel-2",
    kind: "agent",
    public_host: "relay-fi.example.com:21117",
    mgmt_host: "100.64.0.2:21117",
    public_reachable: true,
    region: "FI",
    health: "degraded",
    agent: agentRow({ hostname: "hel-2", connected: false, connected_at: null, last_seen_at: iso(240_000), status: null }),
  }),
  relay({
    id: 4,
    name: "partner",
    kind: "static",
    public_host: "relay.partner.example.net",
    health: "offline",
    last_probe_ms: null,
    last_probe_error: "timeout: no data relayed (wrong key, blocked IP, or not an hbbr?)",
  }),
];

export const prefixRules: S["RelayPrefixRule"][] = [
  {
    id: 1,
    cidr: "192.168.1.0/24",
    relay_host: "rustdesk.example.com",
    comment: "Local office",
    created_at: iso(86_400_000 * 5),
    updated_at: iso(86_400_000 * 5),
  },
  {
    id: 2,
    cidr: "10.0.0.0/8",
    relay_host: "relay-de.example.com",
    comment: "Internal VPN",
    created_at: iso(86_400_000 * 2),
    updated_at: iso(86_400_000 * 2),
  },
];

const options: S["RelayOptions"] = { require_approval: true, allow_private_hosts: false };
const overrides: Record<number, S["RelaySettingsValues"]> = { 2: { total_bandwidth_mbps: 500 } };
const globalProfile: S["RelaySettingsValues"] = {
  blacklist: [],
  blocklist: ["198.51.100.0"],
  limit_speed_mbps: 32,
  total_bandwidth_mbps: 1024,
  single_bandwidth_mbps: 128,
  downgrade_threshold: 0.66,
  downgrade_start_check_s: 1800,
};

function eligible(r: S["Relay"]) {
  return r.in_rotation && r.admin_state === "enabled" && r.weight > 0 && (r.health === "active" || r.health === "degraded");
}

function rotation(): S["RelayRotation"] {
  const members = relays.filter(eligible).map((r) => ({ id: r.id, name: r.name, host: r.public_host, weight: r.weight }));
  for (const r of relays) r.rotation_weight = members.find((m) => m.id === r.id)?.weight ?? 0;
  const servers = members.flatMap((m) => Array.from({ length: m.weight }, () => m.host));
  return { servers, members, fallback: false, shrunk: false, error: null, observed: servers, observed_error: null };
}

function list(): S["RelayList"] {
  const rot = rotation();
  return { items: relays, rotation: rot };
}

function find(id: string | readonly string[] | undefined) {
  return relays.find((r) => String(r.id) === String(id));
}

function enrollment(name: string): S["RelayEnrollment"] {
  const token = Array.from({ length: 43 }, () => "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"[Math.floor(Math.random() * 56)]).join("");
  const exp = new Date(Date.now() + 3_600_000).toISOString();
  return {
    token,
    expires_at: exp,
    panel_url: "https://desk-api.example.com",
    install_command: `curl -fsSL https://desk-api.example.com/agent/install.sh | sudo sh -s -- --token ${token}`,
    docker_run: [
      "docker run -d --name rdpanel-relay --network host --restart unless-stopped",
      "  --ulimit nofile=1048576:1048576 --stop-timeout 20",
      "  --log-opt max-size=20m --log-opt max-file=5",
      "  -e RDPANEL_MODE=agent",
      "  -e RDPANEL_AGENT_PANEL_URL=https://desk-api.example.com",
      `  -e RDPANEL_AGENT_ENROLL_TOKEN=${token}`,
      "  -v /opt/rdpanel-agent/data:/data",
      "  ghcr.io/bropines/rdpanel:latest",
    ].join(" \\\n"),
    compose: `services:\n  relay:\n    image: ghcr.io/bropines/rdpanel:latest\n    network_mode: host        # hbbr must see real client IPs\n    restart: unless-stopped\n    environment:\n      RDPANEL_MODE: agent\n      RDPANEL_AGENT_PANEL_URL: https://desk-api.example.com\n      RDPANEL_AGENT_ENROLL_TOKEN: "${token}"   # one-time, expires ${exp}\n    volumes:\n      - ./relay-data:/data\n# relay: ${name}\n`,
  };
}

/** Simulates the agent enrolling (and, once approved, connecting). */
function simulateAgent(r: S["Relay"]) {
  setTimeout(() => {
    if (!r.agent || !r.agent.token_pending) return;
    r.agent = agentRow({ hostname: r.name, require_approval: options.require_approval, approved: !options.require_approval, connected: !options.require_approval, desired_version: 1, acked_version: options.require_approval ? 0 : 1, status: null });
    r.health = options.require_approval ? "pending" : "active";
  }, 4000);
}

export const relayHandlers = [
  http.get(`${B}/relays`, () => ok(list())),
  http.get(`${B}/relays/rotation`, () => ok(rotation())),
  http.get(`${B}/relays/options`, () => ok(options)),
  http.put(`${B}/relays/options`, async ({ request }) => {
    Object.assign(options, (await request.json()) as S["RelayOptions"]);
    return ok(options);
  }),
  http.get(`${B}/relays/prefix-rules`, () => ok(prefixRules)),
  http.post(`${B}/relays/prefix-rules`, async ({ request }) => {
    const b = (await request.json()) as S["RelayPrefixRuleInput"];
    if (!b.cidr || !b.relay_host) return err(400, "validation_failed", "cidr and relay_host are required");
    const nextId = prefixRules.length ? Math.max(...prefixRules.map((r) => r.id)) + 1 : 1;
    const rule: S["RelayPrefixRule"] = {
      id: nextId,
      cidr: b.cidr,
      relay_host: b.relay_host,
      comment: b.comment ?? "",
      created_at: nowIso(),
      updated_at: nowIso(),
    };
    prefixRules.push(rule);
    return ok(rule, 201);
  }),
  http.get(`${B}/relays/prefix-rules/countries`, () =>
    ok([
      { code: "RU", name: "Russia", prefix_count: 20 },
      { code: "DE", name: "Germany", prefix_count: 15 },
      { code: "NL", name: "Netherlands", prefix_count: 12 },
      { code: "US", name: "United States", prefix_count: 12 },
      { code: "KZ", name: "Kazakhstan", prefix_count: 8 },
      { code: "BY", name: "Belarus", prefix_count: 8 },
      { code: "CN", name: "China", prefix_count: 10 },
      { code: "GB", name: "United Kingdom", prefix_count: 8 },
      { code: "FR", name: "France", prefix_count: 8 },
    ]),
  ),
  http.post(`${B}/relays/prefix-rules/import-country`, async ({ request }) => {
    const b = (await request.json()) as S["ImportCountryPrefixRulesInput"];
    if (!b.country_code || !b.relay_host) return err(400, "validation_failed", "country_code and relay_host are required");
    const code = b.country_code.toUpperCase();
    const cidrs = code === "RU" ? ["95.165.0.0/16", "178.62.0.0/16", "194.87.0.0/16"] : ["85.10.192.0/18", "88.198.0.0/16"];
    let added = 0;
    for (const c of cidrs) {
      if (!prefixRules.some((r) => r.cidr === c)) {
        const nextId = prefixRules.length ? Math.max(...prefixRules.map((r) => r.id)) + 1 : 1;
        prefixRules.push({
          id: nextId,
          cidr: c,
          relay_host: b.relay_host,
          comment: b.comment || `[${code}] Country`,
          created_at: nowIso(),
          updated_at: nowIso(),
        });
        added++;
      }
    }
    return ok({
      country_code: code,
      relay_host: b.relay_host,
      added_count: added,
      total_rules: prefixRules.length,
    });
  }),
  http.delete(`${B}/relays/prefix-rules/:id`, ({ params }) => {
    const id = Number(params.id);
    const idx = prefixRules.findIndex((r) => r.id === id);
    if (idx === -1) return err(404, "not_found", "rule not found");
    prefixRules.splice(idx, 1);
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`${B}/relays`, async ({ request }) => {
    const b = (await request.json()) as S["RelayCreate"];
    if (relays.some((r) => r.name === b.name)) return err(409, "conflict", "a relay with this name already exists");
    if (!b.public_host || /[\s,]/.test(b.public_host)) return err(400, "validation_failed", "public_host: invalid relay server");
    const id = Math.max(0, ...relays.map((r) => r.id)) + 1;
    const r = relay({
      id,
      name: b.name,
      kind: b.kind,
      public_host: b.public_host,
      mgmt_host: b.mgmt_host ?? null,
      region: b.region ?? null,
      weight: b.weight ?? 1,
      in_rotation: b.in_rotation ?? true,
      health: "pending",
      health_since: nowIso(),
      last_probe_at: null,
      last_probe_ms: null,
      created_at: nowIso(),
      updated_at: nowIso(),
    });
    let enr: S["RelayEnrollment"] | null = null;
    if (b.kind === "agent") {
      enr = enrollment(b.name);
      r.agent = agentRow({ enrolled: false, token_pending: true, enroll_expires_at: enr.expires_at, approved: false, approved_at: null, enrolled_at: null, enrolled_from_ip: null, connected: false, connected_at: null, last_seen_at: null, hostname: null, agent_version: null, hbbr_version: null, arch: null, desired_version: 0, acked_version: 0, require_approval: options.require_approval, status: null });
      simulateAgent(r);
    }
    relays.push(r);
    rotation();
    return ok({ relay: r, enrollment: enr }, 201);
  }),
  http.get(`${B}/relays/:id`, ({ params }) => {
    const r = find(params.id);
    return r ? ok(r) : err(404, "not_found", "not found");
  }),
  http.patch(`${B}/relays/:id`, async ({ params, request }) => {
    const r = find(params.id);
    if (!r) return err(404, "not_found", "not found");
    const b = (await request.json()) as S["RelayPatch"];
    if (b.weight !== undefined && (b.weight < 0 || b.weight > 5)) return err(400, "validation_failed", "weight must be 0-5");
    Object.assign(r, b, { updated_at: nowIso() });
    if (b.mgmt_host === null) r.mgmt_host = null;
    rotation();
    return ok(r);
  }),
  http.delete(`${B}/relays/:id`, ({ params }) => {
    const i = relays.findIndex((r) => String(r.id) === String(params.id));
    if (i < 0) return err(404, "not_found", "not found");
    if (relays[i]?.kind === "local") return err(400, "validation_failed", "the local relay cannot be deleted; disable it instead");
    relays.splice(i, 1);
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`${B}/relays/:id/enroll-token`, ({ params }) => {
    const r = find(params.id);
    if (!r?.agent) return err(400, "validation_failed", "not an agent relay");
    const e = enrollment(r.name);
    r.agent = { ...r.agent, enrolled: false, token_pending: true, enroll_expires_at: e.expires_at, approved: false, connected: false, revoked_at: null, status: null };
    r.health = "pending";
    simulateAgent(r);
    return ok(e);
  }),
  http.post(`${B}/relays/:id/approve`, ({ params }) => {
    const r = find(params.id);
    if (!r?.agent?.enrolled) return err(400, "validation_failed", "the agent has not enrolled yet");
    r.agent = { ...r.agent, approved: true, approved_at: nowIso(), connected: true, connected_at: nowIso(), desired_version: 2, acked_version: 2, status: agentRow().status };
    r.health = "active";
    r.last_probe_at = nowIso();
    r.last_probe_ms = 31.2;
    return ok(r);
  }),
  http.post(`${B}/relays/:id/revoke`, ({ params }) => {
    const r = find(params.id);
    if (!r?.agent) return err(400, "validation_failed", "not an agent relay");
    r.agent = { ...r.agent, approved: false, connected: false, revoked_at: nowIso() };
    r.health = "pending";
    return ok(r);
  }),
  http.post(`${B}/relays/:id/probe`, ({ params }) => {
    const r = find(params.id);
    if (!r) return err(404, "not_found", "not found");
    r.last_probe_at = nowIso();
    if (r.kind !== "static") {
      r.last_probe_ms = 15 + Math.random() * 30;
      r.last_probe_error = null;
    }
    return ok(r);
  }),
  http.get(`${B}/relays/:id/settings`, ({ params }) => {
    const id = Number(params.id);
    const ov = overrides[id] ?? {};
    return ok({ overrides: ov, global: globalProfile, effective: { ...globalProfile, ...Object.fromEntries(Object.entries(ov).filter(([, v]) => v != null)) } });
  }),
  http.put(`${B}/relays/:id/settings`, async ({ params, request }) => {
    const id = Number(params.id);
    const r = find(params.id);
    if (r?.kind !== "agent") return err(400, "validation_failed", "per-relay settings apply to agent relays only");
    const b = (await request.json()) as S["RelaySettingsValues"];
    overrides[id] = b;
    if (r.agent) r.agent = { ...r.agent, desired_version: r.agent.desired_version + 1, acked_version: r.agent.desired_version + 1 };
    return ok({ overrides: b, global: globalProfile, effective: { ...globalProfile, ...Object.fromEntries(Object.entries(b).filter(([, v]) => v != null)) } });
  }),
  http.get(`${B}/relays/:id/usage`, ({ params }) => {
    const r = find(params.id);
    if (!r || (r.kind === "agent" && !r.agent?.connected) || r.kind === "static") return err(503, "server_unavailable", "live usage is not available");
    const t = Date.now() / 1000;
    return ok({
      checked_at: nowIso(),
      items: [
        { peer: "185.12.40.11:51234", elapsed_s: 312, total_mb: 85.2, highest_kbps: 9830, avg_kbps: 2185, current_kbps: 1500 + Math.round(Math.sin(t) * 400) },
        { peer: "91.203.7.88:40022", elapsed_s: 1460, total_mb: 41.7, highest_kbps: 4410, avg_kbps: 228, current_kbps: 96 },
      ],
    });
  }),
  http.get(`${B}/relays/:id/traffic`, ({ params, request }) => {
    const r = find(params.id);
    if (!r) return err(404, "not_found", "relay not found");
    const url = new URL(request.url);
    const hours = Math.min(168, Math.max(1, parseInt(url.searchParams.get("hours") || "24", 10)));
    const now = Date.now();
    const items: S["RelayTrafficPoint"][] = [];
    let totalBytes = 0;
    let totalSessions = 0;
    let maxActive = 0;
    for (let i = hours - 1; i >= 0; i--) {
      const t = new Date(now - i * 3600_000);
      t.setMinutes(0, 0, 0);
      const b = i % 3 === 0 ? 0 : 5_000_000 + ((i * 1234567) % 20_000_000);
      const s = b > 0 ? (i % 5) + 1 : 0;
      const a = s > 0 ? Math.min(s, 3) : 0;
      items.push({
        hour_start: t.toISOString(),
        bytes: b,
        sessions: s,
        max_active: a,
      });
      totalBytes += b;
      totalSessions += s;
      if (a > maxActive) maxActive = a;
    }
    return ok({
      relay_id: r.id,
      hours,
      total_bytes: totalBytes,
      total_sessions: totalSessions,
      max_active: maxActive,
      items,
    });
  }),
];
