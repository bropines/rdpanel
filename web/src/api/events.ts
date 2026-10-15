/**
 * SSE client for `GET /api/v1/events` (contract: api/openapi.yaml `x-sse-events`).
 * Native EventSource retries are not used: on any error the source is closed and
 * re-opened with exponential backoff + jitter, so HTTP errors (e.g. 502 during a
 * restart) do not end the stream for good.
 */
import { EVENTS_URL, type Schemas } from "@/api/client";

export interface PanelEvents {
  "presence.changed": Schemas["PresenceChangedEvent"];
  "device.updated": Schemas["Device"];
  "audit.connection": Schemas["ConnectionAudit"];
  "hbbs.health": Schemas["ServerHealth"];
}
export type PanelEventName = keyof PanelEvents;
export const PANEL_EVENT_NAMES: PanelEventName[] = [
  "presence.changed",
  "device.updated",
  "audit.connection",
  "hbbs.health",
];

export type StreamStatus = "connecting" | "open" | "reconnecting" | "closed";

/** The subset of EventSource we use (makes the client testable). */
export interface EventSourceLike {
  onopen: ((ev: Event) => unknown) | null;
  onerror: ((ev: Event) => unknown) | null;
  addEventListener(type: string, listener: (ev: MessageEvent) => void): void;
  close(): void;
}

export interface BackoffOptions {
  initialMs: number;
  maxMs: number;
  factor: number;
  /** 0..1 — fraction of the delay that is randomised. */
  jitter: number;
}

export const DEFAULT_BACKOFF: BackoffOptions = { initialMs: 1000, maxMs: 30_000, factor: 2, jitter: 0.3 };

/** Delay before reconnect attempt `attempt` (0-based). */
export function backoffDelay(attempt: number, o: BackoffOptions = DEFAULT_BACKOFF, random = Math.random): number {
  const base = Math.min(o.maxMs, o.initialMs * o.factor ** Math.max(0, attempt));
  const spread = base * o.jitter;
  return Math.round(base - spread + random() * spread * 2);
}

export interface ConnectOptions {
  url?: string;
  onEvent: <K extends PanelEventName>(name: K, data: PanelEvents[K]) => void;
  onStatus?: (status: StreamStatus) => void;
  /** Called on every successful re-open after the first (events may have been missed). */
  onReconnect?: () => void;
  backoff?: BackoffOptions;
  createSource?: (url: string) => EventSourceLike;
  random?: () => number;
}

/** Opens the stream; returns a function that closes it for good. */
export function connectEvents(opts: ConnectOptions): () => void {
  const url = opts.url ?? EVENTS_URL;
  const create =
    opts.createSource ?? ((u: string) => new EventSource(u, { withCredentials: true }) as unknown as EventSourceLike);
  let source: EventSourceLike | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let attempt = 0;
  let everOpened = false;
  let stopped = false;

  const open = () => {
    if (stopped) return;
    opts.onStatus?.(everOpened || attempt > 0 ? "reconnecting" : "connecting");
    const s = create(url);
    source = s;
    s.onopen = () => {
      if (stopped || source !== s) return;
      attempt = 0;
      opts.onStatus?.("open");
      if (everOpened) opts.onReconnect?.();
      everOpened = true;
    };
    s.onerror = () => {
      if (stopped || source !== s) return;
      s.close();
      source = undefined;
      opts.onStatus?.("reconnecting");
      const delay = backoffDelay(attempt, opts.backoff, opts.random);
      attempt += 1;
      timer = setTimeout(open, delay);
    };
    for (const name of PANEL_EVENT_NAMES) {
      s.addEventListener(name, (ev: MessageEvent) => {
        if (stopped) return;
        try {
          opts.onEvent(name, JSON.parse(String(ev.data)) as PanelEvents[typeof name]);
        } catch {
          /* malformed payload: ignore */
        }
      });
    }
  };

  open();
  return () => {
    stopped = true;
    if (timer) clearTimeout(timer);
    source?.close();
    source = undefined;
    opts.onStatus?.("closed");
  };
}
