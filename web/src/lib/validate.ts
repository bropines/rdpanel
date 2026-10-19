/** Client-side validators mirroring the server rules (server re-validates everything). */

const V4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

export function isIPv4(s: string): boolean {
  return V4.test(s);
}

export function isIPv6(s: string): boolean {
  if (!s.includes(":")) return false;
  let str = s;
  const lastColon = s.lastIndexOf(":");
  const tail = s.slice(lastColon + 1);
  // An embedded IPv4 tail (::ffff:1.2.3.4) counts as two groups.
  if (tail.includes(".")) {
    if (!isIPv4(tail)) return false;
    str = `${s.slice(0, lastColon + 1)}0:0`;
  }
  const halves = str.split("::");
  if (halves.length > 2) return false;
  const parse = (h: string | undefined) => (h ? h.split(":") : []);
  const groups = [...parse(halves[0]), ...parse(halves[1])];
  if (!groups.every((g) => /^[0-9a-fA-F]{1,4}$/.test(g))) return false;
  return halves.length === 2 ? groups.length < 8 : groups.length === 8;
}

/** IP or CIDR prefix (v4/v6). */
export function isIpOrCidr(s: string): boolean {
  const [ip, bits, extra] = s.trim().split("/");
  if (extra !== undefined || !ip) return false;
  const v4 = isIPv4(ip);
  const v6 = !v4 && isIPv6(ip);
  if (!v4 && !v6) return false;
  if (bits === undefined) return true;
  if (!/^\d{1,3}$/.test(bits)) return false;
  const n = Number(bits);
  return v4 ? n <= 32 : n <= 128;
}

/** Explicit CIDR prefix (v4/v6) requiring a prefix length. */
export function isCIDR(s: string): boolean {
  const [ip, bits, extra] = s.trim().split("/");
  if (extra !== undefined || !ip || bits === undefined) return false;
  const v4 = isIPv4(ip);
  const v6 = !v4 && isIPv6(ip);
  if (!v4 && !v6) return false;
  if (!/^\d{1,3}$/.test(bits)) return false;
  const n = Number(bits);
  return v4 ? n <= 32 : n <= 128;
}

/** `host[:port]` without spaces or commas (hbbs relay-servers entry). */
export function isHostPort(s: string): boolean {
  const v = s.trim();
  if (!v || /[\s,]/.test(v)) return false;
  const m = /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+)(?::(\d{1,5}))?$/.exec(v);
  if (!m) return false;
  if (m[2] !== undefined && (Number(m[2]) < 1 || Number(m[2]) > 65535)) return false;
  const host = m[1] ?? "";
  if (host.startsWith("[")) return isIPv6(host.slice(1, -1));
  return !host.startsWith("-") && !host.endsWith("-") && !host.includes("..");
}

/** Splits a textarea (newline/comma separated) into trimmed non-empty unique lines. */
export function splitLines(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const v = raw.trim();
    if (v && !out.includes(v)) out.push(v);
  }
  return out;
}
