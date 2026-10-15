import { keepPreviousData, useQuery } from "@tanstack/react-query";
import type { ColumnDef } from "@tanstack/react-table";
import { Plus, ShieldCheck, Users, X } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import { userKeys, usersApi, type UserListItem, type UserListQuery } from "@/api/users";
import { EmptyState, PageHeader, RelativeTime } from "@/components";
import { DataTable, FilterSelect, SearchInput, type ColumnMeta } from "@/components/data-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useTableSearch } from "@/lib/use-table";
import { CreateUserDialog } from "@/pages/users/UserForm";
import { UserDrawer } from "@/pages/users/UserDrawer";

const FILTER_KEYS = ["role", "status"] as const;

export function UsersPage() {
  const { t } = useTranslation();
  const table = useTableSearch({ sort: "username", filterKeys: FILTER_KEYS });
  const { state, update } = table;
  const query = table.query as UserListQuery;
  const [createOpen, setCreateOpen] = useState(false);
  const openId = Number(table.raw.user) || undefined;

  const list = useQuery({ queryKey: userKeys.list(query), queryFn: () => usersApi.list(query), placeholderData: keepPreviousData });

  const columns = useMemo<ColumnDef<UserListItem, unknown>[]>(
    () => [
      {
        id: "username",
        header: t("users.user"),
        enableHiding: false,
        meta: { label: t("users.user"), className: "min-w-[12rem]" } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => (
          <div className="flex items-center gap-2.5">
            <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold uppercase">
              {(u.display_name || u.username).slice(0, 2)}
            </span>
            <div className="min-w-0">
              <div className="truncate font-medium">{u.display_name || u.username}</div>
              <div className="truncate text-xs text-muted-foreground">
                {u.username}
                {u.email && ` · ${u.email}`}
              </div>
            </div>
          </div>
        ),
      },
      {
        id: "role",
        header: t("users.role"),
        meta: { label: t("users.role") } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => (
          <div className="flex items-center gap-1.5">
            <Badge variant={u.role === "admin" ? "default" : "secondary"}>{t(`roles.${u.role}`)}</Badge>
            {u.is_last_admin && (
              <span className="text-[11px] text-muted-foreground" title={t("users.lastAdminHint")}>
                {t("users.lastAdmin")}
              </span>
            )}
          </div>
        ),
      },
      {
        id: "status",
        header: t("users.status"),
        enableSorting: false,
        meta: { label: t("users.status") } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) =>
          u.status === "active" ? (
            <span className="text-emerald-600 dark:text-emerald-400">{t("users.active")}</span>
          ) : (
            <Badge variant="destructive">{t("users.disabled")}</Badge>
          ),
      },
      {
        id: "group",
        header: t("users.group"),
        enableSorting: false,
        meta: { label: t("users.group") } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => u.user_group?.name ?? <span className="text-muted-foreground">—</span>,
      },
      {
        id: "totp",
        header: t("users.twoFactor"),
        enableSorting: false,
        meta: { label: t("users.twoFactor") } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) =>
          u.totp_enabled ? (
            <ShieldCheck className="h-4 w-4 text-emerald-600" aria-label={t("users.totpOn")} />
          ) : u.totp_required ? (
            <Badge variant="outline">{t("users.totpPending")}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        id: "devices",
        header: t("users.devices"),
        enableSorting: false,
        meta: { label: t("users.devices"), className: "text-right" } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => <span className="tabular-nums">{u.device_count}</span>,
      },
      {
        id: "last_login_at",
        header: t("users.lastLogin"),
        meta: { label: t("users.lastLogin") } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => <RelativeTime iso={u.last_login_at} className="text-muted-foreground" />,
      },
      {
        id: "created_at",
        header: t("users.created"),
        meta: { label: t("users.created"), defaultHidden: true } satisfies ColumnMeta,
        cell: ({ row: { original: u } }) => <RelativeTime iso={u.created_at} className="text-muted-foreground" />,
      },
    ],
    [t],
  );

  const f = state.filters;
  const anyFilter = Object.keys(f).length > 0 || state.q !== "";

  return (
    <div>
      <PageHeader
        title={t("nav.users")}
        description={t("users.description")}
        actions={
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <Plus />
            {t("users.create")}
          </Button>
        }
      />
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <SearchInput value={table.text} onChange={table.setText} placeholder={t("users.searchPlaceholder")} />
        <FilterSelect
          value={String(f.role ?? "")}
          onChange={(v) => update({ filters: { role: v } })}
          placeholder={t("users.role")}
          options={[
            { value: "admin", label: t("roles.admin") },
            { value: "user", label: t("roles.user") },
          ]}
        />
        <FilterSelect
          value={String(f.status ?? "")}
          onChange={(v) => update({ filters: { status: v } })}
          placeholder={t("users.status")}
          options={[
            { value: "active", label: t("users.active") },
            { value: "disabled", label: t("users.disabled") },
          ]}
        />
        {anyFilter && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              table.setText("");
              update({ q: "", filters: { role: "", status: "" } });
            }}
          >
            <X />
            {t("common.reset")}
          </Button>
        )}
      </div>
      <DataTable
        id="users"
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
        onSortChange={(sort) => update({ sort: sort ?? "username" })}
        getRowId={(u) => String(u.id)}
        onRowClick={(u) => table.setSearch({ user: u.id })}
        activeRowId={openId ? String(openId) : undefined}
        empty={<EmptyState icon={Users} title={anyFilter ? t("users.emptyFiltered") : t("users.empty")} />}
      />
      <UserDrawer userId={openId} onClose={() => table.setSearch({ user: undefined })} />
      <CreateUserDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={(u) => table.setSearch({ user: u.id })} />
    </div>
  );
}
