import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  Activity,
  ArrowLeft,
  Ban,
  Camera,
  ClipboardPaste,
  Command,
  Eye,
  FolderOpen,
  Keyboard,
  Loader2,
  Lock,
  Maximize2,
  MessageSquare,
  Minimize2,
  MonitorUp,
  MousePointer2,
  MousePointerClick,
  RefreshCw,
  RotateCcw,
  Scaling,
  Settings2,
  ShieldAlert,
  ShieldCheck,
  Trash2,
  Type,
  Volume2,
  VolumeX,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  Session,
  Viewer,
  decodeBase64,
  encodeBase64,
  isWebCodecsAvailable,
  type CloseInfo,
  type InputMode,
  type PeerSummary,
  type AudioState,
  type ScaleMode,
  type ServerConfig,
  type SessionSettings,
  type SessionState,
  type SessionStats,
} from "rdweb";

import { webClientApi, type WebClientConfig, type WebClientCredentials } from "@/api/system";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox, Textarea } from "@/components/ui/misc";
import { cn } from "@/lib/utils";
import {
  hasRememberedHash,
  loadPrefs,
  loadRecent,
  loadRememberedHash,
  purgeLegacyStorage,
  pushRecent,
  saveRememberedHash,
  savePrefs,
  type ViewPrefs,
  type WebClientPrefs,
} from "./prefs";
import { FilesSheet } from "./FilesSheet";
import { ServerPicker } from "./ServerPicker";
import { loadSelectedServer, relayUrlOf, saveSelectedServer, type OtherServer } from "./servers";
import { SettingsSheet } from "./SettingsSheet";
import { DEFAULT_PINNED_DESKTOP, DEFAULT_PINNED_TOUCH, Toolbar, type ActionId, type ToolbarAction } from "./Toolbar";

type Search = { id?: string };

function wsUrl(configured: string, path: string): string {
  if (configured) return configured;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}

function isTouchFirst(): boolean {
  return window.matchMedia?.("(pointer: coarse)").matches && !window.matchMedia?.("(pointer: fine)").matches;
}

export function WebClientPage() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false }) as Search;
  const navigate = useNavigate();
  const config = useQuery({ queryKey: ["webclient", "config"], queryFn: webClientApi.config, staleTime: 60_000 });
  const [peerId, setPeerId] = useState(() => (search.id ?? "").replace(/\s+/g, ""));
  const [active, setActive] = useState<{ id: string; attempt: number; server: OtherServer | null } | null>(null);
  const [serverId, setServerId] = useState(loadSelectedServer);

  useEffect(() => {
    purgeLegacyStorage();
  }, []);

  // Auto-connect when opened with ?id=
  const autoStarted = useRef(false);
  useEffect(() => {
    if (autoStarted.current || !config.data?.enabled || !search.id) return;
    autoStarted.current = true;
    setActive({ id: search.id.replace(/\s+/g, ""), attempt: 0, server: null });
  }, [config.data, search.id]);

  const leave = useCallback(() => {
    setActive(null);
    void navigate({ to: "/connect", search: peerId ? { id: peerId } : {}, replace: true });
  }, [navigate, peerId]);

  if (!isWebCodecsAvailable()) {
    return (
      <CenterCard>
        <ShieldAlert className="mx-auto h-10 w-10 text-destructive" />
        <h1 className="text-lg font-semibold">{t("webclient.unsupportedTitle")}</h1>
        <p className="text-sm text-muted-foreground">{t("webclient.unsupportedDesc")}</p>
        <Button variant="outline" onClick={() => void navigate({ to: "/" })}>
          {t("common.back")}
        </Button>
      </CenterCard>
    );
  }

  if (config.isLoading) {
    return (
      <CenterCard>
        <Loader2 className="mx-auto h-6 w-6 animate-spin text-muted-foreground" />
      </CenterCard>
    );
  }

  const cfg = config.data;
  if (!cfg?.enabled || !cfg.public_key) {
    return (
      <CenterCard>
        <ShieldAlert className="mx-auto h-10 w-10 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{t("webclient.title")}</h1>
        <p className="text-sm text-muted-foreground">{!cfg?.enabled ? t("webclient.disabled") : t("webclient.noKey")}</p>
        <Button variant="outline" onClick={() => void navigate({ to: "/" })}>
          {t("common.back")}
        </Button>
      </CenterCard>
    );
  }

  const adminServers: OtherServer[] = (cfg.servers ?? []).map((sv, i) => ({
    id: `admin:${i}`,
    name: sv.name,
    idUrl: sv.id_url,
    relayUrl: sv.relay_url,
    key: sv.key,
    source: "admin" as const,
  }));

  if (active) {
    return (
      <SavedCredentials peerId={active.id} enabled={!active.server} key={`creds-${active.id}#${active.attempt}`}>
        {(creds) => (
          <RemoteSession
            key={`${active.id}#${active.attempt}`}
            peerId={active.id}
            config={cfg}
            other={active.server}
            credentials={creds}
        onLeave={leave}
            onReconnect={() => setActive((a) => (a ? { ...a, attempt: a.attempt + 1 } : a))}
          />
        )}
      </SavedCredentials>
    );
  }

  return (
    <ConnectForm
      peerId={peerId}
      setPeerId={setPeerId}
      adminServers={adminServers}
      allowCustom={cfg.custom_servers}
      serverId={serverId}
      setServerId={(id) => {
        setServerId(id);
        saveSelectedServer(id);
      }}
      onConnect={(id, server) => setActive({ id, attempt: 0, server })}
    />
  );
}

/** Loads the device's saved login from the user's address books first. */
function SavedCredentials({
  peerId,
  enabled = true,
  children,
}: {
  peerId: string;
  enabled?: boolean;
  children: (c: WebClientCredentials | null) => React.ReactNode;
}) {
  const q = useQuery({
    queryKey: ["webclient", "credentials", peerId],
    queryFn: () => webClientApi.credentials(peerId),
    enabled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
  });
  if (q.isLoading) {
    return (
      <div className="flex min-h-dvh items-center justify-center bg-black">
        <Loader2 className="h-6 w-6 animate-spin text-white/70" />
      </div>
    );
  }
  return <>{children(q.data ?? null)}</>;
}

function CenterCard({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background p-4">
      <div className="w-full max-w-sm space-y-4 rounded-lg border bg-card p-6 text-center shadow-sm">{children}</div>
    </div>
  );
}

function ConnectForm({
  peerId,
  setPeerId,
  adminServers,
  allowCustom,
  serverId,
  setServerId,
  onConnect,
}: {
  peerId: string;
  setPeerId: (v: string) => void;
  adminServers: OtherServer[];
  allowCustom: boolean;
  serverId: string;
  setServerId: (id: string) => void;
  onConnect: (id: string, server: OtherServer | null) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const recent = useMemo(loadRecent, []);
  const [server, setServer] = useState<OtherServer | null>(null);
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const id = peerId.replace(/\s+/g, "");
    if (id) onConnect(id, server);
  };
  return (
    <div className="flex min-h-dvh items-start justify-center bg-background p-4 sm:items-center">
      <form onSubmit={submit} className="w-full max-w-sm space-y-4 rounded-lg border bg-card p-5 shadow-sm">
        <div className="flex items-center gap-2">
          <Button type="button" variant="ghost" size="icon" aria-label={t("common.back")} onClick={() => void navigate({ to: "/" })}>
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <h1 className="min-w-0 truncate text-lg font-semibold">{t("webclient.title")}</h1>
        </div>
        {(adminServers.length > 0 || allowCustom) && (
          <ServerPicker
            adminServers={adminServers}
            allowCustom={allowCustom}
            value={serverId}
            onChange={(id, sv) => {
              setServerId(id);
              setServer(sv);
            }}
          />
        )}
        <div className="space-y-1.5">
          <Label htmlFor="wc-peer">{t("webclient.peerId")}</Label>
          <Input
            id="wc-peer"
            value={peerId}
            onChange={(e) => setPeerId(e.target.value)}
            inputMode="numeric"
            autoComplete="off"
            autoFocus
            placeholder="123 456 789"
          />
        </div>
        <Button type="submit" className="w-full" disabled={!peerId.trim()}>
          {t("webclient.connect")}
        </Button>
        {recent.length > 0 && (
          <div className="space-y-1">
            <div className="text-xs text-muted-foreground">{t("webclient.recent")}</div>
            <div className="flex flex-wrap gap-1.5">
              {recent.map((r) => (
                <Button key={r.id} type="button" variant="outline" size="sm" className="max-w-full" onClick={() => onConnect(r.id, server)}>
                  <span className="truncate">{r.name ? `${r.name} · ${r.id}` : r.id}</span>
                </Button>
              ))}
            </div>
          </div>
        )}
        <p className="text-xs text-muted-foreground">{t("webclient.formHint")}</p>
      </form>
    </div>
  );
}

/* ───────────────────────────── session ───────────────────────────── */

type Dialog = "none" | "password" | "twoFactor" | "clipboard" | "type" | "chat" | "restart";

const STICKY = ["ControlLeft", "AltLeft", "MetaLeft", "ShiftLeft"] as const;
type Sticky = (typeof STICKY)[number];

function RemoteSession({
  peerId,
  config,
  other,
  credentials,
  onLeave,
  onReconnect,
}: {
  peerId: string;
  config: WebClientConfig;
  /** Another server (admin-offered or user-entered); null = this panel's server. */
  other: OtherServer | null;
  credentials: WebClientCredentials | null;
  onLeave: () => void;
  onReconnect: () => void;
}) {
  const { t } = useTranslation();
  const screenRef = useRef<HTMLDivElement>(null);
  const shellRef = useRef<HTMLDivElement>(null);
  const sessionRef = useRef<Session | null>(null);
  const viewerRef = useRef<Viewer | null>(null);
  const rememberRef = useRef(false);
  const [loginHash, setLoginHash] = useState<Uint8Array | null>(null);

  const [prefs, setPrefs] = useState<WebClientPrefs>(loadPrefs);
  const [state, setState] = useState<SessionState>("idle");
  const [peer, setPeer] = useState<PeerSummary | null>(null);
  const [currentDisplay, setCurrentDisplay] = useState(0);
  const [stats, setStats] = useState<SessionStats | null>(null);
  const [closed, setClosed] = useState<CloseInfo | null>(null);
  const [error, setError] = useState<string>("");
  const [dialog, setDialog] = useState<Dialog>("none");
  const [pwWrong, setPwWrong] = useState(false);
  const [waiting, setWaiting] = useState(false);
  const [tfaWrong, setTfaWrong] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [filesOpen, setFilesOpen] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [keyboardBar, setKeyboardBar] = useState(false);
  const [sticky, setSticky] = useState<Set<Sticky>>(new Set());
  const [resolution, setResolution] = useState("current");
  const [privacyOn, setPrivacyOn] = useState(false);
  const [blockOn, setBlockOn] = useState(false);
  const [audioState, setAudioState] = useState<AudioState>("off");
  const [permKeyboard, setPermKeyboard] = useState(true);
  // Panel features (login token, web id, account passwords) only exist on our own server.
  const own = !other;
  const serverKey = other ? other.key.trim() : config.public_key;
  const myId = own ? config.my_id || undefined : undefined;
  const [hasSaved, setHasSaved] = useState(() => hasRememberedHash(serverKey, peerId));
  const inputMode: InputMode = prefs.view.inputMode ?? (isTouchFirst() ? "trackpad" : "mouse");
  const server = useMemo<ServerConfig>(
    () => ({
      rendezvousUrl: other ? other.idUrl : wsUrl(config.rendezvous_url, "/ws/id"),
      relayUrl: () => (other ? relayUrlOf(other) : wsUrl(config.relay_url, "/ws/relay")),
      key: serverKey,
      token: other ? undefined : (config.token ?? undefined),
    }),
    [config, other, serverKey],
  );
  const touch = inputMode !== "mouse";

  // Session + viewer lifecycle
  useEffect(() => {
    const el = screenRef.current;
    if (!el) return;
    const p = loadPrefs();
    // Saved logins, in the desktop client's order: this browser's remembered
    // hash, the personal address book's hash, then a shared book's password.
    const localHash = loadRememberedHash(serverKey, peerId);
    let abHash: Uint8Array | undefined;
    if (!localHash && credentials?.hash) {
      try {
        abHash = decodeBase64(credentials.hash);
      } catch {
        abHash = undefined;
      }
    }
    let abPasswordTried = !credentials?.password;
    let audioHintShown = false;
    let session: Session;
    try {
      session = new Session({
        peerId,
        server,
        rememberedHash: localHash ?? abHash,
        myName: config.display_name,
        myId,
        settings: p.settings,
      });
    } catch (e) {
      setError((e as Error).message);
      return;
    }
    const viewer = new Viewer(el, session, {
      scaleMode: p.view.scaleMode,
      inputMode: p.view.inputMode ?? undefined,
      zoomCursor: p.view.zoomCursor,
    });
    sessionRef.current = session;
    viewerRef.current = viewer;
    const offs = [
      session.on("state", ({ state }) => setState(state)),
      session.on("password", ({ wrong }) => {
        if (!abPasswordTried && credentials?.password) {
          abPasswordTried = true;
          if (wrong && localHash) {
            saveRememberedHash(serverKey, peerId, null);
            setHasSaved(false);
          }
          void session.login(credentials.password);
          return;
        }
        setPwWrong(wrong);
        if (wrong && localHash) {
          saveRememberedHash(serverKey, peerId, null);
          setHasSaved(false);
        }
        if (wrong && own && abHash && !credentials?.book) void webClientApi.forget(peerId).catch(() => {});
        setDialog("password");
      }),
      session.on("waitingForApproval", () => {
        setWaiting(true);
        setDialog("password");
      }),
      session.on("twoFactor", ({ wrong }) => {
        setTfaWrong(wrong);
        setDialog("twoFactor");
      }),
      session.on("connected", ({ peer, rememberedHash }) => {
        setLoginHash(rememberedHash);
        setPeer(peer);
        setCurrentDisplay(peer.currentDisplay);
        setDialog("none");
        setWaiting(false);
        pushRecent(peerId, peer.hostname || undefined);
        if (rememberedHash && rememberRef.current) {
          saveRememberedHash(serverKey, peerId, rememberedHash);
          setHasSaved(true);
          // the personal address book: other browsers and RustDesk apps signed in to this account
          if (own) webClientApi.remember(peerId, encodeBase64(rememberedHash)).catch(() => toast.error(t("webclient.rememberAccountFailed")));
        }
        viewer.focus();
      }),
      session.on("peer", ({ peer }) => setPeer(peer)),
      session.on("display", ({ display }) => setCurrentDisplay(display.index)),
      session.on("stats", setStats),
      session.on("audio", ({ state }) => {
        setAudioState(state);
        if (state === "blocked" && !audioHintShown) {
          audioHintShown = true;
          toast(t("webclient.sound.blocked"));
        }
      }),
      session.on("permission", ({ name, enabled }) => {
        if (name === "keyboard") setPermKeyboard(enabled);
      }),
      session.on("privacyMode", ({ on, message }) => {
        setPrivacyOn(on);
        if (message) toast.info(message);
      }),
      session.on("blockInput", ({ on, message }) => {
        setBlockOn(on);
        if (message) toast.info(message);
      }),
      session.on("message", ({ type, title, text }) => {
        const body = [title, text].filter(Boolean).join(": ");
        if (!body) return;
        if (type.includes("error")) toast.error(body);
        else toast.info(body);
      }),
      session.on("chat", ({ text }) => toast(t("webclient.chatFromRemote"), { description: text, duration: 10_000 })),
      session.on("close", (info) => {
        setClosed(info);
        setDialog("none");
      }),
      viewer.on("clipboardBlocked", ({ text }) =>
        toast(t("webclient.clipboardReceived"), {
          action: { label: t("webclient.copy"), onClick: () => void navigator.clipboard.writeText(text).catch(() => {}) },
        }),
      ),
    ];
    let disposed = false;
    session.connect().catch((e: Error & { code?: string }) => {
      if (disposed) return;
      const known = ["offline", "not-found", "key-mismatch", "login-required", "timeout", "network", "handshake", "unverified-peer", "refused"];
      setError(e.code && known.includes(e.code) ? t(`webclient.errors.${e.code}`, { detail: e.message }) : e.message);
    });
    return () => {
      disposed = true;
      for (const off of offs) off();
      session.close("closed");
      viewer.destroy();
      sessionRef.current = null;
      viewerRef.current = null;
    };
    // config/peerId are fixed for the life of this component (it is keyed)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Persist prefs
  useEffect(() => savePrefs(prefs), [prefs]);

  // Follow the effective input mode (it depends on the device when unset).
  useEffect(() => {
    if (viewerRef.current && viewerRef.current.getInputMode() !== inputMode) viewerRef.current.setInputMode(inputMode);
  }, [inputMode]);

  // Track the visual viewport so the screen stays above on-screen keyboards.
  useEffect(() => {
    const vv = window.visualViewport;
    const shell = shellRef.current;
    if (!vv || !shell) return;
    const apply = () => {
      shell.style.height = `${vv.height}px`;
      shell.style.transform = `translateY(${vv.offsetTop}px)`;
    };
    apply();
    vv.addEventListener("resize", apply);
    vv.addEventListener("scroll", apply);
    return () => {
      vv.removeEventListener("resize", apply);
      vv.removeEventListener("scroll", apply);
    };
  }, []);

  useEffect(() => {
    const onFs = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", onFs);
    return () => document.removeEventListener("fullscreenchange", onFs);
  }, []);

  const updateSettings = useCallback((patch: Partial<SessionSettings>) => {
    sessionRef.current?.updateSettings(patch);
    setPrefs((p) => ({ ...p, settings: { ...p.settings, ...patch } }));
  }, []);

  const updateView = useCallback((patch: Partial<ViewPrefs>) => {
    const v = viewerRef.current;
    if (v) {
      if (patch.scaleMode) v.setScaleMode(patch.scaleMode);
      if (patch.inputMode) v.setInputMode(patch.inputMode);
      if (patch.zoomCursor !== undefined) v.setZoomCursor(patch.zoomCursor);
    }
    setPrefs((p) => ({ ...p, view: { ...p.view, ...patch } }));
  }, []);

  const s = sessionRef.current;
  const connected = state === "connected";
  const busy = !closed && !error && !connected;

  const pressSpecial = (code: string, key: string) => {
    const sess = sessionRef.current;
    if (!sess) return;
    const mods = [...sticky];
    for (const m of mods) sess.key({ code: m, key: "", down: true });
    sess.key({ code, key, down: true });
    sess.key({ code, key, down: false });
    for (const m of mods.reverse()) sess.key({ code: m, key: "", down: false });
    if (mods.length) setSticky(new Set());
  };

  const toggleSticky = (m: Sticky) =>
    setSticky((prev) => {
      const next = new Set(prev);
      if (next.has(m)) next.delete(m);
      else next.add(m);
      return next;
    });

  // Our own disconnects are not worth a raw (English) reason.
  const closedText = closed ? (closed.remote ? closed.reason : t("webclient.state.closed")) : "";
  const statusText = error ? error : closed ? closedText : t(`webclient.state.${state}`, { defaultValue: state });

  const title = peer?.hostname || peerId;

  const displays = peer?.displays ?? [];
  const screenshot = () =>
    void viewerRef.current?.screenshot().then((blob) => {
      if (!blob) return;
      const a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = `${peerId}-${new Date().toISOString().replace(/[:.]/g, "-")}.png`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    });
  const nextScale: Record<ScaleMode, ScaleMode> = { fit: "original", original: "stretch", stretch: "fit" };
  const nextInput: Record<InputMode, InputMode> = { mouse: "trackpad", trackpad: "touch", touch: "mouse" };
  const off = !connected;
  const st = prefs.settings;
  const actions: ToolbarAction[] = [
    {
      id: "sound",
      label: t("webclient.sound.enable"),
      icon: VolumeX,
      hidden: audioState !== "blocked",
      forced: true,
      run: () => s?.resumeAudio(),
    },
    { id: "settings", label: t("webclient.settings.title"), icon: Settings2, disabled: off, run: () => setSettingsOpen(true) },
    { id: "files", label: t("webclient.files.title"), icon: FolderOpen, disabled: off || s?.permission.file === false, run: () => setFilesOpen(true) },
    {
      id: "keyboard",
      label: t("webclient.keyboard.show"),
      icon: Keyboard,
      hidden: !touch,
      active: keyboardBar,
      disabled: off,
      run: () => {
        setKeyboardBar((v) => !v);
        if (!keyboardBar) viewerRef.current?.showKeyboard();
        else viewerRef.current?.hideKeyboard();
      },
    },
    { id: "inputMode", label: `${t("webclient.settings.inputMode")}: ${t(`webclient.input.${inputMode}`)}`, icon: MousePointer2, disabled: off, run: () => updateView({ inputMode: nextInput[inputMode] }) },
    { id: "viewMode", label: `${t("webclient.settings.viewStyle")}: ${t(`webclient.view.${prefs.view.scaleMode}`)}`, icon: Scaling, disabled: off, run: () => updateView({ scaleMode: nextScale[prefs.view.scaleMode] }) },
    {
      id: "nextDisplay",
      label: t("webclient.toolbar.nextDisplay", { n: currentDisplay + 1, total: displays.length }),
      icon: MonitorUp,
      hidden: displays.length < 2,
      disabled: off,
      run: () => s?.switchDisplay((currentDisplay + 1) % Math.max(1, displays.length)),
    },
    { id: "ctrlAltDel", label: t("webclient.ctrlAltDel"), icon: Command, disabled: off || !permKeyboard || st.viewOnly, run: () => s?.ctrlAltDel() },
    { id: "lockScreen", label: t("webclient.lockScreen"), icon: Lock, disabled: off || !permKeyboard || st.viewOnly, run: () => s?.lockScreen() },
    { id: "refresh", label: t("webclient.refresh"), icon: RefreshCw, disabled: off, run: () => s?.refreshVideo() },
    { id: "mute", label: t("webclient.settings.mute"), icon: st.disableAudio ? VolumeX : Volume2, active: st.disableAudio, disabled: off, run: () => updateSettings({ disableAudio: !st.disableAudio }) },
    { id: "viewOnly", label: t("webclient.settings.viewOnly"), icon: Eye, active: st.viewOnly, disabled: off, run: () => updateSettings({ viewOnly: !st.viewOnly }) },
    { id: "remoteCursor", label: t("webclient.settings.showRemoteCursor"), icon: MousePointerClick, active: st.showRemoteCursor, disabled: off, run: () => updateSettings({ showRemoteCursor: !st.showRemoteCursor }) },
    { id: "stats", label: t("webclient.settings.showStats"), icon: Activity, active: prefs.view.showStats, disabled: off, run: () => updateView({ showStats: !prefs.view.showStats }) },
    { id: "clipboard", label: t("webclient.sendClipboard"), icon: ClipboardPaste, disabled: off || !permKeyboard || st.viewOnly, run: () => setDialog("clipboard") },
    { id: "typeText", label: t("webclient.typeText"), icon: Type, disabled: off || !permKeyboard || st.viewOnly, run: () => setDialog("type") },
    { id: "chat", label: t("webclient.chat"), icon: MessageSquare, disabled: off, run: () => setDialog("chat") },
    { id: "screenshot", label: t("webclient.screenshot"), icon: Camera, disabled: off, run: screenshot },
    {
      id: "privacy",
      label: t("webclient.settings.privacyMode"),
      icon: ShieldCheck,
      hidden: !peer?.privacyModeSupported || s?.permission.privacyMode === false,
      active: privacyOn,
      disabled: off,
      run: () => s?.setPrivacyMode(!privacyOn),
    },
    {
      id: "blockInput",
      label: t("webclient.settings.blockInput"),
      icon: Ban,
      hidden: peer?.os !== "windows" || s?.permission.blockInput === false,
      active: blockOn,
      disabled: off,
      run: () => {
        setBlockOn(!blockOn);
        s?.setBlockInput(!blockOn);
      },
    },
    {
      id: "fullscreen",
      label: fullscreen ? t("webclient.exitFullscreen") : t("webclient.fullscreen"),
      icon: fullscreen ? Minimize2 : Maximize2,
      hidden: !document.fullscreenEnabled,
      active: fullscreen,
      run: () => (fullscreen ? void viewerRef.current?.exitFullscreen() : void viewerRef.current?.enterFullscreen(shellRef.current ?? undefined)),
    },
    {
      id: "forgetPassword",
      label: t("webclient.forgetPassword"),
      icon: Trash2,
      hidden: !hasSaved && !(credentials?.hash && !credentials.book),
      run: () => {
        saveRememberedHash(serverKey, peerId, null);
        setHasSaved(false);
        if (!own) {
          toast.success(t("webclient.passwordCleared"));
          return;
        }
        void webClientApi.forget(peerId).then(
          () => toast.success(t("webclient.passwordCleared")),
          () => toast.error(t("webclient.rememberAccountFailed")),
        );
      },
    },
    { id: "restart", label: t("webclient.restartRemote"), icon: RotateCcw, danger: true, hidden: s?.permission.restart === false, disabled: off, run: () => setDialog("restart") },
  ];
  const defaultPinned = touch ? DEFAULT_PINNED_TOUCH : DEFAULT_PINNED_DESKTOP;
  const pinned = (prefs.toolbar.pinned as ActionId[] | null) ?? defaultPinned;

  return (
    <div ref={shellRef} className="fixed inset-x-0 top-0 flex h-dvh flex-col overflow-hidden bg-black text-white">
      {/* the floating toolbar overlays the screen (positioned in the screen box below) */}
      {/* screen */}
      <div className="relative min-h-0 flex-1">
        <div ref={screenRef} className="absolute inset-0" />
        <Toolbar
          actions={actions}
          pinned={pinned}
          onPinned={(ids) => setPrefs((p) => ({ ...p, toolbar: { ...p.toolbar, pinned: ids } }))}
          defaultPinned={defaultPinned}
          keepOpen={prefs.toolbar.keepOpen}
          onKeepOpen={(v) => setPrefs((p) => ({ ...p, toolbar: { ...p.toolbar, keepOpen: v } }))}
          position={prefs.toolbar.position}
          onPosition={(x) => setPrefs((p) => ({ ...p, toolbar: { ...p.toolbar, position: x } }))}
          title={title}
          subtitle={connected && peer ? `${peerId} · ${peer.platform}` : statusText}
          status={connected ? "ok" : closed || error ? "error" : "busy"}
          canCollapse={connected && !settingsOpen && !filesOpen && dialog === "none"}
          onDisconnect={() => s?.close()}
          onLeave={onLeave}
          ended={!!(closed || error)}
        />

        {prefs.view.showStats && stats && connected && (
          <div className="pointer-events-none absolute left-2 top-2 rounded bg-black/70 px-2 py-1 font-mono text-[11px] leading-snug text-white/90">
            <div>
              {(stats.codec ?? "—").toUpperCase()}
              {stats.chroma444 ? " 4:4:4" : ""} · {stats.width}×{stats.height}
            </div>
            <div>
              {stats.fps} fps · {stats.kbps} kbps
            </div>
            <div>
              {t("webclient.stats.delay")} {stats.delayMs} ms · {t("webclient.stats.target")} {stats.targetKbps} kbps
            </div>
          </div>
        )}
        {busy && dialog === "none" && (
          <Overlay>
            <Loader2 className="h-6 w-6 animate-spin" />
            <div className="text-sm text-white/80">{statusText}</div>
          </Overlay>
        )}
        {(closed || error) && (
          <Overlay>
            <div className="max-w-sm text-center text-sm text-white/90">{error || closedText}</div>
            <div className="flex flex-wrap justify-center gap-2">
              <Button size="sm" onClick={onReconnect}>
                <RefreshCw className="mr-2 h-4 w-4" />
                {t("webclient.reconnect")}
              </Button>
              <Button size="sm" variant="secondary" onClick={onLeave}>
                {t("common.back")}
              </Button>
            </div>
          </Overlay>
        )}
      </div>

      {/* extra keys for on-screen keyboards */}
      {connected && touch && keyboardBar && (
        <div className="flex shrink-0 gap-1 overflow-x-auto border-t border-white/10 bg-neutral-900 px-1.5 py-1">
          {STICKY.map((m) => (
            <KeyBtn key={m} active={sticky.has(m)} onClick={() => toggleSticky(m)}>
              {t(`webclient.keys.${m}`)}
            </KeyBtn>
          ))}
          <KeyBtn onClick={() => pressSpecial("Escape", "Escape")}>Esc</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("Tab", "Tab")}>Tab</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("ArrowLeft", "ArrowLeft")}>←</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("ArrowUp", "ArrowUp")}>↑</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("ArrowDown", "ArrowDown")}>↓</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("ArrowRight", "ArrowRight")}>→</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("Delete", "Delete")}>Del</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("Home", "Home")}>Home</KeyBtn>
          <KeyBtn onClick={() => pressSpecial("End", "End")}>End</KeyBtn>
          {["F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12"].map((f) => (
            <KeyBtn key={f} onClick={() => pressSpecial(f, f)}>
              {f}
            </KeyBtn>
          ))}
        </div>
      )}

      <FilesSheet
        open={filesOpen}
        onOpenChange={setFilesOpen}
        peerId={peerId}
        server={server}
        rememberedHash={loginHash}
        myName={config.display_name}
        myId={myId}
      />
      <SettingsSheet
        open={settingsOpen}
        onOpenChange={setSettingsOpen}
        settings={prefs.settings}
        onSettings={updateSettings}
        view={prefs.view}
        inputMode={inputMode}
        onView={updateView}
        peer={peer}
        currentDisplay={currentDisplay}
        onSwitchDisplay={(i) => s?.switchDisplay(i)}
        resolution={resolution}
        onResolution={(v) => {
          setResolution(v);
          if (v === "current") return;
          const [w, h] = v.split("x").map(Number);
          s?.changeResolution(w ?? 0, h ?? 0);
        }}
        privacyOn={privacyOn}
        onPrivacy={(on) => s?.setPrivacyMode(on)}
        blockInputOn={blockOn}
        onBlockInput={(on) => {
          setBlockOn(on);
          s?.setBlockInput(on);
        }}
        canPrivacy={!!peer?.privacyModeSupported && s?.permission.privacyMode !== false}
        canBlockInput={peer?.os === "windows" && s?.permission.blockInput !== false}
      />

      <PasswordDialog
        open={dialog === "password"}
        wrong={pwWrong}
        waiting={waiting}
        onSubmit={(pw, remember) => {
          rememberRef.current = remember;
          setPwWrong(false);
          void sessionRef.current?.login(pw);
        }}
        onCancel={() => {
          setDialog("none");
          sessionRef.current?.close(t("webclient.cancelled"));
        }}
      />
      <TwoFactorDialog
        open={dialog === "twoFactor"}
        wrong={tfaWrong}
        onSubmit={(code) => {
          setTfaWrong(false);
          sessionRef.current?.submitTwoFactor(code);
        }}
        onCancel={() => {
          setDialog("none");
          sessionRef.current?.close(t("webclient.cancelled"));
        }}
      />
      <TextDialog
        open={dialog === "clipboard"}
        title={t("webclient.sendClipboard")}
        description={t("webclient.sendClipboardDesc")}
        submitLabel={t("webclient.send")}
        onSubmit={(text) => {
          s?.sendClipboard(text);
          toast.success(t("webclient.clipboardSent"));
        }}
        onClose={() => setDialog("none")}
      />
      <TextDialog
        open={dialog === "type"}
        title={t("webclient.typeText")}
        description={t("webclient.typeTextDesc")}
        submitLabel={t("webclient.type")}
        onSubmit={(text) => s?.typeText(text)}
        onClose={() => setDialog("none")}
      />
      <TextDialog
        open={dialog === "chat"}
        title={t("webclient.chat")}
        description={t("webclient.chatDesc")}
        submitLabel={t("webclient.send")}
        onSubmit={(text) => s?.sendChat(text)}
        onClose={() => setDialog("none")}
      />
      <Dialog open={dialog === "restart"} onOpenChange={(o) => !o && setDialog("none")}>
        <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
          <DialogHeader>
            <DialogTitle>{t("webclient.restartRemote")}</DialogTitle>
            <DialogDescription>{t("webclient.restartConfirm")}</DialogDescription>
          </DialogHeader>
          <DialogFooter className="gap-2">
            <Button variant="outline" onClick={() => setDialog("none")}>
              {t("common.cancel")}
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                s?.restartRemote();
                setDialog("none");
              }}
            >
              {t("webclient.restartRemote")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function KeyBtn({ children, onClick, active }: { children: React.ReactNode; onClick: () => void; active?: boolean }) {
  return (
    <button
      type="button"
      // keep focus (and the on-screen keyboard) on the input
      onPointerDown={(e) => e.preventDefault()}
      onClick={onClick}
      className={cn(
        "h-8 shrink-0 rounded border border-white/15 px-2.5 text-xs text-white/90 active:bg-white/20",
        active ? "bg-white/25" : "bg-white/5",
      )}
    >
      {children}
    </button>
  );
}

function Overlay({ children }: { children: React.ReactNode }) {
  return <div className="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-black/60 p-4">{children}</div>;
}

function PasswordDialog({
  open,
  wrong,
  waiting,
  onSubmit,
  onCancel,
}: {
  open: boolean;
  wrong: boolean;
  waiting: boolean;
  onSubmit: (pw: string, remember: boolean) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [pw, setPw] = useState("");
  const [remember, setRemember] = useState(false);
  const [sent, setSent] = useState(false);
  useEffect(() => {
    if (open) setSent(false);
  }, [open, wrong]);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!pw) return;
            setSent(true);
            onSubmit(pw, remember);
            setPw("");
          }}
          className="space-y-4"
        >
          <DialogHeader>
            <DialogTitle>{t("webclient.passwordRequired")}</DialogTitle>
            <DialogDescription>{waiting ? t("webclient.waitingForApproval") : t("webclient.passwordDesc")}</DialogDescription>
          </DialogHeader>
          {wrong && <p className="text-sm text-destructive">{t("webclient.wrongPassword")}</p>}
          <Input
            type="password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            placeholder={t("webclient.passwordPlaceholder")}
            autoComplete="off"
            autoFocus
            aria-label={t("webclient.passwordPlaceholder")}
          />
          <label className="flex items-start gap-2 text-sm">
            <Checkbox className="mt-0.5" checked={remember} onCheckedChange={(v) => setRemember(v === true)} />
            <span>
              {t("webclient.rememberPassword")}
              <span className="block text-xs text-muted-foreground">{t("webclient.rememberHint")}</span>
            </span>
          </label>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!pw || sent}>
              {sent ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
              {t("webclient.connect")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function TwoFactorDialog({ open, wrong, onSubmit, onCancel }: { open: boolean; wrong: boolean; onSubmit: (code: string) => void; onCancel: () => void }) {
  const { t } = useTranslation();
  const [code, setCode] = useState("");
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onCancel()}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-sm">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (code.trim()) onSubmit(code.trim());
            setCode("");
          }}
          className="space-y-4"
        >
          <DialogHeader>
            <DialogTitle>{t("webclient.twoFactorTitle")}</DialogTitle>
            <DialogDescription>{t("webclient.twoFactorDesc")}</DialogDescription>
          </DialogHeader>
          {wrong && <p className="text-sm text-destructive">{t("webclient.twoFactorWrong")}</p>}
          <Input value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" autoComplete="one-time-code" autoFocus maxLength={10} aria-label={t("webclient.twoFactorTitle")} />
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={onCancel}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!code.trim()}>
              {t("webclient.verify")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function TextDialog({
  open,
  title,
  description,
  submitLabel,
  onSubmit,
  onClose,
}: {
  open: boolean;
  title: string;
  description: string;
  submitLabel: string;
  onSubmit: (text: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState("");
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={4} autoFocus aria-label={title} />
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!text}
            onClick={() => {
              onSubmit(text);
              setText("");
              onClose();
            }}
          >
            {submitLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
