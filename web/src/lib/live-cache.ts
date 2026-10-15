/**
 * Pure cache patchers applied when SSE events arrive, so lists update in place
 * without refetching (and without losing pagination/scroll).
 */
import type { Schemas } from "@/api/client";

type Device = Schemas["Device"];
type Page<T> = { items: T[]; total: number; page: number; page_size: number };

/** Applies `presence.changed` to a device (no-op when the id does not match). */
export function applyPresence(d: Device, ev: Schemas["PresenceChangedEvent"]): Device {
  if (d.id !== ev.id) return d;
  return {
    ...d,
    online: ev.online,
    online_since: ev.since,
    // Going offline means "last seen" is the moment presence ended; going online is "now".
    last_seen_at: ev.since,
    last_seen_ip: ev.last_seen_ip ?? d.last_seen_ip ?? null,
  };
}

/** Patches one device inside a page; returns the same object when nothing changed. */
export function patchDevicePage(
  page: Page<Device> | undefined,
  id: number,
  fn: (d: Device) => Device,
): Page<Device> | undefined {
  if (!page) return page;
  let changed = false;
  const items = page.items.map((d) => {
    if (d.id !== id) return d;
    changed = true;
    return fn(d);
  });
  return changed ? { ...page, items } : page;
}

/** Merges a full `device.updated` payload, keeping embedded detail fields. */
export function mergeDevice<T extends Device>(current: T, update: Device): T {
  return current.id === update.id ? { ...current, ...update } : current;
}
