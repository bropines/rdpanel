// Other servers for the web client: offered by the admin (config) or added by
// the user (stored in this browser).

export interface OtherServer {
  /** Stable id: "admin:<n>" or "user:<random>". */
  id: string;
  name: string;
  idUrl: string;
  /** Empty: idUrl with /ws/relay. */
  relayUrl: string;
  key: string;
  source: "admin" | "user";
}

const USER_SERVERS_KEY = "rdpanel.webclient.servers";
const SELECTED_KEY = "rdpanel.webclient.server";

export function loadUserServers(): OtherServer[] {
  try {
    const raw = localStorage.getItem(USER_SERVERS_KEY);
    const list = raw ? (JSON.parse(raw) as OtherServer[]) : [];
    return Array.isArray(list) ? list.filter((s) => s && typeof s.idUrl === "string" && typeof s.key === "string") : [];
  } catch {
    return [];
  }
}

export function saveUserServers(list: OtherServer[]): void {
  try {
    localStorage.setItem(USER_SERVERS_KEY, JSON.stringify(list.map((s) => ({ ...s, source: "user" }))));
  } catch {
    /* storage unavailable */
  }
}

export function loadSelectedServer(): string {
  try {
    return localStorage.getItem(SELECTED_KEY) ?? "own";
  } catch {
    return "own";
  }
}

export function saveSelectedServer(id: string): void {
  try {
    localStorage.setItem(SELECTED_KEY, id);
  } catch {
    /* ignore */
  }
}

/** The relay URL of a server (explicit, else the ID URL's host with /ws/relay). */
export function relayUrlOf(s: Pick<OtherServer, "idUrl" | "relayUrl">): string {
  if (s.relayUrl) return s.relayUrl;
  try {
    const u = new URL(s.idUrl);
    return `${u.protocol}//${u.host}/ws/relay`;
  } catch {
    return s.idUrl.replace(/\/ws\/id\/?$/, "/ws/relay");
  }
}

/**
 * Turns what users type into WebSocket URLs: a full wss:// URL is kept, a
 * host (optionally with a RustDesk port, which a browser cannot use) becomes
 * wss://host/ws/id.
 */
export function idUrlFromInput(input: string): string {
  const v = input.trim();
  if (/^wss?:\/\//i.test(v)) return v;
  const host = v.replace(/^https?:\/\//i, "").replace(/\/.*$/, "").replace(/:\d+$/, "");
  return host ? `wss://${host}/ws/id` : "";
}

export function isValidServer(s: Pick<OtherServer, "idUrl" | "relayUrl" | "key">): boolean {
  const okUrl = (u: string) => {
    try {
      const p = new URL(u);
      return p.protocol === "wss:" && !!p.host;
    } catch {
      return false;
    }
  };
  if (!okUrl(s.idUrl) || (s.relayUrl && !okUrl(s.relayUrl))) return false;
  try {
    return atob(s.key.trim()).length === 32;
  } catch {
    return false;
  }
}

/** An IPv4 or IPv6 literal (with or without a port): browsers rarely get a valid certificate for one. */
export function isIpHost(host: string): boolean {
  const h = host.trim().replace(/^\[(.*)\](:\d+)?$/, "$1").replace(/^(\d+\.\d+\.\d+\.\d+):\d+$/, "$1");
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(h) || (h.includes(":") && /^[0-9a-f:.]+$/i.test(h));
}

/** The host part of what users type ("https://a.example/x", "a.example:21116" -> "a.example"). */
export function hostFromInput(input: string): string {
  const url = idUrlFromInput(input);
  try {
    return url ? new URL(url).host : "";
  } catch {
    return "";
  }
}

export interface DecodedConfig {
  host: string;
  relay: string;
  key: string;
  api: string;
}

/**
 * Decodes a RustDesk server config string (reversed URL-safe base64 of
 * {"host","relay","key","api"}), as exported by RustDesk clients and RD
 * Panel's Server page. Returns null when it is not one.
 */
export function decodeConfigString(input: string): DecodedConfig | null {
  const s = input.trim().replace(/^[=]+/, "");
  if (!s) return null;
  try {
    const reversed = s.split("").reverse().join("");
    const b64 = reversed.replace(/-/g, "+").replace(/_/g, "/");
    const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
    const json = JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(padded), (c) => c.charCodeAt(0)))) as Record<string, unknown>;
    const str = (k: string) => (typeof json[k] === "string" ? (json[k] as string) : "");
    if (!str("host") && !str("key")) return null;
    return { host: str("host"), relay: str("relay"), key: str("key"), api: str("api") };
  } catch {
    return null;
  }
}

/**
 * Form values for a decoded config string. Its host and relay are hbbs/hbbr
 * TCP addresses for native clients (often an IP address); the HTTPS API server
 * is what a browser can reach, and RD Panel serves /ws/id and /ws/relay there.
 */
export function serverFromConfig(c: DecodedConfig): { name: string; host: string; relay: string } {
  const api = /^https:\/\//i.test(c.api) ? hostFromInput(c.api) : "";
  if (api && !isIpHost(api)) return { name: api, host: api, relay: "" };
  const host = hostFromInput(c.host) || api;
  const relay = hostFromInput(c.relay);
  return { name: host, host, relay: relay && relay !== host && !isIpHost(relay) ? relay : "" };
}
