/**
 * MSW handlers for Server → Logs (`/server/logs*`): an in-memory buffer of
 * hbbs/hbbr/panel lines in their real formats; the SSE stream appends a new
 * line every 1–3 s while it is open.
 */
import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";

type Entry = Schemas["LogEntry"];
type Level = Schemas["LogLevel"];
const B = "/api/v1/admin";
const CAP = 2000;
const RANK: Record<Level, number> = { debug: 0, info: 1, warn: 2, error: 3 };

let seq = 0;
const buffer: Entry[] = [];

function hbbTime(d: Date): string {
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}.${p(d.getUTCMilliseconds(), 3)}000 +00:00`;
}

function push(source: Entry["source"], level: Level, message: string, loc = "src/rendezvous_server.rs:419", at = new Date()): Entry {
  const raw =
    source === "panel"
      ? JSON.stringify({ time: at.toISOString(), level: level.toUpperCase(), msg: message })
      : `[${hbbTime(at)}] ${level.toUpperCase()} [${loc}] ${message}`;
  const e: Entry = { seq: ++seq, time: at.toISOString(), source, level, message, raw };
  buffer.push(e);
  if (buffer.length > CAP * 3) buffer.splice(0, buffer.length - CAP * 3);
  return e;
}

const SAMPLES: [Entry["source"], Level, string, string?][] = [
  ["hbbs", "info", "update_pk 123456789 [::ffff:185.12.40.11]:51234 ", "src/peer.rs:120"],
  ["hbbs", "info", "Punch hole request from 987654321 to 123456789", "src/rendezvous_server.rs:688"],
  ["hbbs", "debug", "tcp connection from [::ffff:91.203.7.88]:40022, ws: false", "src/rendezvous_server.rs:1210"],
  ["hbbs", "warn", "Failed to send punch hole response: timed out", "src/rendezvous_server.rs:742"],
  ["hbbr", "info", "New relay request 7e1c… from [::ffff:46.39.12.201]:61007", "src/relay_server.rs:420"],
  ["hbbr", "info", "Relayrequest 7e1c… from [::ffff:46.39.12.201]:61007 got paired", "src/relay_server.rs:452"],
  ["hbbr", "error", "Relay of [::ffff:46.39.12.201]:61007 closed: Connection reset by peer", "src/relay_server.rs:530"],
  ["panel", "info", 'http request method=POST path=/api/heartbeat status=200 duration_ms=2 ip=185.12.40.11'],
  ["panel", "info", "presence: poll ok online=12 rtt_ms=1.4"],
  ["panel", "warn", 'login failed username=ivan reason=bad_password password=[REDACTED]'],
];

function seed() {
  if (buffer.length) return;
  const start = Date.now() - 20 * 60_000;
  push("hbbs", "info", "Key: [REDACTED]", "src/rendezvous_server.rs:2003", new Date(start));
  push("hbbs", "info", "Listening on tcp/udp [::]:21116", "src/rendezvous_server.rs:419", new Date(start + 5));
  push("hbbr", "info", "Listening on tcp :21117", "src/relay_server.rs:86", new Date(start + 7));
  push("panel", "info", "starting rdpanel version=0.3.0 commit=mock", undefined, new Date(start + 20));
  for (let i = 0; i < 260; i++) {
    const s = SAMPLES[i % SAMPLES.length]!;
    push(s[0], s[1], s[2], s[3], new Date(start + 30_000 + i * 4_500));
  }
  // A continuation (multi-line hbbs SQL log).
  push("hbbs", "debug", "create table if not exists peer (", undefined, new Date());
}

function matcher(u: URL) {
  const sources = (u.searchParams.get("source") ?? "").split(",").filter(Boolean);
  const level = (u.searchParams.get("level") ?? "debug") as Level;
  const q = (u.searchParams.get("q") ?? "").toLowerCase();
  const after = Number(u.searchParams.get("after") ?? 0);
  return (e: Entry) =>
    (!sources.length || sources.includes(e.source)) &&
    RANK[e.level] >= (RANK[level] ?? 0) &&
    (!q || e.raw.toLowerCase().includes(q)) &&
    e.seq > after;
}

export const logHandlers = [
  http.get(`${B}/server/logs`, ({ request }) => {
    seed();
    const u = new URL(request.url);
    const limit = Math.min(2000, Number(u.searchParams.get("limit") ?? 500) || 500);
    const before = Number(u.searchParams.get("before") ?? 0);
    const all = buffer.filter((e) => matcher(u)(e) && (!before || e.seq < before));
    const items = all.slice(Math.max(0, all.length - limit));
    const sources = (["hbbr", "hbbs", "panel"] as const).map((name) => ({ name, lines: Math.min(CAP, buffer.filter((e) => e.source === name).length) }));
    const body: Schemas["LogPage"] = {
      enabled: true,
      capture: "files",
      capacity: CAP,
      sources,
      items,
      next_before: all.length > items.length && items.length ? items[0]!.seq : null,
    };
    return HttpResponse.json(body);
  }),
  http.get(`${B}/server/logs/download`, ({ request }) => {
    seed();
    const m = matcher(new URL(request.url));
    const text = buffer.filter(m).map((e) => `[${e.source}] ${e.raw}`).join("\n") + "\n";
    return new HttpResponse(text, {
      headers: { "Content-Type": "text/plain; charset=utf-8", "Content-Disposition": `attachment; filename="rdpanel-logs-mock.txt"` },
    });
  }),
  http.get(`${B}/server/logs/stream`, ({ request }) => {
    seed();
    const url = new URL(request.url);
    const m = matcher(url);
    const replay = Number(url.searchParams.get("after") ?? 0) > 0;
    const enc = new TextEncoder();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const frame = (e: Entry) => enc.encode(`id: ${e.seq}\nevent: log\ndata: ${JSON.stringify(e)}\n\n`);
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(enc.encode("retry: 5000\n\n"));
        if (replay) for (const e of buffer) if (m(e)) controller.enqueue(frame(e));
        const step = () => {
          const s = SAMPLES[Math.floor(Math.random() * SAMPLES.length)]!;
          const e = push(s[0], s[1], s[2], s[3]);
          try {
            if (m(e)) controller.enqueue(frame(e));
          } catch {
            return; // closed
          }
          timer = setTimeout(step, 1000 + Math.random() * 2000);
        };
        timer = setTimeout(step, 1500);
      },
      cancel() {
        if (timer) clearTimeout(timer);
      },
    });
    return new HttpResponse(stream, { headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" } });
  }),
];
