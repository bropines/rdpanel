import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, Save, Sparkles } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { serverApi, type ClientOptions, type ClientOptionsInput, type IceServerProbeItem } from "@/api/system";
import { ErrorState, RelativeTime, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton, Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type Tri = "default" | "on" | "off";

interface Draft {
  ice_servers: string;
  congestion: Tri;
}

interface Preset {
  id: string;
  servers: string;
}

const PRESETS: Preset[] = [
  {
    id: "global",
    servers: "stun:stun.cloudflare.com:3478\nstun:stun.l.google.com:19302\nstun:stun1.l.google.com:19302",
  },
  {
    id: "worldwide",
    servers: "stun:stun.cloudflare.com:3478\nstun:stun.l.google.com:19302\nstun:stun.syncthing.net:3478\nstun:stun.qq.com:3478",
  },
  {
    id: "ru",
    servers: "stun:stun.cloudflare.com:3478\nstun:stun.qq.com:3478\nstun:stun.syncthing.net:3478",
  },
  {
    id: "cloudflare",
    servers: "stun:stun.cloudflare.com:3478",
  },
  {
    id: "default",
    servers: "",
  },
];

const MAX_ICE_SERVERS = 16;
const STUN = /^stuns?:(\/\/)?[^\s,/@]+$/;
// RustDesk drops TURN servers without credentials, so require turn(s)://user:pass@host[:port].
const TURN = /^turns?:\/\/[^\s,:@/]+:[^\s,@/]+@[^\s,/@]+$/;

function toDraft(o: ClientOptions): Draft {
  const c = o.webrtc_congestion_control;
  return { ice_servers: o.ice_servers, congestion: c == null ? "default" : c ? "on" : "off" };
}

/** Splits the ICE server list like the server does (commas or newlines). Exported for tests. */
export function iceEntries(s: string): string[] {
  return s
    .split(/[,\r\n]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/** Returns an i18n error key, or null when the list is valid. Exported for tests. */
export function validateIceServers(s: string): string | null {
  const entries = iceEntries(s);
  if (entries.length > MAX_ICE_SERVERS) return "server.clientOptions.errors.tooMany";
  for (const e of entries) {
    if (/^turns?:/.test(e)) {
      if (!TURN.test(e)) return "server.clientOptions.errors.turn";
    } else if (!STUN.test(e)) return "server.clientOptions.errors.url";
  }
  return null;
}

/** The PUT body for a draft. Exported for tests. */
export function toInput(d: Draft): ClientOptionsInput {
  return {
    ice_servers: iceEntries(d.ice_servers).join("\n"),
    webrtc_congestion_control: d.congestion === "default" ? null : d.congestion === "on",
  };
}

/** Client options pushed to every RustDesk client in heartbeats (WebRTC ICE servers). */
export function ClientOptionsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const q = useQuery({ queryKey: ["server", "client-options"], queryFn: serverApi.clientOptions });
  const [draft, setDraft] = useState<Draft | null>(null);
  const [probeResults, setProbeResults] = useState<IceServerProbeItem[] | null>(null);

  useEffect(() => {
    if (q.data) setDraft(toDraft(q.data));
  }, [q.data]);

  const save = useMutation({
    mutationFn: (d: Draft) => serverApi.updateClientOptions(toInput(d)),
    onSuccess: (o) => {
      qc.setQueryData(["server", "client-options"], o);
      setDraft(toDraft(o));
      toast.success(t("server.clientOptions.saved"));
    },
    onError: toastError,
  });

  const probe = useMutation({
    mutationFn: (servers: string) => serverApi.probeIceServers({ ice_servers: servers }),
    onSuccess: (res) => {
      setProbeResults(res.items);
      const reachable = res.items.filter((x) => x.reachable).length;
      if (reachable === res.items.length) {
        toast.success(t("server.clientOptions.probe.success"));
      } else {
        toast.warning(t("server.clientOptions.probe.someFailed"));
      }
    },
    onError: toastError,
  });

  const fetchStun = useMutation({
    mutationFn: () => serverApi.fetchPublicStun({ limit: 6 }),
    onSuccess: (res) => {
      if (!draft) return;
      setProbeResults(res.items);
      const turnEntries = iceEntries(draft.ice_servers).filter((e) => /^turns?:/i.test(e));
      const newStuns = res.items.filter((r) => r.reachable).map((r) => r.server);
      const combined = [...newStuns, ...turnEntries];
      setDraft({ ...draft, ice_servers: combined.join("\n") });
      toast.success(t("server.clientOptions.fetchStun.success", { count: newStuns.length }));
    },
    onError: toastError,
  });

  const applyPreset = (presetServers: string) => {
    if (!draft) return;
    setProbeResults(null);
    // Keep existing TURN servers intact, replace STUN servers
    const turnEntries = iceEntries(draft.ice_servers).filter((e) => /^turns?:/i.test(e));
    const newStunEntries = iceEntries(presetServers).filter((e) => !/^turns?:/i.test(e));
    const combined = [...newStunEntries, ...turnEntries];
    setDraft({ ...draft, ice_servers: combined.join("\n") });
  };

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || !draft) return <Skeleton className="h-48" />;
  const base = toDraft(q.data);
  const dirty = draft.ice_servers !== base.ice_servers || draft.congestion !== base.congestion;
  const error = validateIceServers(draft.ice_servers);

  return (
    <Card>
      <CardHeader className="pb-0">
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {t("server.clientOptions.title")}
          {q.data.pushed ? (
            <Badge variant="outline" className="font-normal">
              {t("server.clientOptions.pushed")}
              {q.data.modified_at && (
                <>
                  {" · "}
                  <RelativeTime iso={q.data.modified_at} />
                </>
              )}
            </Badge>
          ) : (
            <Badge variant="outline" className="font-normal">{t("server.clientOptions.notPushed")}</Badge>
          )}
        </CardTitle>
        <CardDescription>{t("server.clientOptions.hint")}</CardDescription>
      </CardHeader>
      <CardContent>
        <div className="grid gap-2 border-b py-4 md:grid-cols-[minmax(12rem,16rem)_1fr]">
          <div className="space-y-1">
            <label htmlFor="srv-ice_servers" className="text-sm font-medium">
              {t("server.clientOptions.iceServers")}
            </label>
            <p className="text-xs text-muted-foreground">{t("server.clientOptions.iceServersHint")}</p>
            <span className="font-mono text-[11px] text-muted-foreground">ice-servers</span>
          </div>
          <div className="min-w-0 space-y-1.5">
            <div className="flex flex-wrap items-center gap-1.5 pb-1">
              <span className="text-xs text-muted-foreground mr-0.5">{t("server.clientOptions.presets.label")}:</span>
              {PRESETS.map((p) => (
                <Button
                  key={p.id}
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-6 text-xs px-2 rounded-full font-normal"
                  onClick={() => applyPreset(p.servers)}
                >
                  {t(`server.clientOptions.presets.${p.id}`)}
                </Button>
              ))}
            </div>
            <Textarea
              id="srv-ice_servers"
              rows={4}
              className="font-mono"
              value={draft.ice_servers}
              onChange={(e) => {
                setDraft({ ...draft, ice_servers: e.target.value });
                setProbeResults(null);
              }}
              placeholder={"stun:stun.example.com:3478\nturn://user:pass@turn.example.com:3478"}
              autoComplete="off"
              spellCheck={false}
            />
            {error && <p className="text-xs text-destructive">{t(error)}</p>}
            <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
              <div className="flex items-center gap-1.5">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 text-xs px-2"
                  disabled={!iceEntries(draft.ice_servers).length || !!error || probe.isPending}
                  onClick={() => probe.mutate(draft.ice_servers)}
                >
                  <Activity className="size-3.5 mr-1" />
                  {probe.isPending ? t("server.clientOptions.probe.testing") : t("server.clientOptions.probe.button")}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-7 text-xs px-2"
                  disabled={fetchStun.isPending}
                  onClick={() => fetchStun.mutate()}
                >
                  <Sparkles className="size-3.5 mr-1 text-primary" />
                  {fetchStun.isPending ? t("server.clientOptions.fetchStun.fetching") : t("server.clientOptions.fetchStun.button")}
                </Button>
              </div>
              {probeResults && probeResults.length > 0 && (
                <span className="text-xs text-muted-foreground font-mono">
                  {probeResults.filter((r) => r.reachable).length}/{probeResults.length} {t("server.clientOptions.probe.reachableCount")}
                </span>
              )}
            </div>
            {probeResults && probeResults.length > 0 && (
              <div className="flex flex-wrap gap-1.5 pt-1">
                {probeResults.map((r, i) => (
                  <Badge
                    key={i}
                    variant={r.reachable ? "outline" : "destructive"}
                    className={`text-[11px] font-mono font-normal ${r.reachable ? "border-emerald-500/50 text-emerald-600 dark:text-emerald-400 bg-emerald-500/10" : ""}`}
                  >
                    {r.server}
                    {r.reachable ? ` · ${r.rtt_ms} ms ✓` : ` · ${r.error || "failed"} ✗`}
                  </Badge>
                ))}
              </div>
            )}
          </div>
        </div>
        <div className="grid gap-2 py-4 md:grid-cols-[minmax(12rem,16rem)_1fr]">
          <div className="space-y-1">
            <label htmlFor="srv-webrtc_cc" className="text-sm font-medium">
              {t("server.clientOptions.congestion")}
            </label>
            <p className="text-xs text-muted-foreground">{t("server.clientOptions.congestionHint")}</p>
            <span className="font-mono text-[11px] text-muted-foreground">allow-webrtc-congestion-control</span>
          </div>
          <div className="min-w-0">
            <Select value={draft.congestion} onValueChange={(v) => setDraft({ ...draft, congestion: v as Tri })}>
              <SelectTrigger id="srv-webrtc_cc" className="w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(["default", "on", "off"] as const).map((v) => (
                  <SelectItem key={v} value={v}>
                    {t(`server.clientOptions.tri.${v}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
        <div className="flex justify-end">
          <Button size="sm" onClick={() => save.mutate(draft)} disabled={(!dirty && q.data.pushed) || !!error || save.isPending}>
            <Save />
            {t("server.clientOptions.save")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
