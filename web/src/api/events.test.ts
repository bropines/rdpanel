import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { backoffDelay, connectEvents, DEFAULT_BACKOFF, type EventSourceLike } from "@/api/events";

class FakeSource implements EventSourceLike {
  static instances: FakeSource[] = [];
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  closed = false;
  listeners = new Map<string, ((ev: MessageEvent) => void)[]>();
  constructor(readonly url: string) {
    FakeSource.instances.push(this);
  }
  addEventListener(type: string, l: (ev: MessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), l]);
  }
  close() {
    this.closed = true;
  }
  open() {
    this.onopen?.(new Event("open"));
  }
  fail() {
    this.onerror?.(new Event("error"));
  }
  emit(type: string, data: string) {
    for (const l of this.listeners.get(type) ?? []) l(new MessageEvent(type, { data }));
  }
}

const last = () => FakeSource.instances[FakeSource.instances.length - 1]!;

describe("backoffDelay", () => {
  it("grows exponentially and caps at max", () => {
    const mid = () => 0.5; // no jitter offset
    expect(backoffDelay(0, DEFAULT_BACKOFF, mid)).toBe(1000);
    expect(backoffDelay(1, DEFAULT_BACKOFF, mid)).toBe(2000);
    expect(backoffDelay(3, DEFAULT_BACKOFF, mid)).toBe(8000);
    expect(backoffDelay(20, DEFAULT_BACKOFF, mid)).toBe(30_000);
  });

  it("applies bounded jitter", () => {
    expect(backoffDelay(2, DEFAULT_BACKOFF, () => 0)).toBe(2800);
    expect(backoffDelay(2, DEFAULT_BACKOFF, () => 1)).toBe(5200);
  });
});

describe("connectEvents", () => {
  beforeEach(() => {
    FakeSource.instances = [];
    vi.useFakeTimers();
  });
  afterEach(() => vi.useRealTimers());

  it("dispatches typed events and ignores malformed payloads", () => {
    const onEvent = vi.fn();
    connectEvents({ url: "/e", onEvent, createSource: (u) => new FakeSource(u), random: () => 0.5 });
    const s = last();
    s.open();
    s.emit("presence.changed", JSON.stringify({ id: 1, rustdesk_id: "1", online: true, since: "t" }));
    s.emit("hbbs.health", "{not json");
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith("presence.changed", { id: 1, rustdesk_id: "1", online: true, since: "t" });
  });

  it("reconnects with backoff after errors and reports a reconnect after re-open", () => {
    const statuses: string[] = [];
    const onReconnect = vi.fn();
    connectEvents({
      url: "/e",
      onEvent: () => undefined,
      onStatus: (s) => statuses.push(s),
      onReconnect,
      createSource: (u) => new FakeSource(u),
      random: () => 0.5,
    });
    const first = last();
    first.open();
    expect(onReconnect).not.toHaveBeenCalled();

    first.fail();
    expect(first.closed).toBe(true);
    expect(FakeSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(999);
    expect(FakeSource.instances).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(FakeSource.instances).toHaveLength(2);

    // Second failure before opening: delay doubles.
    last().fail();
    vi.advanceTimersByTime(1999);
    expect(FakeSource.instances).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(FakeSource.instances).toHaveLength(3);

    last().open();
    expect(onReconnect).toHaveBeenCalledTimes(1);

    // Backoff resets after a successful open.
    last().fail();
    vi.advanceTimersByTime(1000);
    expect(FakeSource.instances).toHaveLength(4);
    expect(statuses).toEqual(["connecting", "open", "reconnecting", "reconnecting", "reconnecting", "reconnecting", "open", "reconnecting", "reconnecting"]);
  });

  it("stops for good when closed (no pending reconnect)", () => {
    const close = connectEvents({ url: "/e", onEvent: () => undefined, createSource: (u) => new FakeSource(u), random: () => 0.5 });
    last().fail();
    close();
    vi.advanceTimersByTime(60_000);
    expect(FakeSource.instances).toHaveLength(1);
  });

  it("ignores events from a superseded source", () => {
    const onEvent = vi.fn();
    connectEvents({ url: "/e", onEvent, createSource: (u) => new FakeSource(u), random: () => 0.5 });
    const old = last();
    old.fail();
    vi.advanceTimersByTime(1000);
    old.onerror?.(new Event("error")); // stale handler must not schedule another reconnect
    vi.advanceTimersByTime(60_000);
    expect(FakeSource.instances).toHaveLength(2);
  });
});
