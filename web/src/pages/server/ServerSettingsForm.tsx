import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, CheckCircle2, RefreshCw, RotateCcw, Save } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import type { Schemas } from "@/api/client";
import { serverApi, type ServerSettingsPatch, type ServerSettingsState } from "@/api/system";
import { ErrorState, RelativeTime, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch, Textarea } from "@/components/ui/misc";
import { cn } from "@/lib/utils";
import { isHostPort, isIpOrCidr, splitLines } from "@/lib/validate";

type Key = Schemas["ServerSettingKey"];
type Values = Schemas["ServerSettingsValues"];

interface Draft {
  relay_servers: string;
  always_use_relay: boolean;
  must_login: boolean;
  tcp_keepalive: boolean;
  webrtc: boolean;
  blacklist: string;
  blocklist: string;
  limit_speed_mbps: string;
  total_bandwidth_mbps: string;
  single_bandwidth_mbps: string;
  downgrade_threshold: string;
  downgrade_start_check_s: string;
}

const NUM_KEYS = ["total_bandwidth_mbps", "single_bandwidth_mbps", "limit_speed_mbps", "downgrade_threshold", "downgrade_start_check_s"] as const;

function toDraft(v: Values): Draft {
  const n = (x: number | null | undefined) => (x == null ? "" : String(x));
  return {
    relay_servers: (v.relay_servers ?? []).join("\n"),
    always_use_relay: !!v.always_use_relay,
    must_login: !!v.must_login,
    tcp_keepalive: !!v.tcp_keepalive,
    webrtc: !!v.webrtc,
    blacklist: (v.blacklist ?? []).join("\n"),
    blocklist: (v.blocklist ?? []).join("\n"),
    limit_speed_mbps: n(v.limit_speed_mbps),
    total_bandwidth_mbps: n(v.total_bandwidth_mbps),
    single_bandwidth_mbps: n(v.single_bandwidth_mbps),
    downgrade_threshold: n(v.downgrade_threshold),
    downgrade_start_check_s: n(v.downgrade_start_check_s),
  };
}

/** Validates the draft; returns per-key i18n error keys. Exported for tests. */
export function validateDraft(d: Draft): Partial<Record<Key, string>> {
  const errors: Partial<Record<Key, string>> = {};
  if (splitLines(d.relay_servers).some((s) => !isHostPort(s))) errors.relay_servers = "server.errors.hostPort";
  if (splitLines(d.blacklist).some((s) => !isIpOrCidr(s))) errors.blacklist = "server.errors.ip";
  if (splitLines(d.blocklist).some((s) => !isIpOrCidr(s))) errors.blocklist = "server.errors.ip";
  for (const k of NUM_KEYS) {
    const v = d[k].trim();
    if (v === "") continue;
    const n = Number(v);
    if (!Number.isFinite(n) || n <= 0 || (k === "downgrade_start_check_s" && !Number.isInteger(n))) errors[k] = "server.errors.positive";
  }
  return errors;
}

/** Only changed keys go into the PATCH (unchanged = still managed as before). Exported for tests. */
export function diffDraft(desired: Values, d: Draft): ServerSettingsPatch {
  const base = toDraft(desired);
  const patch: ServerSettingsPatch = {};
  if (d.relay_servers !== base.relay_servers) patch.relay_servers = splitLines(d.relay_servers);
  if (d.always_use_relay !== base.always_use_relay) patch.always_use_relay = d.always_use_relay;
  if (d.must_login !== base.must_login) patch.must_login = d.must_login;
  if (d.tcp_keepalive !== base.tcp_keepalive) patch.tcp_keepalive = d.tcp_keepalive;
  if (d.webrtc !== base.webrtc) patch.webrtc = d.webrtc;
  if (d.blacklist !== base.blacklist) patch.blacklist = splitLines(d.blacklist);
  if (d.blocklist !== base.blocklist) patch.blocklist = splitLines(d.blocklist);
  for (const k of NUM_KEYS) {
    if (d[k].trim() !== base[k] && d[k].trim() !== "") patch[k] = Number(d[k]);
  }
  return patch;
}

function fmt(v: unknown, t: (k: string) => string): string {
  if (v === null || v === undefined) return t("server.unknown");
  if (typeof v === "boolean") return v ? t("common.yes") : t("common.no");
  if (Array.isArray(v)) return v.length ? v.join(", ") : t("server.empty");
  return String(v);
}

function SyncBadge({ status }: { status: Schemas["ServerSettingStatus"] | undefined }) {
  const { t } = useTranslation();
  if (!status) return null;
  if (status.last_error)
    return (
      <Badge variant="destructive" className="gap-1 font-normal" title={status.last_error}>
        <AlertTriangle className="h-3 w-3" />
        {t("server.error")}
      </Badge>
    );
  if (!status.managed) return <Badge variant="outline" className="font-normal">{t("server.unmanaged")}</Badge>;
  return status.in_sync ? (
    <Badge variant="outline" className="gap-1 border-emerald-500/40 font-normal text-emerald-700 dark:text-emerald-400">
      <CheckCircle2 className="h-3 w-3" />
      {t("server.inSync")}
    </Badge>
  ) : (
    <Badge variant="outline" className="gap-1 border-amber-500/50 font-normal text-amber-700 dark:text-amber-400">
      <RefreshCw className="h-3 w-3" />
      {t("server.drift")}
    </Badge>
  );
}

function Row({
  k,
  state,
  label,
  hint,
  error,
  dirty,
  children,
}: {
  k: Key;
  state: ServerSettingsState;
  label: string;
  hint?: ReactNode;
  error?: string;
  dirty: boolean;
  children: ReactNode;
}) {
  const { t } = useTranslation();
  const status = state.keys.find((s) => s.key === k);
  const applied = state.applied[k];
  return (
    <div className={cn("grid gap-2 border-b py-4 last:border-0 md:grid-cols-[minmax(12rem,16rem)_1fr]", dirty && "bg-primary/[0.03]")}>
      <div className="space-y-1">
        <label htmlFor={`srv-${k}`} className="flex flex-wrap items-center gap-2 text-sm font-medium">
          {label}
          {dirty && <Badge variant="secondary" className="font-normal">{t("server.changed")}</Badge>}
        </label>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
        <div className="flex flex-wrap items-center gap-1.5">
          <SyncBadge status={status} />
          <span className="font-mono text-[11px] text-muted-foreground">{status?.target}</span>
        </div>
      </div>
      <div className="min-w-0 space-y-1.5">
        {children}
        {error && <p className="text-xs text-destructive">{t(error)}</p>}
        <p className={cn("text-xs text-muted-foreground", status && !status.in_sync && status.managed && "text-amber-700 dark:text-amber-400")}>
          {t("server.applied")}: <span className="break-all font-mono">{fmt(applied, t)}</span>
          {status?.applied_at && (
            <>
              {" · "}
              <RelativeTime iso={status.applied_at} />
            </>
          )}
          {status?.updated_by && ` · ${t("server.changedBy", { name: status.updated_by })}`}
        </p>
        {status?.last_error && <p className="text-xs text-destructive">{status.last_error}</p>}
      </div>
    </div>
  );
}

export function ServerSettingsForm() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const q = useQuery({ queryKey: ["server", "settings"], queryFn: serverApi.settings, refetchInterval: 30_000 });
  const [draft, setDraft] = useState<Draft | null>(null);
  const [touched, setTouched] = useState(false);

  useEffect(() => {
    if (q.data && !touched) setDraft(toDraft(q.data.desired));
  }, [q.data, touched]);

  const errors = useMemo(() => (draft ? validateDraft(draft) : {}), [draft]);
  const patch = useMemo(() => (draft && q.data ? diffDraft(q.data.desired, draft) : {}), [draft, q.data]);
  const dirtyKeys = new Set(Object.keys(patch));
  const hasErrors = Object.keys(errors).length > 0;

  const onSaved = (s: ServerSettingsState) => {
    qc.setQueryData(["server", "settings"], s);
    setTouched(false);
    setDraft(toDraft(s.desired));
  };
  const save = useMutation({
    mutationFn: () => serverApi.updateSettings(patch),
    onSuccess: (s) => {
      onSaved(s);
      const failed = s.keys.filter((k) => k.last_error);
      if (failed.length) toast.warning(t("server.savedWithErrors", { count: failed.length }));
      else toast.success(t("server.saved"));
    },
    onError: toastError,
  });
  const reapply = useMutation({
    mutationFn: serverApi.reapply,
    onSuccess: (s) => {
      onSaved(s);
      toast.success(t("server.reapplied"));
    },
    onError: toastError,
  });

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || !draft) return <Skeleton className="h-96" />;
  const state = q.data;
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => {
    setTouched(true);
    setDraft((d) => (d ? { ...d, [k]: v } : d));
  };
  const outOfSync = state.keys.filter((k) => k.managed && !k.in_sync).length;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card px-4 py-3 text-sm">
        {outOfSync > 0 ? (
          <span className="flex items-center gap-2 text-amber-700 dark:text-amber-400">
            <AlertTriangle className="h-4 w-4" />
            {t("server.driftSummary", { count: outOfSync })}
          </span>
        ) : (
          <span className="flex items-center gap-2 text-emerald-700 dark:text-emerald-400">
            <CheckCircle2 className="h-4 w-4" />
            {t("server.allInSync")}
          </span>
        )}
        {state.checked_at && (
          <span className="text-muted-foreground">
            · {t("server.checked")} <RelativeTime iso={state.checked_at} />
          </span>
        )}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => reapply.mutate()} disabled={reapply.isPending}>
            <RefreshCw className={cn(reapply.isPending && "animate-spin")} />
            {t("server.reapply")}
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader className="pb-0">
          <CardTitle className="text-base">{t("server.rendezvous")}</CardTitle>
          <CardDescription>{t("server.rendezvousHint")}</CardDescription>
        </CardHeader>
        <CardContent>
          <Row k="relay_servers" state={state} label={t("server.keys.relay_servers")} hint={t("server.hints.relay_servers")} error={errors.relay_servers} dirty={dirtyKeys.has("relay_servers")}>
            <Textarea id="srv-relay_servers" rows={3} className="font-mono" value={draft.relay_servers} onChange={(e) => set("relay_servers", e.target.value)} placeholder="relay.example.com:21117" />
          </Row>
          <Row k="always_use_relay" state={state} label={t("server.keys.always_use_relay")} hint={t("server.hints.always_use_relay")} dirty={dirtyKeys.has("always_use_relay")}>
            <Switch id="srv-always_use_relay" checked={draft.always_use_relay} onCheckedChange={(v) => set("always_use_relay", v)} />
          </Row>
          <Row k="must_login" state={state} label={t("server.keys.must_login")} hint={t("server.hints.must_login")} dirty={dirtyKeys.has("must_login")}>
            <Switch id="srv-must_login" checked={draft.must_login} onCheckedChange={(v) => set("must_login", v)} />
          </Row>
          <Row k="tcp_keepalive" state={state} label={t("server.keys.tcp_keepalive")} hint={t("server.hints.tcp_keepalive")} dirty={dirtyKeys.has("tcp_keepalive")}>
            <Switch id="srv-tcp_keepalive" checked={draft.tcp_keepalive} onCheckedChange={(v) => set("tcp_keepalive", v)} />
          </Row>
          <Row k="webrtc" state={state} label={t("server.keys.webrtc")} hint={t("server.hints.webrtc")} dirty={dirtyKeys.has("webrtc")}>
            <Switch id="srv-webrtc" checked={draft.webrtc} onCheckedChange={(v) => set("webrtc", v)} />
          </Row>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-0">
          <CardTitle className="text-base">{t("server.relay")}</CardTitle>
          <CardDescription>{t("server.relayHint")}</CardDescription>
        </CardHeader>
        <CardContent>
          {NUM_KEYS.map((k) => (
            <Row key={k} k={k} state={state} label={t(`server.keys.${k}`)} hint={t(`server.hints.${k}`)} error={errors[k]} dirty={dirtyKeys.has(k)}>
              <Input id={`srv-${k}`} inputMode="decimal" className="max-w-[12rem] font-mono" value={draft[k]} onChange={(e) => set(k, e.target.value)} />
            </Row>
          ))}
          <Row k="blacklist" state={state} label={t("server.keys.blacklist")} hint={t("server.hints.blacklist")} error={errors.blacklist} dirty={dirtyKeys.has("blacklist")}>
            <Textarea id="srv-blacklist" rows={4} className="font-mono" value={draft.blacklist} onChange={(e) => set("blacklist", e.target.value)} placeholder="203.0.113.7" />
          </Row>
          <Row k="blocklist" state={state} label={t("server.keys.blocklist")} hint={t("server.hints.blocklist")} error={errors.blocklist} dirty={dirtyKeys.has("blocklist")}>
            <Textarea id="srv-blocklist" rows={4} className="font-mono" value={draft.blocklist} onChange={(e) => set("blocklist", e.target.value)} placeholder="198.51.100.0/24" />
          </Row>
        </CardContent>
      </Card>

      {dirtyKeys.size > 0 && (
        <div className="sticky bottom-3 z-10 flex flex-wrap items-center gap-3 rounded-lg border bg-background/95 px-4 py-3 shadow-lg backdrop-blur">
          <span className="text-sm">{t("server.pendingChanges", { count: dirtyKeys.size })}</span>
          <div className="ml-auto flex gap-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setTouched(false);
                setDraft(toDraft(state.desired));
              }}
            >
              <RotateCcw />
              {t("common.discard")}
            </Button>
            <Button size="sm" onClick={() => save.mutate()} disabled={hasErrors || save.isPending}>
              <Save />
              {t("server.saveApply")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
