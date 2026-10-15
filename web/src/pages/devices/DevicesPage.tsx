import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ColumnDef, RowSelectionState } from "@tanstack/react-table";
import { BookPlus, Download, FolderInput, MoreHorizontal, Power, Trash2, UserPlus, X } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { saveBlob } from "@/api/client";
import { deviceKeys, devicesApi, type Device, type DeviceListQuery } from "@/api/devices";
import { ConfirmDialog, CopyButton, EmptyState, Mono, OnlineDot, PageHeader, RelativeTime, useToastError } from "@/components";
import { DataTable, FilterSelect, SearchInput, type ColumnMeta } from "@/components/data-table";
import { AsyncCombobox } from "@/components/pickers";
import { groupsApi, lookupApi } from "@/api/users";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { formatRustdeskId, userLabel } from "@/lib/format";
import { useIsAdmin } from "@/lib/session";
import { useTableSearch } from "@/lib/use-table";
import { DeviceDrawer, deviceName, OsIcon } from "@/pages/devices/DeviceDrawer";
import { DeviceBulkDialog } from "@/pages/devices/dialogs";
import { Monitor } from "lucide-react";

const FILTER_KEYS = ["online", "owner", "device_group", "os", "version", "seen"] as const;
const SEEN_HOURS: Record<string, number> = { "24h": 24, "7d": 24 * 7, "30d": 24 * 30, "90d": 24 * 90 };

/** `seen=7d` (URL) → `seen_after` (API). Exported for tests. */
export function seenAfter(seen: unknown, now = Date.now()): string | undefined {
  const h = typeof seen === "string" ? SEEN_HOURS[seen] : undefined;
  return h ? new Date(now - h * 3600_000).toISOString() : undefined;
}

/** Builds the device list query from table state; `my` forces owner=me. */
export function buildDeviceQuery(
  query: Record<string, string | number | boolean>,
  my: boolean,
  now = Date.now(),
): DeviceListQuery {
  const { seen, online, ...rest } = query;
  const q: DeviceListQuery = { ...(rest as DeviceListQuery) };
  if (online === true || online === "true") q.online = true;
  else if (online === false || online === "false") q.online = false;
  const sa = seenAfter(seen, now);
  if (sa) q.seen_after = sa;
  if (my) q.owner = "me";
  return q;
}

type BulkMode = "owner" | "group" | "book";

function DevicesView({ my }: { my: boolean }) {
  const { t } = useTranslation();
  const isAdmin = useIsAdmin();
  const admin = isAdmin && !my;
  const qc = useQueryClient();
  const toastError = useToastError();
  const table = useTableSearch({ sort: "-last_seen_at", filterKeys: FILTER_KEYS });
  const { state, update } = table;
  const query = useMemo(() => buildDeviceQuery(table.query, my), [table.query, my]);
  const [selection, setSelection] = useState<RowSelectionState>({});
  const [bulk, setBulk] = useState<BulkMode | null>(null);
  const [confirm, setConfirm] = useState<"delete" | "kick" | null>(null);
  const openId = typeof table.raw.device === "number" ? table.raw.device : Number(table.raw.device) || undefined;

  const list = useQuery({
    queryKey: deviceKeys.list(query),
    queryFn: ({ signal }) => devicesApi.list(query, signal),
    placeholderData: keepPreviousData,
  });
  const facets = useQuery({ queryKey: deviceKeys.facets, queryFn: devicesApi.facets, staleTime: 60_000 });
  // Names for the active owner/group filter chips (the URL only holds ids).
  const ownerNames = useQuery({ queryKey: ["user-names"], queryFn: () => lookupApi.users(), enabled: admin, staleTime: 60_000 });
  const groupNames = useQuery({ queryKey: ["device-groups", "names"], queryFn: () => groupsApi.listDeviceGroups({ page_size: 500 }), enabled: admin, staleTime: 60_000 });
  const selectedIds = Object.keys(selection).filter((k) => selection[k]).map(Number);

  const bulkAction = useMutation({
    mutationFn: (action: "delete" | "kick") => devicesApi.bulk({ action, device_ids: selectedIds }),
    onSuccess: (res, action) => {
      toast.success(action === "delete" ? t("devices.deletedCount", { count: res.affected }) : t("devices.kickedCount", { count: res.affected }));
      setConfirm(null);
      setSelection({});
      void qc.invalidateQueries({ queryKey: deviceKeys.all });
    },
    onError: toastError,
  });

  const exportCsv = useMutation({
    mutationFn: () => {
      const { page: _p, page_size: _s, sort: _o, ...filters } = query;
      return devicesApi.exportCsv(filters);
    },
    onSuccess: ({ blob, filename }) => saveBlob(blob, filename ?? `devices-${new Date().toISOString().slice(0, 10)}.csv`),
    onError: toastError,
  });

  const columns = useMemo<ColumnDef<Device, unknown>[]>(() => {
    const cols: ColumnDef<Device, unknown>[] = [
      {
        id: "alias",
        header: t("devices.device"),
        meta: { label: t("devices.device"), className: "min-w-[12rem]" } satisfies ColumnMeta,
        enableHiding: false,
        cell: ({ row: { original: d } }) => (
          <div className="flex items-center gap-2.5">
            <OnlineDot online={d.online} />
            <div className="min-w-0">
              <div className="truncate font-medium">{deviceName(d)}</div>
              {d.alias && d.hostname && <div className="truncate text-xs text-muted-foreground">{d.hostname}</div>}
            </div>
          </div>
        ),
      },
      {
        id: "rustdesk_id",
        header: t("devices.rustdeskId"),
        meta: { label: t("devices.rustdeskId") } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => (
          <span className="inline-flex items-center gap-0.5 whitespace-nowrap">
            <Mono>{formatRustdeskId(d.rustdesk_id)}</Mono>
            <CopyButton value={d.rustdesk_id} label={t("devices.copyId")} />
          </span>
        ),
      },
      {
        id: "os",
        header: t("devices.os"),
        meta: { label: t("devices.os") } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => (
          <span className="inline-flex max-w-[14rem] items-center gap-1.5 truncate" title={d.os}>
            <OsIcon family={d.os_family} />
            <span className="truncate">{d.os || "—"}</span>
          </span>
        ),
      },
      {
        id: "client_version",
        header: t("devices.version"),
        meta: { label: t("devices.version") } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => <Mono>{d.client_version || "—"}</Mono>,
      },
    ];
    if (!my) {
      cols.push({
        id: "owner",
        header: t("devices.owner"),
        enableSorting: false,
        meta: { label: t("devices.owner") } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) =>
          d.owner ? <span className="truncate">{userLabel(d.owner)}</span> : <span className="text-muted-foreground">{t("devices.unassigned")}</span>,
      });
    }
    cols.push(
      {
        id: "group",
        header: t("devices.group"),
        enableSorting: false,
        meta: { label: t("devices.group"), defaultHidden: my } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => d.device_group?.name ?? <span className="text-muted-foreground">—</span>,
      },
      {
        id: "last_seen_at",
        header: t("devices.lastSeen"),
        meta: { label: t("devices.lastSeen") } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) =>
          d.online ? (
            <span className="whitespace-nowrap text-emerald-600 dark:text-emerald-400">{t("devices.onlineNow")}</span>
          ) : (
            <RelativeTime iso={d.last_seen_at} className="text-muted-foreground" />
          ),
      },
      {
        id: "last_seen_ip",
        header: t("devices.ip"),
        enableSorting: false,
        meta: { label: t("devices.ip"), defaultHidden: true } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => <Mono className="text-muted-foreground">{d.last_seen_ip ?? "—"}</Mono>,
      },
      {
        id: "note",
        header: t("devices.note"),
        enableSorting: false,
        meta: { label: t("devices.note"), defaultHidden: true, className: "max-w-[16rem]" } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => <span className="line-clamp-1 text-muted-foreground">{d.note || "—"}</span>,
      },
      {
        id: "created_at",
        header: t("devices.created"),
        meta: { label: t("devices.created"), defaultHidden: true } satisfies ColumnMeta,
        cell: ({ row: { original: d } }) => <RelativeTime iso={d.created_at} className="text-muted-foreground" />,
      },
    );
    return cols;
  }, [t, my]);

  const versionOptions = (facets.data?.versions ?? []).map((v) => ({ value: v.value, label: `${v.value} (${v.count})` }));
  const osOptions = (facets.data?.os ?? []).map((v) => ({ value: v.value, label: `${t(`os.${v.value}`)} (${v.count})` }));
  const f = state.filters;
  const anyFilter = Object.keys(f).length > 0 || state.q !== "";
  const ownerFilter = typeof f.owner === "string" ? f.owner : f.owner !== undefined ? String(f.owner) : "";
  const groupFilter = typeof f.device_group === "string" ? f.device_group : f.device_group !== undefined ? String(f.device_group) : "";

  return (
    <div>
      <PageHeader
        title={my ? t("nav.myDevices") : t("nav.devices")}
        description={my ? t("devices.myDescription") : t("devices.description")}
        actions={
          admin && (
            <Button variant="outline" size="sm" onClick={() => exportCsv.mutate()} disabled={exportCsv.isPending}>
              <Download />
              {t("devices.exportCsv")}
            </Button>
          )
        }
      />

      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SearchInput value={table.text} onChange={table.setText} placeholder={t("devices.searchPlaceholder")} className="sm:w-56" />
        <FilterSelect
          value={f.online === undefined ? "" : String(f.online)}
          onChange={(v) => update({ filters: { online: v } })}
          placeholder={t("devices.status")}
          options={[
            { value: "true", label: t("devices.online") },
            { value: "false", label: t("devices.offline") },
          ]}
        />
        {admin && (
          <div className="w-40">
            <AsyncCombobox
              value={ownerFilter && ownerFilter !== "none" && ownerFilter !== "me" ? Number(ownerFilter) : ownerFilter === "none" ? -1 : null}
              valueLabel={ownerFilter === "none" ? t("devices.unassigned") : userLabel(ownerNames.data?.items.find((u) => String(u.id) === ownerFilter)) }
              onChange={(id) => update({ filters: { owner: id == null ? "" : id === -1 ? "none" : String(id) } })}
              queryKey="owner-filter"
              fetchOptions={async (q) => [
                { id: -1, label: t("devices.unassigned") },
                ...(await lookupApi.users(q)).items.map((u) => ({ id: u.id, label: userLabel(u) })),
              ]}
              placeholder={t("devices.owner")}
              noneLabel={`${t("devices.owner")}: ${t("common.any")}`}
              className={ownerFilter ? "border-primary/50 bg-primary/5" : undefined}
            />
          </div>
        )}
        {admin && (
          <div className="w-40">
            <AsyncCombobox
              value={groupFilter === "none" ? -1 : groupFilter ? Number(groupFilter) : null}
              valueLabel={groupFilter === "none" ? t("pickers.noGroup") : (groupNames.data?.items.find((g) => String(g.id) === groupFilter)?.name ?? `#${groupFilter}`)}
              onChange={(id) => update({ filters: { device_group: id == null ? "" : id === -1 ? "none" : String(id) } })}
              queryKey="group-filter"
              fetchOptions={async (q) => [
                { id: -1, label: t("pickers.noGroup") },
                ...(await groupsApi.listDeviceGroups({ q, page_size: 100 })).items.map((g) => ({ id: g.id, label: g.name })),
              ]}
              placeholder={t("devices.group")}
              noneLabel={`${t("devices.group")}: ${t("common.any")}`}
              className={groupFilter ? "border-primary/50 bg-primary/5" : undefined}
            />
          </div>
        )}
        <FilterSelect value={String(f.os ?? "")} onChange={(v) => update({ filters: { os: v } })} placeholder={t("devices.os")} options={osOptions} />
        <FilterSelect
          value={String(f.version ?? "")}
          onChange={(v) => update({ filters: { version: v } })}
          placeholder={t("devices.version")}
          options={versionOptions}
        />
        <FilterSelect
          value={String(f.seen ?? "")}
          onChange={(v) => update({ filters: { seen: v } })}
          placeholder={t("devices.seenWithin")}
          options={Object.keys(SEEN_HOURS).map((k) => ({ value: k, label: t(`devices.seen.${k}`) }))}
        />
        {anyFilter && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              table.setText("");
              update({ q: "", filters: Object.fromEntries(FILTER_KEYS.map((k) => [k, ""])) });
            }}
          >
            <X />
            {t("common.reset")}
          </Button>
        )}
      </div>

      <DataTable
        id={my ? "my-devices" : "devices"}
        columns={columns}
        data={list.data?.items}
        total={list.data?.total}
        isLoading={list.isLoading}
        error={list.error}
        onRetry={() => void list.refetch()}
        page={state.page}
        pageSize={state.pageSize}
        onPageChange={(page) => update({ page })}
        onPageSizeChange={(pageSize) => update({ pageSize })}
        sort={state.sort}
        onSortChange={(sort) => update({ sort: sort ?? "-last_seen_at" })}
        getRowId={(d) => String(d.id)}
        selectable
        rowSelection={selection}
        onRowSelectionChange={setSelection}
        onRowClick={(d) => table.setSearch({ device: d.id })}
        activeRowId={openId ? String(openId) : undefined}
        empty={
          <EmptyState
            icon={Monitor}
            title={anyFilter ? t("devices.emptyFiltered") : t("devices.empty")}
            description={anyFilter ? undefined : t("devices.emptyHint")}
          />
        }
        bulkActions={() => (
          <>
            {admin && (
              <>
                <Button size="sm" variant="outline" onClick={() => setBulk("owner")}>
                  <UserPlus />
                  {t("devices.assignOwner")}
                </Button>
                <Button size="sm" variant="outline" onClick={() => setBulk("group")}>
                  <FolderInput />
                  {t("devices.assignGroup")}
                </Button>
              </>
            )}
            <Button size="sm" variant="outline" onClick={() => setBulk("book")}>
              <BookPlus />
              {t("devices.addToBook")}
            </Button>
            {admin && (
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button size="sm" variant="outline" aria-label={t("common.more")}>
                    <MoreHorizontal />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuItem onSelect={() => setConfirm("kick")}>
                    <Power />
                    {t("devices.kick")}
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem className="text-destructive focus:text-destructive" onSelect={() => setConfirm("delete")}>
                    <Trash2 />
                    {t("common.delete")}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            )}
          </>
        )}
      />

      <DeviceDrawer deviceId={openId} onClose={() => table.setSearch({ device: undefined })} isAdmin={admin} />
      {bulk && (
        <DeviceBulkDialog
          mode={bulk}
          deviceIds={selectedIds}
          open
          onOpenChange={(o) => !o && setBulk(null)}
          onDone={() => setSelection({})}
          bookScope={admin ? "all" : "accessible"}
        />
      )}
      <ConfirmDialog
        open={confirm === "delete"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t("devices.bulkDeleteTitle", { count: selectedIds.length })}
        description={t("devices.deleteDescription")}
        confirmLabel={t("common.delete")}
        destructive
        confirmText={selectedIds.length > 5 ? String(selectedIds.length) : undefined}
        pending={bulkAction.isPending}
        onConfirm={() => bulkAction.mutate("delete")}
      />
      <ConfirmDialog
        open={confirm === "kick"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t("devices.bulkKickTitle", { count: selectedIds.length })}
        description={t("devices.kickDescription")}
        confirmLabel={t("devices.kick")}
        pending={bulkAction.isPending}
        onConfirm={() => bulkAction.mutate("kick")}
      />
    </div>
  );
}

export function DevicesPage() {
  return <DevicesView my={false} />;
}

export function MyDevicesPage() {
  return <DevicesView my />;
}
