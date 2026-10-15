/** Fake `/api/v1/events` stream: flips device presence every few seconds. */
import * as db from "@/mocks/data";

function frame(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;
}

export function liveStream(): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  let timer: ReturnType<typeof setTimeout> | undefined;
  return new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(enc.encode("retry: 3000\n: connected\n\n"));
      let tick = 0;
      const step = () => {
        tick += 1;
        const d = db.devices[Math.floor(Math.random() * db.devices.length)];
        if (d) {
          d.online = !d.online;
          const since = new Date().toISOString();
          d.online_since = since;
          d.last_seen_at = since;
          try {
            controller.enqueue(enc.encode(frame("presence.changed", { id: d.id, rustdesk_id: d.rustdesk_id, online: d.online, since, last_seen_ip: d.last_seen_ip })));
            if (tick % 5 === 0) controller.enqueue(enc.encode(": ping\n\n"));
          } catch {
            return; // stream closed
          }
        }
        timer = setTimeout(step, 2500 + Math.random() * 3500);
      };
      timer = setTimeout(step, 3000);
    },
    cancel() {
      if (timer) clearTimeout(timer);
    },
  });
}
