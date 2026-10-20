// Per-browser web client preferences and remembered device passwords.
import { DEFAULT_SETTINGS, decodeBase64, encodeBase64, type InputMode, type ScaleMode, type SessionSettings } from "rdweb";

export interface ViewPrefs {
  scaleMode: ScaleMode;
  /** null = pick by device (touch screens: trackpad) */
  inputMode: InputMode | null;
  zoomCursor: boolean;
  showStats: boolean;
}

export interface WebClientPrefs {
  settings: SessionSettings;
  view: ViewPrefs;
}

const PREFS_KEY = "rdpanel.webclient.prefs";
const PW_PREFIX = "rdpanel.webclient.pw.";
const RECENT_KEY = "rdpanel.webclient.recent";

export const DEFAULT_VIEW: ViewPrefs = { scaleMode: "fit", inputMode: null, zoomCursor: false, showStats: false };

function read<T>(key: string): T | null {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function write(key: string, value: unknown): void {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* storage unavailable */
  }
}

export function loadPrefs(): WebClientPrefs {
  const p = read<Partial<WebClientPrefs>>(PREFS_KEY);
  return {
    // view-only is a per-session choice, never restored
    settings: { ...DEFAULT_SETTINGS, ...(p?.settings ?? {}), viewOnly: false },
    view: { ...DEFAULT_VIEW, ...(p?.view ?? {}) },
  };
}

export function savePrefs(p: WebClientPrefs): void {
  write(PREFS_KEY, p);
}

/**
 * Remembered passwords are stored like the desktop client does: the salted
 * hash SHA256(password || salt), bound to the server key and device id. It
 * logs in to that device but does not reveal the password.
 */
function pwKey(serverKey: string, peerId: string): string {
  return `${PW_PREFIX}${serverKey.slice(0, 16)}.${peerId}`;
}

export function loadRememberedHash(serverKey: string, peerId: string): Uint8Array | undefined {
  const v = read<string>(pwKey(serverKey, peerId));
  if (!v) return undefined;
  try {
    return decodeBase64(v);
  } catch {
    return undefined;
  }
}

export function saveRememberedHash(serverKey: string, peerId: string, hash: Uint8Array | null): void {
  write(pwKey(serverKey, peerId), hash ? encodeBase64(hash) : null);
}

export function hasRememberedHash(serverKey: string, peerId: string): boolean {
  return read<string>(pwKey(serverKey, peerId)) !== null;
}

export interface RecentPeer {
  id: string;
  name?: string;
  at: number;
}

export function loadRecent(): RecentPeer[] {
  const r = read<RecentPeer[]>(RECENT_KEY);
  return Array.isArray(r) ? r.filter((x) => typeof x?.id === "string").slice(0, 8) : [];
}

export function pushRecent(id: string, name?: string): void {
  const list = loadRecent().filter((r) => r.id !== id);
  list.unshift({ id, name, at: Date.now() });
  write(RECENT_KEY, list.slice(0, 8));
}

/** Forgets everything stored by the web client in this browser. */
export function clearAllWebClientData(): void {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (k && (k.startsWith(PW_PREFIX) || k === RECENT_KEY || k === PREFS_KEY)) localStorage.removeItem(k);
    }
    // legacy keys from the first web client version (plaintext passwords)
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (k && (k.startsWith("rdpanel_pwd_") || k === "rdpanel_webclient_server_settings")) localStorage.removeItem(k);
    }
  } catch {
    /* ignore */
  }
}

/** Removes plaintext passwords stored by the first web client version. */
export function purgeLegacyStorage(): void {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const k = localStorage.key(i);
      if (k && (k.startsWith("rdpanel_pwd_") || k === "rdpanel_webclient_server_settings")) localStorage.removeItem(k);
    }
  } catch {
    /* ignore */
  }
}
