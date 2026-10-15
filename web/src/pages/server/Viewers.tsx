import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ColumnDef } from "@tanstack/react-table";
import { RefreshCw, Trash2 } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import type { Schemas } from "@/api/client";
import { serverApi } from "@/api/system";
import { EmptyState, Mono, RelativeTime, useToastError } from "@/components";
import { DataTable, SearchInput, type ColumnMeta } from "@/components/data-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/misc";
import { formatNumber } from "@/lib/format";
import { useTableSearch } from "@/lib/use-table";

function Refresh({ onClick, spinning }: { onClick: () => void; spinning: boolean }) {
  const { t } = useTranslation();
  return (
    <Button variant="outline" size="sm" onClick={onClick} disabled={spinning}>
      <RefreshCw className={spinning ? "animate-spin" : undefined} />
      {t("common.refresh")}
    </Button>
  );
}

function IpBlocker() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const table = useTableSearch({ prefix: "ib_" });
  const { state, update } = table;
  const q = useQuery({ queryKey: ["server", "ip-blocker", table.query], queryFn: () => serverApi.ipBlocker(table.query), placeholderData: keepPreviousData });
  const unblock = useMutation({
    mutationFn: (ip: string) => serverApi.unblockIp(ip),
    onSuccess: (_r, ip) => {
      toast.success(t("server.unblocked", { ip }));
      void qc.invalidateQueries({ queryKey: ["server", "ip-blocker"] });
    },
    onError: toastError,
  });
  const columns = useMemo<ColumnDef<Schemas["IpBlockerEntry"], unknown>[]>(
    () => [
      { id: "ip", header: t("server.ip"), enableSorting: false, meta: { label: t("server.ip") } satisfies ColumnMeta, cell: ({ row }) => <Mono>{row.original.ip}</Mono> },
      {
        id: "attempts",
        header: t("server.attempts"),
        enableSorting: false,
        meta: { label: t("server.attempts") } satisfies ColumnMeta,
        cell: ({ row }) => `${row.original.attempts} / ${row.original.attempts_window_s}s`,
      },
      {
        id: "ids",
        header: t("server.ids"),
        enableSorting: false,
        meta: { label: t("server.ids") } satisfies ColumnMeta,
        cell: ({ row }) => `${row.original.ids} / ${Math.round(row.original.ids_window_s / 3600)}h`,
      },
      {
        id: "blocked",
        header: t("server.state"),
        enableSorting: false,
        meta: { label: t("server.state") } satisfies ColumnMeta,
        cell: ({ row }) => (row.original.blocked ? <Badge variant="destructive">{t("server.blocked")}</Badge> : <Badge variant="outline">{t("server.watching")}</Badge>),
      },
      {
        id: "_a",
        header: () => <span className="sr-only">{t("common.actions")}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { className: "w-24 text-right" } satisfies ColumnMeta,
        cell: ({ row }) => (
          <Button variant="ghost" size="sm" onClick={() => unblock.mutate(row.original.ip)} disabled={unblock.isPending}>
            {t("server.unblock")}
          </Button>
        ),
      },
    ],
    [t, unblock],
  );
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t("server.ipBlockerHint")}</p>
        <Refresh onClick={() => void q.refetch()} spinning={q.isFetching} />
      </div>
      <DataTable
        id="ip-blocker"
        columns={columns}
        data={q.data?.items}
        total={q.data?.total}
        isLoading={q.isLoading}
        error={q.error}
        onRetry={() => void q.refetch()}
        page={state.page}
        pageSize={state.pageSize}
        onPageChange={(page) => update({ page })}
        sort={undefined}
        getRowId={(r) => r.ip}
        empty={<EmptyState title={t("server.ipBlockerEmpty")} />}
      />
    </div>
  );
}

function IpChanges() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const table = useTableSearch({ prefix: "ic_" });
  const { state, update } = table;
  const q = useQuery({ queryKey: ["server", "ip-changes", table.query], queryFn: () => serverApi.ipChanges(table.query), placeholderData: keepPreviousData });
  const clear = useMutation({
    mutationFn: (id: string) => serverApi.clearIpChanges(id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["server", "ip-changes"] }),
    onError: toastError,
  });
  const columns = useMemo<ColumnDef<Schemas["IpChangesEntry"], unknown>[]>(
    () => [
      { id: "id", header: t("devices.rustdeskId"), enableSorting: false, meta: { label: t("devices.rustdeskId") } satisfies ColumnMeta, cell: ({ row }) => <Mono>{row.original.rustdesk_id}</Mono> },
      {
        id: "ips",
        header: t("server.ips"),
        enableSorting: false,
        meta: { label: t("server.ips") } satisfies ColumnMeta,
        cell: ({ row }) => (
          <div className="flex flex-wrap gap-1">
            {Object.entries(row.original.ips).map(([ip, n]) => (
              <Badge key={ip} variant="secondary" className="font-mono font-normal">
                {ip} × {n}
              </Badge>
            ))}
          </div>
        ),
      },
      {
        id: "age",
        header: t("server.age"),
        enableSorting: false,
        meta: { label: t("server.age") } satisfies ColumnMeta,
        cell: ({ row }) => <RelativeTime iso={new Date(Date.now() - row.original.seconds * 1000).toISOString()} />,
      },
      {
        id: "_a",
        header: () => <span className="sr-only">{t("common.actions")}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { className: "w-12" } satisfies ColumnMeta,
        cell: ({ row }) => (
          <Button variant="ghost" size="icon" className="h-7 w-7" onClick={() => clear.mutate(row.original.rustdesk_id)} aria-label={t("server.forget")}>
            <Trash2 />
          </Button>
        ),
      },
    ],
    [t, clear],
  );
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-sm text-muted-foreground">{t("server.ipChangesHint")}</p>
        <Refresh onClick={() => void q.refetch()} spinning={q.isFetching} />
      </div>
      <DataTable
        id="ip-changes"
        columns={columns}
        data={q.data?.items}
        total={q.data?.total}
        isLoading={q.isLoading}
        error={q.error}
        onRetry={() => void q.refetch()}
        page={state.page}
        pageSize={state.pageSize}
        onPageChange={(page) => update({ page })}
        sort={undefined}
        getRowId={(r) => r.rustdesk_id}
        empty={<EmptyState title={t("server.ipChangesEmpty")} />}
      />
    </div>
  );
}

function PunchRequests() {
  const { t } = useTranslation();
  const table = useTableSearch({ prefix: "pr_" });
  const { state, update } = table;
  const q = useQuery({ queryKey: ["server", "punch", table.query], queryFn: () => serverApi.punchRequests(table.query), placeholderData: keepPreviousData });
  const columns = useMemo<ColumnDef<Schemas["PunchRequest"], unknown>[]>(
    () => [
      { id: "at", header: t("audit.time"), enableSorting: false, meta: { label: t("audit.time") } satisfies ColumnMeta, cell: ({ row }) => <RelativeTime iso={row.original.at} /> },
      { id: "from", header: t("server.fromIp"), enableSorting: false, meta: { label: t("server.fromIp") } satisfies ColumnMeta, cell: ({ row }) => <Mono>{row.original.from_ip}</Mono> },
      { id: "to", header: t("server.toId"), enableSorting: false, meta: { label: t("server.toId") } satisfies ColumnMeta, cell: ({ row }) => <Mono>{row.original.to_rustdesk_id}</Mono> },
      { id: "toip", header: t("server.toIp"), enableSorting: false, meta: { label: t("server.toIp") } satisfies ColumnMeta, cell: ({ row }) => <Mono className="text-muted-foreground">{row.original.to_ip ?? "—"}</Mono> },
    ],
    [t],
  );
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <SearchInput value={table.text} onChange={table.setText} placeholder={t("server.punchSearch")} />
        <Refresh onClick={() => void q.refetch()} spinning={q.isFetching} />
      </div>
      <DataTable
        id="punch"
        columns={columns}
        data={q.data?.items}
        total={q.data?.total}
        isLoading={q.isLoading}
        error={q.error}
        onRetry={() => void q.refetch()}
        page={state.page}
        pageSize={state.pageSize}
        onPageChange={(page) => update({ page })}
        onPageSizeChange={(pageSize) => update({ pageSize })}
        sort={undefined}
        getRowId={(r) => String(r.id)}
        empty={<EmptyState title={t("server.punchEmpty")} />}
      />
    </div>
  );
}

function RelayUsage() {
  const { t, i18n } = useTranslation();
  const q = useQuery({ queryKey: ["server", "relay-usage"], queryFn: serverApi.relayUsage, refetchInterval: 10_000 });
  const columns = useMemo<ColumnDef<Schemas["RelayUsage"], unknown>[]>(
    () => [
      { id: "peer", header: t("server.peer"), enableSorting: false, meta: { label: t("server.peer") } satisfies ColumnMeta, cell: ({ row }) => <Mono>{row.original.peer}</Mono> },
      {
        id: "elapsed",
        header: t("server.elapsed"),
        enableSorting: false,
        meta: { label: t("server.elapsed") } satisfies ColumnMeta,
        cell: ({ row }) => `${Math.floor(row.original.elapsed_s / 60)}m ${row.original.elapsed_s % 60}s`,
      },
      { id: "total", header: t("server.total"), enableSorting: false, meta: { label: t("server.total"), className: "text-right" } satisfies ColumnMeta, cell: ({ row }) => `${formatNumber(row.original.total_mb, i18n.language, 2)} MB` },
      { id: "cur", header: t("server.current"), enableSorting: false, meta: { label: t("server.current"), className: "text-right" } satisfies ColumnMeta, cell: ({ row }) => `${formatNumber(row.original.current_kbps, i18n.language)} kb/s` },
      { id: "avg", header: t("server.avg"), enableSorting: false, meta: { label: t("server.avg"), className: "text-right" } satisfies ColumnMeta, cell: ({ row }) => `${formatNumber(row.original.avg_kbps, i18n.language)} kb/s` },
      { id: "max", header: t("server.peak"), enableSorting: false, meta: { label: t("server.peak"), className: "text-right" } satisfies ColumnMeta, cell: ({ row }) => `${formatNumber(row.original.highest_kbps, i18n.language)} kb/s` },
    ],
    [t, i18n.language],
  );
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2 text-sm text-muted-foreground">
        <span>
          {t("server.relayUsageHint")}
          {q.data && (
            <>
              {" · "}
              <RelativeTime iso={q.data.checked_at} />
            </>
          )}
        </span>
        <Refresh onClick={() => void q.refetch()} spinning={q.isFetching} />
      </div>
      <DataTable
        id="relay-usage"
        columns={columns}
        data={q.data?.items}
        total={q.data?.items.length}
        isLoading={q.isLoading}
        error={q.error}
        onRetry={() => void q.refetch()}
        page={1}
        pageSize={Math.max(50, q.data?.items.length ?? 0)}
        onPageChange={() => undefined}
        sort={undefined}
        getRowId={(r) => r.peer}
        empty={<EmptyState title={t("server.relayUsageEmpty")} />}
      />
    </div>
  );
}

export function Viewers() {
  const { t } = useTranslation();
  const table = useTableSearch({});
  const tab = typeof table.raw.view === "string" ? table.raw.view : "relays";
  return (
    <Tabs value={tab} onValueChange={(v) => table.setSearch({ view: v === "relays" ? undefined : v })}>
      <TabsList>
        <TabsTrigger value="relays">{t("server.viewers.relays")}</TabsTrigger>
        <TabsTrigger value="ib">{t("server.viewers.ipBlocker")}</TabsTrigger>
        <TabsTrigger value="ic">{t("server.viewers.ipChanges")}</TabsTrigger>
        <TabsTrigger value="pr">{t("server.viewers.punch")}</TabsTrigger>
      </TabsList>
      <TabsContent value="relays">{tab === "relays" && <RelayUsage />}</TabsContent>
      <TabsContent value="ib">{tab === "ib" && <IpBlocker />}</TabsContent>
      <TabsContent value="ic">{tab === "ic" && <IpChanges />}</TabsContent>
      <TabsContent value="pr">{tab === "pr" && <PunchRequests />}</TabsContent>
    </Tabs>
  );
}
