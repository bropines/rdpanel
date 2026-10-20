import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowLeft, FolderOpen, Loader2, ShieldAlert, SquareTerminal } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { decodeBase64, encodeBase64, isWebCodecsAvailable, RDWEB_VERSION, type ServerConfig } from "rdweb";
import { RemoteDesktop, RDPANEL_STORAGE_KEY, loadRecent, migrateStorage, type RemoteDesktopCredentials, type ShellStore } from "rdweb/react";

import { webClientApi, type WebClientConfig, type WebClientCredentials } from "@/api/system";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ServerPicker } from "./ServerPicker";
import { loadSelectedServer, relayUrlOf, saveSelectedServer, type OtherServer } from "./servers";

type Search = { id?: string; mode?: Mode };
type Mode = "files" | "terminal";

function wsUrl(configured: string, path: string): string {
  if (configured) return configured;
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}${path}`;
}

function safeDecode(b64: string | null | undefined): Uint8Array | undefined {
  if (!b64) return undefined;
  try {
    return decodeBase64(b64);
  } catch {
    return undefined;
  }
}

export function WebClientPage() {
  const { t } = useTranslation();
  const search = useSearch({ strict: false }) as Search;
  const navigate = useNavigate();
  const config = useQuery({ queryKey: ["webclient", "config"], queryFn: webClientApi.config, staleTime: 60_000 });
  const [peerId, setPeerId] = useState(() => (search.id ?? "").replace(/\s+/g, ""));
  const [active, setActive] = useState<{ id: string; attempt: number; server: OtherServer | null; mode?: Mode } | null>(null);
  const [serverId, setServerId] = useState(loadSelectedServer);

  useEffect(() => {
    // Keeps settings, remembered hashes and recent devices of earlier versions.
    migrateStorage(RDPANEL_STORAGE_KEY);
  }, []);

  // Auto-connect when opened with ?id= (from a device's drawer): our own server.
  const autoStarted = useRef(false);
  useEffect(() => {
    if (autoStarted.current || !config.data?.enabled || !search.id) return;
    autoStarted.current = true;
    setActive({ id: search.id.replace(/\s+/g, ""), attempt: 0, server: null, mode: search.mode });
  }, [config.data, search.id, search.mode]);

  const leave = useCallback(() => {
    setActive(null);
    void navigate({ to: "/connect", search: peerId ? { id: peerId } : {}, replace: true });
  }, [navigate, peerId]);

  if (!isWebCodecsAvailable() && !search.mode) {
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
          <Session
            key={`${active.id}#${active.attempt}`}
            peerId={active.id}
            config={cfg}
            other={active.server}
            mode={active.mode}
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
      onConnect={(id, server, mode) => setActive({ id, attempt: 0, server, mode })}
    />
  );
}

/**
 * The session itself is rdweb's RemoteDesktop; the panel adds its server,
 * login token, web session id and the account (address book) passwords.
 * Sessions to other servers get none of the panel-only parts.
 */
function Session({
  peerId,
  config,
  other,
  mode,
  credentials,
  onLeave,
  onReconnect,
}: {
  peerId: string;
  config: WebClientConfig;
  other: OtherServer | null;
  mode?: Mode;
  credentials: WebClientCredentials | null;
  onLeave: () => void;
  onReconnect: () => void;
}) {
  const { t, i18n } = useTranslation();
  const own = !other;
  const server = useMemo<ServerConfig>(
    () =>
      other
        ? { rendezvousUrl: other.idUrl, relayUrl: () => relayUrlOf(other), key: other.key.trim() }
        : {
            rendezvousUrl: wsUrl(config.rendezvous_url, "/ws/id"),
            relayUrl: () => wsUrl(config.relay_url, "/ws/relay"),
            key: config.public_key,
            token: config.token ?? undefined,
          },
    [config, other],
  );
  const creds = useMemo<RemoteDesktopCredentials | null>(
    () =>
      own && credentials
        ? {
            rememberedHash: safeDecode(credentials.hash),
            password: credentials.password ?? undefined,
            fromPersonalBook: !!credentials.hash && !credentials.book,
          }
        : null,
    [own, credentials],
  );
  // Terminal sessions on our own server are remembered in the account (any browser).
  const shellStore = useMemo<ShellStore | undefined>(
    () =>
      own
        ? {
            list: async () =>
              (await webClientApi.shells(peerId)).items.map((x) => ({
                serviceId: x.service_id,
                hostname: x.hostname || undefined,
                tabs: x.tabs,
                createdAt: Date.parse(x.created_at),
                updatedAt: Date.parse(x.updated_at),
              })),
            save: (sh) => webClientApi.saveShell(peerId, { service_id: sh.serviceId, hostname: sh.hostname ?? "", tabs: sh.tabs }),
            remove: (id) => webClientApi.forgetShell(peerId, id),
          }
        : undefined,
    [own, peerId],
  );
  return (
    <RemoteDesktop
      peerId={peerId}
      server={server}
      shellStore={shellStore}
      lang={i18n.language.startsWith("ru") ? "ru" : "en"}
      mode={mode}
      storageKey={RDPANEL_STORAGE_KEY}
      messages={{ toolbar: { back: t("webclient.toolbar.back") }, rememberAccountFailed: t("webclient.rememberAccountFailed") }}
      rememberLabel={own ? t("webclient.rememberPassword") : undefined}
      rememberHint={own ? t("webclient.rememberHint") : undefined}
      credentials={creds}
      myName={config.display_name}
      myId={own ? config.my_id || undefined : undefined}
      onRemembered={own ? (h) => webClientApi.remember(peerId, encodeBase64(h)) : undefined}
      onForget={own ? () => webClientApi.forget(peerId) : undefined}
      onReconnect={onReconnect}
      onLeave={onLeave}
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
  if (enabled && q.isLoading) {
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
  onConnect: (id: string, server: OtherServer | null, mode?: Mode) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const recent = useMemo(() => loadRecent(RDPANEL_STORAGE_KEY), []);
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
        <div className="grid grid-cols-2 gap-2">
          {(["files", "terminal"] as const).map((m) => (
            <Button
              key={m}
              type="button"
              variant="outline"
              disabled={!peerId.trim()}
              onClick={() => {
                const id = peerId.replace(/\s+/g, "");
                if (id) onConnect(id, server, m);
              }}
            >
              {m === "files" ? <FolderOpen className="h-4 w-4" /> : <SquareTerminal className="h-4 w-4" />}
              {t(`webclient.mode.${m}`)}
            </Button>
          ))}
        </div>
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
        <p className="text-right font-mono text-[11px] text-muted-foreground/70">rdweb {RDWEB_VERSION}</p>
      </form>
    </div>
  );
}
