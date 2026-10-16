import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Save } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { serverApi, type ClientOptions, type ClientOptionsInput } from "@/api/system";
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
            <Textarea
              id="srv-ice_servers"
              rows={4}
              className="font-mono"
              value={draft.ice_servers}
              onChange={(e) => setDraft({ ...draft, ice_servers: e.target.value })}
              placeholder={"stun:stun.example.com:3478\nturn://user:pass@turn.example.com:3478"}
              autoComplete="off"
              spellCheck={false}
            />
            {error && <p className="text-xs text-destructive">{t(error)}</p>}
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
