import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ColumnDef } from "@tanstack/react-table";
import { ArrowDownToLine, ArrowUpFromLine, Bell, Clock, FileClock, FolderOpen, LogIn, Plug, ShieldAlert, UserCog } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import type { Schemas } from "@/api/client";
import { auditApi, settingsApi } from "@/api/system";
import { EmptyState, Field, Mono, PageHeader, RelativeTime, useToastError } from "@/components";
import { DataTable, FilterSelect, SearchInput, type ColumnMeta } from "@/components/data-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger, Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { formatDateTime, formatDuration } from "@/lib/format";
import { localInputToIso, rangeToQuery, RANGE_PRESETS } from "@/lib/time-range";
import { useTableSearch } from "@/lib/use-table";
import { ConnTypeBadge } from "@/pages/devices/DeviceDrawer";

type Kind = "connections" | "files" | "alarms" | "logins" | "admin";
const KINDS: Kind[] = ["connections", "files", "alarms", "logins", "admin"];

function RangeFilter({
  range,
  from,
  to,
  onChange,
}: {
  range: string;
  from: string;
  to: string;
  onChange: (p: { range?: string; from?: string; to?: string }) => void;
}) {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const [f, setF] = useState(from);
  const [tt, setTt] = useState(to);
  useEffect(() => {
    setF(from);
    setTt(to);
  }, [from, to]);
  return (
    <div className="flex items-center gap-1">
      <Select
        value={range}
        onValueChange={(v) => {
          if (v === "custom") setOpen(true);
          else onChange({ range: v === "7d" ? undefined : v, from: undefined, to: undefined });
        }}
      >
        <SelectTrigger className={cn("h-9 w-auto min-w-[9.5rem]", range !== "7d" && "border-primary/50 bg-primary/5")} aria-label={t("audit.range.label")}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {RANGE_PRESETS.map((p) => (
            <SelectItem key={p} value={p}>
              {t(`audit.range.${p}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="ghost" size="sm" className="h-9 px-2" aria-label={t("audit.range.custom")}>
            <Clock />
            {range === "custom" && (
              <span className="hidden text-xs md:inline">
                {from ? formatDateTime(localInputToIso(from), i18n.language) : "…"} – {to ? formatDateTime(localInputToIso(to), i18n.language) : "…"}
              </span>
            )}
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-72 space-y-3">
          <Field label={t("audit.range.from")} htmlFor="r-from">
            <Input id="r-from" type="datetime-local" value={f} onChange={(e) => setF(e.target.value)} />
          </Field>
          <Field label={t("audit.range.to")} htmlFor="r-to">
            <Input id="r-to" type="datetime-local" value={tt} onChange={(e) => setTt(e.target.value)} />
          </Field>
          <div className="flex justify-end">
            <Button
              size="sm"
              onClick={() => {
                onChange({ range: "custom", from: f || undefined, to: tt || undefined });
                setOpen(false);
              }}
            >
              {t("common.apply")}
            </Button>
          </div>
        </PopoverContent>
      </Popover>
    </div>
  );
}

function JsonCell({ value }: { value: Record<string, unknown> | undefined }) {
  if (!value || Object.keys(value).length === 0) return <span className="text-muted-foreground">—</span>;
  const s = JSON.stringify(value);
  return (
    <code className="line-clamp-1 max-w-[22rem] break-all text-xs text-muted-foreground" title={JSON.stringify(value, null, 2)}>
      {s}
    </code>
  );
}

function useAuditColumns(kind: Kind): ColumnDef<never, unknown>[] {
  const { t } = useTranslation();
  return useMemo(() => {
    const time = (key: string, label: string): ColumnDef<never, unknown> => ({
      id: key,
      header: label,
      meta: { label } satisfies ColumnMeta,
      cell: ({ row }) => <RelativeTime iso={(row.original as Record<string, string>)[key]} />,
    });
    const device = (): ColumnDef<never, unknown> => ({
      id: "device",
      header: t("audit.device"),
      enableSorting: false,
      meta: { label: t("audit.device") } satisfies ColumnMeta,
      cell: ({ row }) => {
        const r = row.original as { device_alias?: string | null; device_rustdesk_id: string };
        return (
          <div className="min-w-0">
            {r.device_alias && <div className="truncate font-medium">{r.device_alias}</div>}
            <Mono className={r.device_alias ? "text-xs text-muted-foreground" : undefined}>{r.device_rustdesk_id}</Mono>
          </div>
        );
      },
    });
    const controller = (): ColumnDef<never, unknown> => ({
      id: "controller",
      header: t("audit.controller"),
      enableSorting: false,
      meta: { label: t("audit.controller") } satisfies ColumnMeta,
      cell: ({ row }) => {
        const r = row.original as { controller_name?: string | null; controller_rustdesk_id?: string | null; controller_ip?: string | null };
        return (
          <div className="min-w-0">
            <div className="truncate">{r.controller_name || r.controller_rustdesk_id || "—"}</div>
            <div className="truncate text-xs text-muted-foreground">
              {r.controller_name && r.controller_rustdesk_id && <Mono>{r.controller_rustdesk_id}</Mono>}
              {r.controller_ip && <Mono> · {r.controller_ip}</Mono>}
            </div>
          </div>
        );
      },
    });
    switch (kind) {
      case "connections":
        return [
          time("started_at", t("audit.started")),
          device(),
          controller(),
          {
            id: "conn_type",
            header: t("audit.type"),
            enableSorting: false,
            meta: { label: t("audit.type") } satisfies ColumnMeta,
            cell: ({ row }) => <ConnTypeBadge type={(row.original as Schemas["ConnectionAudit"]).conn_type} />,
          },
          {
            id: "ended_at",
            header: t("audit.duration"),
            meta: { label: t("audit.duration") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["ConnectionAudit"];
              return r.ended_at ? (
                <span className="tabular-nums">{formatDuration(r.started_at, r.ended_at)}</span>
              ) : (
                <Badge variant="success">{t("audit.active")}</Badge>
              );
            },
          },
          {
            id: "note",
            header: t("audit.note"),
            enableSorting: false,
            meta: { label: t("audit.note"), defaultHidden: true } satisfies ColumnMeta,
            cell: ({ row }) => <span className="text-muted-foreground">{(row.original as Schemas["ConnectionAudit"]).note || "—"}</span>,
          },
          {
            id: "session_id",
            header: t("audit.session"),
            enableSorting: false,
            meta: { label: t("audit.session"), defaultHidden: true } satisfies ColumnMeta,
            cell: ({ row }) => <Mono className="text-xs text-muted-foreground">{(row.original as Schemas["ConnectionAudit"]).session_id ?? "—"}</Mono>,
          },
        ];
      case "files":
        return [
          time("created_at", t("audit.time")),
          device(),
          controller(),
          {
            id: "direction",
            header: t("audit.direction"),
            enableSorting: false,
            meta: { label: t("audit.direction") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["FileTransferAudit"];
              return (
                <span className="inline-flex items-center gap-1 whitespace-nowrap">
                  {r.direction === 0 ? <ArrowDownToLine className="h-3.5 w-3.5" /> : <ArrowUpFromLine className="h-3.5 w-3.5" />}
                  {t(`audit.dir.${r.direction}`)}
                </span>
              );
            },
          },
          {
            id: "path",
            header: t("audit.path"),
            enableSorting: false,
            meta: { label: t("audit.path"), className: "max-w-[24rem]" } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["FileTransferAudit"];
              return (
                <div className="flex min-w-0 items-center gap-1.5" title={r.path}>
                  {!r.is_file && <FolderOpen className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />}
                  <Mono className="truncate text-xs">{r.path}</Mono>
                  {r.file_count > 1 && <Badge variant="secondary">{r.file_count}</Badge>}
                </div>
              );
            },
          },
          {
            id: "info",
            header: t("audit.info"),
            enableSorting: false,
            meta: { label: t("audit.info"), defaultHidden: true } satisfies ColumnMeta,
            cell: ({ row }) => <JsonCell value={(row.original as Schemas["FileTransferAudit"]).info} />,
          },
        ];
      case "alarms":
        return [
          time("created_at", t("audit.time")),
          device(),
          {
            id: "alarm_type",
            header: t("audit.alarmType"),
            enableSorting: false,
            meta: { label: t("audit.alarmType") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const type = (row.original as Schemas["AlarmAudit"]).alarm_type;
              return (
                <Badge variant="destructive" className="font-normal">
                  {t(`audit.alarm.${type}`, { defaultValue: t("audit.alarm.unknown", { type }) })}
                </Badge>
              );
            },
          },
          {
            id: "ip",
            header: t("audit.ip"),
            enableSorting: false,
            meta: { label: t("audit.ip") } satisfies ColumnMeta,
            cell: ({ row }) => <Mono>{(row.original as Schemas["AlarmAudit"]).ip ?? "—"}</Mono>,
          },
          {
            id: "info",
            header: t("audit.info"),
            enableSorting: false,
            meta: { label: t("audit.info") } satisfies ColumnMeta,
            cell: ({ row }) => <JsonCell value={(row.original as Schemas["AlarmAudit"]).info} />,
          },
        ];
      case "logins":
        return [
          time("created_at", t("audit.time")),
          {
            id: "user",
            header: t("audit.user"),
            enableSorting: false,
            meta: { label: t("audit.user") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["LoginEvent"];
              return r.user ? (
                <span className="font-medium">{r.user.username}</span>
              ) : (
                <span className="text-muted-foreground">{r.username_attempted ?? "—"}</span>
              );
            },
          },
          {
            id: "result",
            header: t("audit.result"),
            enableSorting: false,
            meta: { label: t("audit.result") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["LoginEvent"];
              return r.result === "success" ? (
                <Badge variant="success">{t("audit.success")}</Badge>
              ) : (
                <span className="inline-flex items-center gap-1.5">
                  <Badge variant="destructive">{t("audit.failure")}</Badge>
                  {r.failure_reason && <span className="text-xs text-muted-foreground">{t(`audit.reason.${r.failure_reason}`, { defaultValue: r.failure_reason })}</span>}
                </span>
              );
            },
          },
          {
            id: "method",
            header: t("audit.method"),
            enableSorting: false,
            meta: { label: t("audit.method") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["LoginEvent"];
              return (
                <span className="whitespace-nowrap">
                  {t(`audit.methods.${r.method}`)}
                  {r.provider_slug && <span className="text-muted-foreground"> · {r.provider_slug}</span>}
                </span>
              );
            },
          },
          {
            id: "client_kind",
            header: t("audit.client"),
            enableSorting: false,
            meta: { label: t("audit.client") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["LoginEvent"];
              return (
                <span className="whitespace-nowrap">
                  {t(`audit.clientKind.${r.client_kind}`)}
                  {r.platform && <span className="text-muted-foreground"> · {r.platform}</span>}
                  {r.device_rustdesk_id && <Mono className="text-muted-foreground"> · {r.device_rustdesk_id}</Mono>}
                </span>
              );
            },
          },
          {
            id: "ip",
            header: t("audit.ip"),
            enableSorting: false,
            meta: { label: t("audit.ip") } satisfies ColumnMeta,
            cell: ({ row }) => <Mono className="text-muted-foreground">{(row.original as Schemas["LoginEvent"]).ip ?? "—"}</Mono>,
          },
        ];
      case "admin":
        return [
          time("created_at", t("audit.time")),
          {
            id: "actor",
            header: t("audit.actor"),
            enableSorting: false,
            meta: { label: t("audit.actor") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["AdminAuditEvent"];
              return (
                <div>
                  <div className="font-medium">{r.actor_name}</div>
                  {r.actor_ip && <Mono className="text-xs text-muted-foreground">{r.actor_ip}</Mono>}
                </div>
              );
            },
          },
          {
            id: "action",
            header: t("audit.action"),
            enableSorting: false,
            meta: { label: t("audit.action") } satisfies ColumnMeta,
            cell: ({ row }) => <Badge variant="outline" className="font-mono font-normal">{(row.original as Schemas["AdminAuditEvent"]).action}</Badge>,
          },
          {
            id: "target",
            header: t("audit.target"),
            enableSorting: false,
            meta: { label: t("audit.target") } satisfies ColumnMeta,
            cell: ({ row }) => {
              const r = row.original as Schemas["AdminAuditEvent"];
              return (
                <span>
                  {r.target_label ?? r.target_id ?? "—"}
                  {r.target_type && <span className="text-xs text-muted-foreground"> · {r.target_type}</span>}
                </span>
              );
            },
          },
          {
            id: "details",
            header: t("audit.details"),
            enableSorting: false,
            meta: { label: t("audit.details") } satisfies ColumnMeta,
            cell: ({ row }) => <JsonCell value={(row.original as Schemas["AdminAuditEvent"]).details} />,
          },
        ];
    }
  }, [kind, t]);
}

function fetchAudit(kind: Kind, q: Record<string, unknown>): Promise<{ items: unknown[]; total: number }> {
  switch (kind) {
    case "connections":
      return auditApi.connections(q);
    case "files":
      return auditApi.files(q);
    case "alarms":
      return auditApi.alarms(q);
    case "logins":
      return auditApi.logins(q);
    case "admin":
      return auditApi.admin(q);
  }
}

const KIND_FILTERS: Record<Kind, string[]> = {
  connections: ["range", "from", "to", "conn_type", "active"],
  files: ["range", "from", "to", "direction"],
  alarms: ["range", "from", "to"],
  logins: ["range", "from", "to", "result", "method", "client_kind"],
  admin: ["range", "from", "to", "action"],
};

/** Table state (URL) → audit API query. Exported for tests. */
export function auditQuery(query: Record<string, string | number | boolean>, now = Date.now()): Record<string, unknown> {
  const { range, from, to, active, ...rest } = query;
  const out: Record<string, unknown> = { ...rest, ...rangeToQuery(range, { from, to }, now) };
  if (active === true || active === "true") out.active = true;
  return out;
}

function AuditTable({ kind }: { kind: Kind }) {
  const { t } = useTranslation();
  const table = useTableSearch({ sort: kind === "connections" ? "-started_at" : "-created_at", filterKeys: KIND_FILTERS[kind], prefix: `${kind[0]}${kind[1]}_` });
  const { state, update } = table;
  // Rounded "now" keeps the query key stable for a minute (no refetch loop on re-render).
  const minute = Math.floor(Date.now() / 60_000) * 60_000;
  const query = useMemo(() => auditQuery(table.query, minute), [table.query, minute]);
  const columns = useAuditColumns(kind);
  const list = useQuery({
    queryKey: ["audit", kind, query],
    queryFn: () => fetchAudit(kind, query),
    placeholderData: keepPreviousData,
  });
  const f = state.filters;
  const sel = (k: string) => (f[k] === undefined ? "" : String(f[k]));

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SearchInput value={table.text} onChange={table.setText} placeholder={t(`audit.search.${kind}`)} />
        <RangeFilter
          range={sel("range") || "7d"}
          from={sel("from")}
          to={sel("to")}
          onChange={(p) => update({ filters: { range: p.range ?? "", from: p.from ?? "", to: p.to ?? "" } })}
        />
        {kind === "connections" && (
          <>
            <FilterSelect
              value={sel("conn_type")}
              onChange={(v) => update({ filters: { conn_type: v } })}
              placeholder={t("audit.type")}
              options={[0, 1, 2, 3, 4].map((n) => ({ value: String(n), label: t(`audit.connType.${n}`) }))}
            />
            <FilterSelect
              value={sel("active")}
              onChange={(v) => update({ filters: { active: v } })}
              placeholder={t("audit.state")}
              options={[{ value: "true", label: t("audit.activeOnly") }]}
            />
          </>
        )}
        {kind === "files" && (
          <FilterSelect
            value={sel("direction")}
            onChange={(v) => update({ filters: { direction: v } })}
            placeholder={t("audit.direction")}
            options={[0, 1].map((n) => ({ value: String(n), label: t(`audit.dir.${n}`) }))}
          />
        )}
        {kind === "logins" && (
          <>
            <FilterSelect
              value={sel("result")}
              onChange={(v) => update({ filters: { result: v } })}
              placeholder={t("audit.result")}
              options={[
                { value: "success", label: t("audit.success") },
                { value: "failure", label: t("audit.failure") },
              ]}
            />
            <FilterSelect
              value={sel("method")}
              onChange={(v) => update({ filters: { method: v } })}
              placeholder={t("audit.method")}
              options={["password", "oidc", "webauth"].map((m) => ({ value: m, label: t(`audit.methods.${m}`) }))}
            />
            <FilterSelect
              value={sel("client_kind")}
              onChange={(v) => update({ filters: { client_kind: v } })}
              placeholder={t("audit.client")}
              options={["web", "client"].map((m) => ({ value: m, label: t(`audit.clientKind.${m}`) }))}
            />
          </>
        )}
        {kind === "admin" && (
          <FilterSelect
            value={sel("action")}
            onChange={(v) => update({ filters: { action: v } })}
            placeholder={t("audit.action")}
            options={["user.", "device.", "address_book.", "server.", "settings.", "identity_provider."].map((a) => ({ value: a, label: `${a}*` }))}
          />
        )}
      </div>
      <DataTable
        id={`audit-${kind}`}
        columns={columns as ColumnDef<unknown, unknown>[]}
        data={list.data?.items as unknown[] | undefined}
        total={list.data?.total}
        isLoading={list.isLoading}
        error={list.error}
        onRetry={() => void list.refetch()}
        page={state.page}
        pageSize={state.pageSize}
        onPageChange={(page) => update({ page })}
        onPageSizeChange={(pageSize) => update({ pageSize })}
        sort={state.sort}
        onSortChange={(sort) => update({ sort: sort ?? (kind === "connections" ? "-started_at" : "-created_at") })}
        getRowId={(r) => String((r as { id: number }).id)}
        empty={<EmptyState icon={FileClock} title={t("audit.empty")} description={t("audit.emptyHint")} />}
      />
    </div>
  );
}

const RETENTION_KEYS = [
  "login_events_days",
  "connection_audits_days",
  "file_transfer_audits_days",
  "alarm_audits_days",
  "admin_audit_days",
  "punch_requests_days",
] as const;

function RetentionDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const settings = useQuery({ queryKey: ["settings"], queryFn: settingsApi.get, enabled: open });
  const [values, setValues] = useState<Record<string, string>>({});
  useEffect(() => {
    if (settings.data) setValues(Object.fromEntries(RETENTION_KEYS.map((k) => [k, String(settings.data.retention[k])])));
  }, [settings.data]);
  const valid = RETENTION_KEYS.every((k) => /^\d+$/.test(values[k] ?? ""));
  const save = useMutation({
    mutationFn: () => settingsApi.update({ retention: Object.fromEntries(RETENTION_KEYS.map((k) => [k, Number(values[k])])) }),
    onSuccess: (s) => {
      qc.setQueryData(["settings"], s);
      toast.success(t("common.saved"));
      onOpenChange(false);
    },
    onError: toastError,
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{t("audit.retention")}</DialogTitle>
          <DialogDescription>{t("settings.retentionHint")}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          {RETENTION_KEYS.map((k) => (
            <Field key={k} label={t(`settings.retentionKeys.${k}`)} htmlFor={`ret-${k}`}>
              <Input
                id={`ret-${k}`}
                inputMode="numeric"
                value={values[k] ?? ""}
                onChange={(e) => setValues((v) => ({ ...v, [k]: e.target.value }))}
                aria-invalid={!/^\d+$/.test(values[k] ?? "")}
              />
            </Field>
          ))}
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button disabled={!valid || save.isPending || !settings.data} onClick={() => save.mutate()}>
            {t("common.save")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function AuditPage() {
  const { t } = useTranslation();
  const table = useTableSearch({});
  const tab: Kind = KINDS.includes(table.raw.tab as Kind) ? (table.raw.tab as Kind) : "connections";
  const [retention, setRetention] = useState(false);
  const icons: Record<Kind, ReactNode> = {
    connections: <Plug />,
    files: <FolderOpen />,
    alarms: <Bell />,
    logins: <LogIn />,
    admin: <UserCog />,
  };
  return (
    <div>
      <PageHeader
        title={t("nav.audit")}
        description={t("audit.description")}
        actions={
          <Button variant="outline" size="sm" onClick={() => setRetention(true)}>
            <ShieldAlert />
            {t("audit.retention")}
          </Button>
        }
      />
      <Tabs value={tab} onValueChange={(v) => table.setSearch({ tab: v === "connections" ? undefined : v })}>
        <TabsList>
          {KINDS.map((k) => (
            <TabsTrigger key={k} value={k}>
              {icons[k]}
              {t(`audit.tabs.${k}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        {KINDS.map((k) => (
          <TabsContent key={k} value={k}>
            {tab === k && <AuditTable kind={k} />}
          </TabsContent>
        ))}
      </Tabs>
      <RetentionDialog open={retention} onOpenChange={setRetention} />
    </div>
  );
}
