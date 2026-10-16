/**
 * Validates a post-login `next` target. Only same-origin relative paths are allowed:
 * must start with a single "/", no "//" or "/\" (protocol-relative), no scheme, no
 * control characters, and never the login page itself.
 */
export function safeNext(next: unknown, fallback = "/"): string {
  if (typeof next !== "string" || next.length === 0 || next.length > 2048) return fallback;
  if (!next.startsWith("/")) return fallback;
  if (next.startsWith("//") || next.startsWith("/\\")) return fallback;
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f\\]/.test(next)) return fallback;
  try {
    const u = new URL(next, "http://panel.invalid");
    if (u.origin !== "http://panel.invalid") return fallback;
    if (u.pathname === "/login") return fallback;
    return u.pathname + u.search + u.hash;
  } catch {
    return fallback;
  }
}

/** `next` targets served by the backend, not the SPA router (e.g. /api/v1/docs): need a full page load. */
export function isServerPath(next: string): boolean {
  return next.startsWith("/api/");
}
