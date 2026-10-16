import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  KeyRound,
  Loader2,
  MoreHorizontal,
  Pause,
  Play,
  Plus,
  Power,
  Radar,
  RefreshCw,
  ShieldCheck,
  ShieldOff,
  SlidersHorizontal,
  Trash2,
} from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  fromOverrideDraft,
  mbps,
  RELAY_NUM_KEYS,
  relayKeys,
  relaysApi,
  toOverrideDraft,
  type OverrideDraft,
  type Relay,
  type RelayEnrollment,
  type RelayList,
} from "@/api/relays";
import { ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, KeyValue, Mono, RelativeTime, SectionTitle, useErrorMessage } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch, Tabs, TabsContent, TabsList, TabsTrigger, Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { isHostPort } from "@/lib/validate";

type Health = Relay["health"];

const HEALTH_TONE: Record<Health, string> = {
  active: "border-emerald-500/40 text-emerald-700 dark:text-emerald-400",
  degraded: "border-amber-500/50 text-amber-700 dark:text-amber-400",
  offline: "border-red-500/50 text-red-700 dark:text-red-400",
  pending: "text-muted-foreground",
};
const HEALTH_DOT: Record<Health, string> = {
  active: "bg-emerald-500",
  degraded: "bg-amber-500",
  offline: "bg-red-500",
  pending: "bg-zinc-400",
};

export function HealthBadge({ health }: { health: Health }) {
  const { t } = useTranslation();
  return (
    <Badge variant="outline" className={cn("gap-1.5 font-normal", HEALTH_TONE[health])}>
      <span className={cn("h-2 w-2 rounded-full", HEALTH_DOT[health])} />
      {t(`relays.health.${health}`)}
    </Badge>
  );
}

/** Toasts the server's validation message (more precise than the generic one). */
function useRelayError() {
  const msg = useErrorMessage();
  return (err: unknown) => {
    if (err instanceof ApiError && err.code === "validation_failed" && err.message) toast.error(err.message);
    else toast.error(msg(err));
  };
}

/** One-line status of an agent relay for the table. */
function AgentState({ r }: { r: Relay }) {
  const { t } = useTranslation();
  const a = r.agent;
  if (!a) return <span className="text-muted-foreground">—</span>;
  if (a.revoked_at) return <span className="text-destructive">{t("relays.agent.revoked")}</span>;
  if (!a.enrolled) return <span className="text-muted-foreground">{a.token_pending ? t("relays.agent.waitingEnroll") : t("relays.agent.noToken")}</span>;
  if (!a.approved) return <span className="text-amber-700 dark:text-amber-400">{t("relays.agent.needsApproval")}</span>;
  return (
    <span className="flex items-center gap-1.5">
      <span className={cn("h-2 w-2 rounded-full", a.connected ? "bg-emerald-500" : "bg-zinc-400")} />
      {a.connected ? t("relays.agent.connected") : t("relays.agent.disconnected")}
      {a.acked_version < a.desired_version && <span className="text-xs text-amber-700 dark:text-amber-400">· {t("relays.agent.configPending")}</span>}
    </span>
  );
}

function RotationBar({ data }: { data: RelayList }) {
  const { t } = useTranslation();
  const rot = data.rotation;
  const members = rot.members.map((m) => (m.weight > 1 ? `${m.name} ×${m.weight}` : m.name)).join(", ");
  const observed = rot.observed ?? [];
  const mismatch = rot.observed != null && rot.servers.length > 0 && observed.some((o) => !rot.servers.includes(o));
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border bg-card px-4 py-3 text-sm">
      <span className="font-medium">{t("relays.rotation")}:</span>
      {rot.members.length ? <span className="break-all">{members}</span> : <span className="text-muted-foreground">{t("relays.rotationUnmanaged")}</span>}
      {rot.fallback && (
        <span className="flex items-center gap-1 text-amber-700 dark:text-amber-400">
          <AlertTriangle className="h-4 w-4" />
          {t("relays.fallback")}
        </span>
      )}
      {rot.shrunk && <span className="text-muted-foreground">· {t("relays.shrunk")}</span>}
      {rot.error && <span className="text-destructive">· {rot.error}</span>}
      <span className="text-muted-foreground" title={observed.join(", ")}>
        · {t("relays.observed", { count: observed.length })}
        {mismatch && ` (${t("relays.observedMismatch")})`}
      </span>
    </div>
  );
}

function OptionsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const onError = useRelayError();
  const q = useQuery({ queryKey: relayKeys.options, queryFn: relaysApi.options });
  const save = useMutation({
    mutationFn: relaysApi.updateOptions,
    onSuccess: (o) => {
      qc.setQueryData(relayKeys.options, o);
      toast.success(t("common.saved"));
    },
    onError,
  });
  if (!q.data) return null;
  const o = q.data;
  return (
    <Card>
      <CardContent className="grid gap-4 p-4 md:grid-cols-2">
        <label className="flex items-start justify-between gap-3 text-sm">
          <span>
            <span className="font-medium">{t("relays.options.requireApproval")}</span>
            <span className="block text-xs text-muted-foreground">{t("relays.options.requireApprovalHint")}</span>
          </span>
          <Switch checked={o.require_approval} disabled={save.isPending} onCheckedChange={(v) => save.mutate({ ...o, require_approval: v })} />
        </label>
        <label className="flex items-start justify-between gap-3 text-sm">
          <span>
            <span className="font-medium">{t("relays.options.allowPrivate")}</span>
            <span className="block text-xs text-muted-foreground">{t("relays.options.allowPrivateHint")}</span>
          </span>
          <Switch checked={o.allow_private_hosts} disabled={save.isPending} onCheckedChange={(v) => save.mutate({ ...o, allow_private_hosts: v })} />
        </label>
      </CardContent>
    </Card>
  );
}

/* ── add relay ─────────────────────────────────────────────────────────── */

function AddRelayDialog({
  kind,
  onOpenChange,
  onEnrollment,
}: {
  kind: "static" | "agent" | null;
  onOpenChange: (o: boolean) => void;
  onEnrollment: (id: number, e: RelayEnrollment) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const onError = useRelayError();
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [region, setRegion] = useState("");
  const [weight, setWeight] = useState("1");
  useEffect(() => {
    if (kind) {
      setName("");
      setHost("");
      setRegion("");
      setWeight("1");
    }
  }, [kind]);
  const hostOk = isHostPort(host.trim());
  const save = useMutation({
    mutationFn: () =>
      relaysApi.create({
        kind: kind ?? "static",
        name: name.trim(),
        public_host: host.trim(),
        region: region.trim() || null,
        weight: Number(weight),
        in_rotation: true,
      }),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: relayKeys.all });
      onOpenChange(false);
      if (res.enrollment) onEnrollment(res.relay.id, res.enrollment);
      else toast.success(t("relays.added", { name: res.relay.name }));
    },
    onError,
  });
  return (
    <Dialog open={kind !== null} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form
          className="space-y-4"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim() && hostOk) save.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{kind === "agent" ? t("relays.addAgent") : t("relays.addStatic")}</DialogTitle>
            <DialogDescription>{kind === "agent" ? t("relays.addAgentHint") : t("relays.addStaticHint")}</DialogDescription>
          </DialogHeader>
          <Field label={t("relays.name")} htmlFor="relay-name">
            <Input id="relay-name" autoFocus maxLength={64} value={name} onChange={(e) => setName(e.target.value)} placeholder="fra-1" />
          </Field>
          <Field
            label={t("relays.publicHost")}
            htmlFor="relay-host"
            hint={t("relays.publicHostHint")}
            error={host && !hostOk ? t("server.errors.hostPort") : undefined}
          >
            <Input id="relay-host" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} placeholder="relay-de.example.com:21117" />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label={t("relays.region")} htmlFor="relay-region">
              <Input id="relay-region" maxLength={32} value={region} onChange={(e) => setRegion(e.target.value)} placeholder="DE" />
            </Field>
            <Field label={t("relays.weight")} htmlFor="relay-weight" hint={t("relays.weightHint")}>
              <Select value={weight} onValueChange={setWeight}>
                <SelectTrigger id="relay-weight">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {[0, 1, 2, 3, 4, 5].map((w) => (
                    <SelectItem key={w} value={String(w)}>
                      {w}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={save.isPending || !name.trim() || !hostOk}>
              {t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** A copyable command or file. */
function Snippet({ value }: { value: string }) {
  return (
    <div className="relative">
      <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-3 pr-10 font-mono text-xs leading-relaxed">{value}</pre>
      <CopyButton value={value} className="absolute right-2 top-2" />
    </div>
  );
}

/** Shows the one-time token and install commands; flips live when the agent enrolls/connects. */
function EnrollmentDialog({ target, onClose, relays }: { target: { id: number; e: RelayEnrollment } | null; onClose: () => void; relays: Relay[] }) {
  const { t } = useTranslation();
  const r = target ? relays.find((x) => x.id === target.id) : undefined;
  const a = r?.agent;
  const steps: [boolean, string][] = [
    [!!a?.enrolled, t("relays.enroll.stepEnrolled")],
    [!!a?.approved, a?.require_approval ? t("relays.enroll.stepApproved") : t("relays.enroll.stepAutoApproved")],
    [!!a?.connected, t("relays.enroll.stepConnected")],
    [r?.health === "active", t("relays.enroll.stepProbe")],
  ];
  return (
    <Dialog open={target !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("relays.enroll.title", { name: r?.name ?? "" })}</DialogTitle>
          <DialogDescription>{t("relays.enroll.hint")}</DialogDescription>
        </DialogHeader>
        {target && (
          <div className="min-w-0 space-y-3">
            <Tabs defaultValue="install">
              <TabsList>
                <TabsTrigger value="install">{t("relays.enroll.tabInstall")}</TabsTrigger>
                <TabsTrigger value="docker">{t("relays.enroll.tabDocker")}</TabsTrigger>
                <TabsTrigger value="compose">{t("relays.enroll.tabCompose")}</TabsTrigger>
              </TabsList>
              <TabsContent value="install" className="mt-3 space-y-2">
                <Snippet value={target.e.install_command} />
                <p className="text-xs text-muted-foreground">{t("relays.enroll.installHint")}</p>
              </TabsContent>
              <TabsContent value="docker" className="mt-3 space-y-2">
                <Snippet value={target.e.docker_run} />
                <p className="text-xs text-muted-foreground">{t("relays.enroll.dockerHint")}</p>
              </TabsContent>
              <TabsContent value="compose" className="mt-3 space-y-2">
                <Snippet value={target.e.compose} />
                <p className="text-xs text-muted-foreground">{t("relays.enroll.composeHint")}</p>
              </TabsContent>
            </Tabs>
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <span className="text-muted-foreground">{t("relays.enroll.token")}:</span>
              <Mono className="break-all">{target.e.token}</Mono>
              <CopyButton value={target.e.token} />
            </div>
            <p role="note" className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-800 dark:text-amber-300">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>
                {t("relays.enroll.warning")} {t("relays.enroll.expires")} <RelativeTime iso={target.e.expires_at} />.
              </span>
            </p>
            <ul className="space-y-1 text-sm">
              {steps.map(([done, label]) => (
                <li key={label} className="flex items-center gap-2">
                  {done ? <CheckCircle2 className="h-4 w-4 text-emerald-600" /> : <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
                  {label}
                </li>
              ))}
            </ul>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{t("common.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/* ── relay drawer ─────────────────────────────────────────────────────── */

function EditSection({ r }: { r: Relay }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const onError = useRelayError();
  const [name, setName] = useState(r.name);
  const [host, setHost] = useState(r.public_host);
  const [region, setRegion] = useState(r.region ?? "");
  const [weight, setWeight] = useState(String(r.weight));
  const [inRotation, setInRotation] = useState(r.in_rotation);
  useEffect(() => {
    setName(r.name);
    setHost(r.public_host);
    setRegion(r.region ?? "");
    setWeight(String(r.weight));
    setInRotation(r.in_rotation);
  }, [r.id, r.name, r.public_host, r.region, r.weight, r.in_rotation]);
  const save = useMutation({
    mutationFn: () =>
      relaysApi.update(r.id, { name: name.trim(), public_host: host.trim(), region: region.trim() || null, weight: Number(weight), in_rotation: inRotation }),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: relayKeys.all });
    },
    onError,
  });
  const hostOk = isHostPort(host.trim());
  return (
    <div className="space-y-3">
      <SectionTitle>{t("relays.drawer.settings")}</SectionTitle>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("relays.name")} htmlFor="rd-name">
          <Input id="rd-name" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t("relays.region")} htmlFor="rd-region">
          <Input id="rd-region" maxLength={32} value={region} onChange={(e) => setRegion(e.target.value)} />
        </Field>
        <Field label={t("relays.publicHost")} htmlFor="rd-host" error={!hostOk ? t("server.errors.hostPort") : undefined} className="sm:col-span-2">
          <Input id="rd-host" className="font-mono" value={host} onChange={(e) => setHost(e.target.value)} />
        </Field>
        <Field label={t("relays.weight")} htmlFor="rd-weight" hint={t("relays.weightHint")}>
          <Select value={weight} onValueChange={setWeight}>
            <SelectTrigger id="rd-weight">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {[0, 1, 2, 3, 4, 5].map((w) => (
                <SelectItem key={w} value={String(w)}>
                  {w}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <label className="flex items-center justify-between gap-3 self-end rounded-md border px-3 py-2 text-sm">
          {t("relays.inRotation")}
          <Switch checked={inRotation} onCheckedChange={setInRotation} />
        </label>
      </div>
      <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending || !name.trim() || !hostOk}>
        {t("common.save")}
      </Button>
    </div>
  );
}

function OverridesSection({ r }: { r: Relay }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const onError = useRelayError();
  const q = useQuery({ queryKey: relayKeys.settings(r.id), queryFn: () => relaysApi.settings(r.id) });
  const [draft, setDraft] = useState<OverrideDraft | null>(null);
  useEffect(() => {
    if (q.data) setDraft(toOverrideDraft(q.data.overrides));
  }, [q.data]);
  const body = useMemo(() => (draft ? fromOverrideDraft(draft) : null), [draft]);
  const save = useMutation({
    mutationFn: () => relaysApi.updateSettings(r.id, body ?? {}),
    onSuccess: (s) => {
      qc.setQueryData(relayKeys.settings(r.id), s);
      void qc.invalidateQueries({ queryKey: relayKeys.list });
      toast.success(t("relays.overrides.saved"));
    },
    onError,
  });
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || !draft) return <Skeleton className="h-40" />;
  const g = q.data.global;
  const fmtGlobal = (v: unknown) => (v == null ? t("relays.overrides.unset") : Array.isArray(v) ? t("relays.overrides.entries", { count: v.length }) : String(v));
  const listRow = (k: "blacklist" | "blocklist") => (
    <div key={k} className="space-y-1.5">
      <label className="flex items-center justify-between gap-2 text-sm">
        <span>
          {t(`server.keys.${k}`)}
          <span className="ml-2 text-xs text-muted-foreground">
            {t("relays.overrides.global")}: {fmtGlobal(g[k])}
          </span>
        </span>
        <span className="flex items-center gap-2 text-xs text-muted-foreground">
          {t("relays.overrides.override")}
          <Switch checked={draft[k] !== null} onCheckedChange={(on) => setDraft({ ...draft, [k]: on ? (g[k] ?? []).join("\n") : null })} />
        </span>
      </label>
      {draft[k] !== null && <Textarea rows={3} className="font-mono" value={draft[k] ?? ""} onChange={(e) => setDraft({ ...draft, [k]: e.target.value })} />}
    </div>
  );
  return (
    <div className="space-y-3">
      <SectionTitle>{t("relays.overrides.title")}</SectionTitle>
      <p className="text-xs text-muted-foreground">{t("relays.overrides.hint")}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        {RELAY_NUM_KEYS.map((k) => (
          <Field key={k} label={t(`server.keys.${k}`)} htmlFor={`ov-${k}`} hint={`${t("relays.overrides.global")}: ${fmtGlobal(g[k])}`}>
            <Input
              id={`ov-${k}`}
              inputMode="decimal"
              className="font-mono"
              placeholder={t("relays.overrides.inherit")}
              value={draft.nums[k]}
              onChange={(e) => setDraft({ ...draft, nums: { ...draft.nums, [k]: e.target.value } })}
            />
          </Field>
        ))}
      </div>
      {listRow("blacklist")}
      {listRow("blocklist")}
      {!body && <p className="text-xs text-destructive">{t("server.errors.positive")}</p>}
      <Button size="sm" onClick={() => save.mutate()} disabled={save.isPending || !body}>
        {t("relays.overrides.save")}
      </Button>
    </div>
  );
}

function UsageSection({ r }: { r: Relay }) {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: relayKeys.usage(r.id), queryFn: () => relaysApi.usage(r.id), refetchInterval: 10_000, retry: false });
  return (
    <div className="space-y-2">
      <SectionTitle actions={<Button variant="ghost" size="sm" onClick={() => void q.refetch()}><RefreshCw className={cn(q.isFetching && "animate-spin")} /></Button>}>
        {t("relays.drawer.usage")}
      </SectionTitle>
      {q.isError ? (
        <p className="text-sm text-muted-foreground">{t("relays.drawer.usageUnavailable")}</p>
      ) : !q.data ? (
        <Skeleton className="h-16" />
      ) : q.data.items.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("relays.drawer.noSessions")}</p>
      ) : (
        <div className="overflow-x-auto rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("relays.drawer.peer")}</TableHead>
                <TableHead className="text-right">MB</TableHead>
                <TableHead className="text-right">kb/s</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {q.data.items.map((u) => (
                <TableRow key={u.peer}>
                  <TableCell>
                    <Mono>{u.peer}</Mono>
                  </TableCell>
                  <TableCell className="text-right">{u.total_mb.toFixed(1)}</TableCell>
                  <TableCell className="text-right">{u.current_kbps}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

function RelayDrawer({ r, onClose }: { r: Relay | undefined; onClose: () => void }) {
  const { t } = useTranslation();
  const a = r?.agent;
  const st = a?.status;
  return (
    <Sheet open={!!r} onOpenChange={(o) => !o && onClose()}>
      <SheetContent closeLabel={t("common.close")}>
        {r && (
          <>
            <SheetHeader>
              <SheetTitle className="flex flex-wrap items-center gap-2">
                {r.name}
                <Badge variant="secondary" className="font-normal">
                  {t(`relays.kind.${r.kind}`)}
                </Badge>
                <HealthBadge health={r.health} />
              </SheetTitle>
              <SheetDescription>
                <Mono>{r.public_host}</Mono>
              </SheetDescription>
            </SheetHeader>
            <SheetBody className="space-y-6">
              <KeyValue
                items={[
                  [t("relays.drawer.lastProbe"), <span key="p">{r.last_probe_at ? <RelativeTime iso={r.last_probe_at} /> : "—"}{r.last_probe_ms != null && ` · ${r.last_probe_ms.toFixed(1)} ms`}</span>],
                  ...(r.last_probe_error ? ([[t("relays.drawer.probeError"), <span key="e" className="text-destructive">{r.last_probe_error}</span>]] as [ReactNode, ReactNode][]) : []),
                  [t("relays.drawer.healthSince"), <RelativeTime key="h" iso={r.health_since} />],
                  [t("relays.rotationCol"), r.rotation_weight ? `×${r.rotation_weight}` : t("relays.notInRotation")],
                  [t("relays.adminState"), t(`relays.state.${r.admin_state}`)],
                ]}
              />
              {a && (
                <div className="space-y-2">
                  <SectionTitle>{t("relays.drawer.agent")}</SectionTitle>
                  <KeyValue
                    items={[
                      [t("relays.drawer.session"), <AgentState key="s" r={r} />],
                      [t("relays.drawer.host"), `${a.hostname ?? "—"}${a.arch ? ` (${a.arch})` : ""}`],
                      [t("relays.drawer.versions"), `rdpanel ${a.agent_version ?? "?"}${a.hbbr_version ? ` · ${a.hbbr_version}` : ""}`],
                      [t("relays.drawer.enrolled"), a.enrolled_at ? <span key="en"><RelativeTime iso={a.enrolled_at} />{a.enrolled_from_ip && ` · ${a.enrolled_from_ip}`}</span> : "—"],
                      [t("relays.drawer.config"), `v${a.acked_version} / v${a.desired_version}`],
                      ...(st
                        ? ([
                            [
                              "hbbr",
                              <span key="hb" className="flex flex-wrap items-center gap-2">
                                {st.hbbr_up ? t("relays.drawer.hbbrUp") : t("relays.drawer.hbbrDown")}
                                {st.hbbr_restarts > 0 && <span className="text-xs text-muted-foreground">{t("relays.drawer.restarts", { count: st.hbbr_restarts })}</span>}
                                {st.key_fp && (
                                  <span className={cn("flex items-center gap-1 text-xs", st.key_matches === false ? "text-destructive" : "text-muted-foreground")}>
                                    <KeyRound className="h-3 w-3" />
                                    {st.key_fp}
                                    {st.key_matches === false && ` · ${t("relays.drawer.keyMismatch")}`}
                                  </span>
                                )}
                              </span>,
                            ],
                            [t("relays.drawer.load"), `${st.relays_active} ${t("relays.sessions")} · ↓${mbps(st.rx_bps)} ↑${mbps(st.tx_bps)} Mb/s · load ${st.load1.toFixed(2)}`],
                          ] as [ReactNode, ReactNode][])
                        : []),
                    ]}
                  />
                  {Object.keys(a.ack_errors).length > 0 && (
                    <div className="rounded-md border border-destructive/40 bg-destructive/5 p-2 text-xs text-destructive">
                      {Object.entries(a.ack_errors).map(([k, v]) => (
                        <div key={k}>
                          <Mono>{k}</Mono>: {v}
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )}
              <EditSection r={r} />
              {r.kind === "agent" && <OverridesSection r={r} />}
              {(r.kind === "local" || (r.kind === "agent" && a?.connected)) && <UsageSection r={r} />}
            </SheetBody>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

/* ── tab ──────────────────────────────────────────────────────────────── */

export function RelaysTab() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const onError = useRelayError();
  const q = useQuery({ queryKey: relayKeys.list, queryFn: relaysApi.list, refetchInterval: 15_000 });
  const [adding, setAdding] = useState<"static" | "agent" | null>(null);
  const [enrolling, setEnrolling] = useState<{ id: number; e: RelayEnrollment } | null>(null);
  const [openId, setOpenId] = useState<number | null>(null);
  const [deleting, setDeleting] = useState<Relay | null>(null);
  const [resetting, setResetting] = useState<Relay | null>(null);
  const invalidate = () => void qc.invalidateQueries({ queryKey: relayKeys.all });
  const act = useMutation({
    mutationFn: ({ r, op }: { r: Relay; op: "probe" | "approve" | "revoke" | "drain" | "enable" | "disable" }) => {
      switch (op) {
        case "probe":
          return relaysApi.probe(r.id);
        case "approve":
          return relaysApi.approve(r.id);
        case "revoke":
          return relaysApi.revoke(r.id);
        case "drain":
          return relaysApi.update(r.id, { admin_state: "draining" });
        case "enable":
          return relaysApi.update(r.id, { admin_state: "enabled" });
        case "disable":
          return relaysApi.update(r.id, { admin_state: "disabled" });
      }
    },
    onSuccess: (r, v) => {
      invalidate();
      if (v.op === "probe") {
        if (r.last_probe_error) toast.warning(t("relays.probeFailed", { error: r.last_probe_error }));
        else toast.success(t("relays.probeOk", { ms: r.last_probe_ms?.toFixed(1) ?? "?" }));
      } else toast.success(t("common.saved"));
    },
    onError,
  });
  const del = useMutation({
    mutationFn: (r: Relay) => relaysApi.remove(r.id),
    onSuccess: () => {
      setDeleting(null);
      invalidate();
      toast.success(t("relays.deleted"));
    },
    onError,
  });
  const reset = useMutation({
    mutationFn: (r: Relay) => relaysApi.resetEnrollment(r.id),
    onSuccess: (e, r) => {
      setResetting(null);
      invalidate();
      setEnrolling({ id: r.id, e });
    },
    onError,
  });

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-64" />;
  const data = q.data;
  const open = data.items.find((r) => r.id === openId);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <RotationBar data={data} />
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button>
              <Plus />
              {t("relays.add")}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => setAdding("agent")}>
              <Radar />
              {t("relays.addAgent")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => setAdding("static")}>
              <CircleDashed />
              {t("relays.addStatic")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {data.items.length === 0 ? (
        <EmptyState icon={Radar} title={t("relays.empty")} description={t("relays.emptyHint")} />
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("relays.name")}</TableHead>
                <TableHead>{t("relays.healthCol")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("relays.agentCol")}</TableHead>
                <TableHead className="hidden sm:table-cell text-right">{t("relays.sessions")}</TableHead>
                <TableHead className="text-right">{t("relays.rotationCol")}</TableHead>
                <TableHead className="w-12">
                  <span className="sr-only">{t("common.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.items.map((r) => (
                <TableRow key={r.id} className="cursor-pointer" onClick={() => setOpenId(r.id)}>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-1.5 font-medium">
                      {r.name}
                      <Badge variant="secondary" className="font-normal">
                        {t(`relays.kind.${r.kind}`)}
                      </Badge>
                      {r.region && <Badge variant="outline" className="font-normal">{r.region}</Badge>}
                      {r.admin_state !== "enabled" && <Badge variant="outline" className="font-normal text-amber-700 dark:text-amber-400">{t(`relays.state.${r.admin_state}`)}</Badge>}
                    </div>
                    <Mono className="block text-xs text-muted-foreground">{r.public_host}</Mono>
                  </TableCell>
                  <TableCell>
                    <HealthBadge health={r.health} />
                    <div className="mt-0.5 max-w-[16rem] truncate text-xs text-muted-foreground" title={r.last_probe_error ?? undefined}>
                      {r.last_probe_error ? r.last_probe_error : r.last_probe_ms != null ? `${r.last_probe_ms.toFixed(1)} ms` : ""}
                    </div>
                  </TableCell>
                  <TableCell className="hidden text-sm md:table-cell">
                    <AgentState r={r} />
                  </TableCell>
                  <TableCell className="hidden text-right sm:table-cell">{r.agent?.status ? r.agent.status.relays_active : "—"}</TableCell>
                  <TableCell className="text-right">
                    {r.rotation_weight ? <span>×{r.rotation_weight}</span> : <span className="text-muted-foreground">—</span>}
                    <div className="text-xs text-muted-foreground">{t("relays.weightShort", { w: r.weight })}</div>
                  </TableCell>
                  <TableCell onClick={(e) => e.stopPropagation()}>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button variant="ghost" size="icon" aria-label={t("common.more")}>
                          <MoreHorizontal />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onSelect={() => setOpenId(r.id)}>
                          <SlidersHorizontal />
                          {t("common.details")}
                        </DropdownMenuItem>
                        <DropdownMenuItem onSelect={() => act.mutate({ r, op: "probe" })}>
                          <Radar />
                          {t("relays.probe")}
                        </DropdownMenuItem>
                        {r.agent?.enrolled && !r.agent.approved && !r.agent.revoked_at && (
                          <DropdownMenuItem onSelect={() => act.mutate({ r, op: "approve" })}>
                            <ShieldCheck />
                            {t("relays.approve")}
                          </DropdownMenuItem>
                        )}
                        <DropdownMenuSeparator />
                        {r.admin_state === "enabled" ? (
                          <DropdownMenuItem onSelect={() => act.mutate({ r, op: "drain" })}>
                            <Pause />
                            {t("relays.drain")}
                          </DropdownMenuItem>
                        ) : (
                          <DropdownMenuItem onSelect={() => act.mutate({ r, op: "enable" })}>
                            <Play />
                            {t("relays.enable")}
                          </DropdownMenuItem>
                        )}
                        {r.admin_state !== "disabled" && (
                          <DropdownMenuItem onSelect={() => act.mutate({ r, op: "disable" })}>
                            <Power />
                            {t("relays.disable")}
                          </DropdownMenuItem>
                        )}
                        {r.kind === "agent" && (
                          <>
                            <DropdownMenuSeparator />
                            <DropdownMenuItem onSelect={() => setResetting(r)}>
                              <KeyRound />
                              {t("relays.newToken")}
                            </DropdownMenuItem>
                            {r.agent?.enrolled && !r.agent.revoked_at && (
                              <DropdownMenuItem onSelect={() => act.mutate({ r, op: "revoke" })} className="text-destructive">
                                <ShieldOff />
                                {t("relays.revoke")}
                              </DropdownMenuItem>
                            )}
                          </>
                        )}
                        {r.kind !== "local" && (
                          <DropdownMenuItem onSelect={() => setDeleting(r)} className="text-destructive">
                            <Trash2 />
                            {t("common.delete")}
                          </DropdownMenuItem>
                        )}
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <OptionsCard />

      <AddRelayDialog kind={adding} onOpenChange={(o) => !o && setAdding(null)} onEnrollment={(id, e) => setEnrolling({ id, e })} />
      <EnrollmentDialog target={enrolling} relays={data.items} onClose={() => setEnrolling(null)} />
      <RelayDrawer r={open} onClose={() => setOpenId(null)} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("relays.deleteTitle", { name: deleting?.name ?? "" })}
        description={t("relays.deleteHint")}
        confirmLabel={t("common.delete")}
        destructive
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting)}
      />
      <ConfirmDialog
        open={resetting !== null}
        onOpenChange={(o) => !o && setResetting(null)}
        title={t("relays.newTokenTitle", { name: resetting?.name ?? "" })}
        description={t("relays.newTokenHint")}
        confirmLabel={t("relays.newToken")}
        pending={reset.isPending}
        onConfirm={() => resetting && reset.mutate(resetting)}
      />
    </div>
  );
}
