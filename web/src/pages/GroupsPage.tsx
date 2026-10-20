import { zodResolver } from "@hookform/resolvers/zod";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ColumnDef } from "@tanstack/react-table";
import { FolderTree, MoreHorizontal, Pencil, Plus, Trash2, UsersRound } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { ApiError } from "@/api/client";
import { groupKeys, groupsApi, type DeviceGroup, type UserGroup } from "@/api/users";
import { ConfirmDialog, EmptyState, Field, PageHeader, RelativeTime, useToastError } from "@/components";
import { DataTable, SearchInput, type ColumnMeta } from "@/components/data-table";
import { StrategyPicker } from "@/components/pickers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Switch, Tabs, TabsContent, TabsList, TabsTrigger, Textarea } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMe } from "@/lib/session";
import { useTableSearch } from "@/lib/use-table";

type Kind = "user" | "device";
type Group = UserGroup | DeviceGroup;

const groupSchema = z.object({
  name: z.string().trim().min(1, "validation.required").max(128, "validation.tooLong"),
  note: z.string().max(4096, "validation.tooLong"),
  visibility: z.enum(["self", "shared"]),
  can_share_links: z.boolean(),
});
type GroupValues = z.infer<typeof groupSchema>;

function GroupDialog({ kind, group, open, onOpenChange }: { kind: Kind; group: Group | null; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const shareLinks = useMe().can_share_links === true;
  const form = useForm<GroupValues>({
    resolver: zodResolver(groupSchema),
    defaultValues: { name: "", note: "", visibility: "self", can_share_links: false },
  });
  const [strategy, setStrategy] = useState<number | null>(null);
  const [strategyLabel, setStrategyLabel] = useState<string | undefined>();

  useEffect(() => {
    if (open) {
      form.reset({
        name: group?.name ?? "",
        note: group?.note ?? "",
        visibility: group && "visibility" in group ? group.visibility : "self",
        can_share_links: !!group && "can_share_links" in group && group.can_share_links,
      });
      if (group && "strategy_id" in group) {
        setStrategy(group.strategy_id ?? null);
        setStrategyLabel(group.strategy?.name);
      } else {
        setStrategy(null);
        setStrategyLabel(undefined);
      }
    }
  }, [open, group, form]);
  const save = useMutation({
    mutationFn: (v: GroupValues): Promise<Group> => {
      if (kind === "user") {
        const body = { name: v.name, note: v.note, visibility: v.visibility, ...(shareLinks ? { can_share_links: v.can_share_links } : {}) };
        return group ? groupsApi.updateUserGroup(group.id, body) : groupsApi.createUserGroup(body);
      }
      const body = { name: v.name, note: v.note, strategy_id: strategy };
      return group ? groupsApi.updateDeviceGroup(group.id, body) : groupsApi.createDeviceGroup(body);
    },
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: kind === "user" ? groupKeys.user : groupKeys.device });
      void qc.invalidateQueries({ queryKey: ["lookup"] });
      onOpenChange(false);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "conflict") form.setError("name", { message: "groups.nameTaken" });
      else toastError(e);
    },
  });
  const nameErr = form.formState.errors.name?.message;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>
              {group ? t("groups.edit") : kind === "user" ? t("groups.createUserGroup") : t("groups.createDeviceGroup")}
            </DialogTitle>
            <DialogDescription>{kind === "user" ? t("groups.userGroupsHint") : t("groups.deviceGroupsHint")}</DialogDescription>
          </DialogHeader>
          <Field label={t("groups.name")} htmlFor="g-name" error={nameErr ? t(nameErr) : undefined}>
            <Input id="g-name" autoFocus {...form.register("name")} />
          </Field>
          {kind === "user" && (
            <Field label={t("groups.visibility")} htmlFor="g-vis" hint={t(`groups.visibilityHint.${form.watch("visibility")}`)}>
              <Controller
                control={form.control}
                name="visibility"
                render={({ field }) => (
                  <Select value={field.value} onValueChange={field.onChange}>
                    <SelectTrigger id="g-vis">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="self">{t("groups.visibilityOption.self")}</SelectItem>
                      <SelectItem value="shared">{t("groups.visibilityOption.shared")}</SelectItem>
                    </SelectContent>
                  </Select>
                )}
              />
            </Field>
          )}
          {kind === "user" && shareLinks && (
            <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
              <div>
                <label htmlFor="g-share" className="text-sm font-medium">
                  {t("share.groupRight")}
                </label>
                <p className="text-xs text-muted-foreground">{t("share.groupRightHint")}</p>
              </div>
              <Controller
                control={form.control}
                name="can_share_links"
                render={({ field }) => <Switch id="g-share" checked={field.value} onCheckedChange={field.onChange} />}
              />
            </div>
          )}
          {kind === "device" && (
            <Field label={t("strategies.title")} htmlFor="g-strategy" hint={t("strategies.groupHint")}>
              <StrategyPicker
                id="g-strategy"
                value={strategy}
                valueLabel={strategyLabel}
                noneLabel={t("strategies.inheritedOrDefault")}
                onChange={(id, s) => {
                  setStrategy(id);
                  setStrategyLabel(s?.name);
                }}
              />
            </Field>
          )}
          <Field label={t("groups.note")} htmlFor="g-note">
            <Textarea id="g-note" rows={3} {...form.register("note")} />
          </Field>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {group ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function GroupTable({ kind }: { kind: Kind }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const table = useTableSearch({ sort: "name", prefix: `${kind[0]}_` });
  const { state, update } = table;
  const [editing, setEditing] = useState<Group | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [deleting, setDeleting] = useState<Group | null>(null);
  const shareLinks = useMe().can_share_links === true;

  const list = useQuery({
    queryKey: [...(kind === "user" ? groupKeys.user : groupKeys.device), table.query],
    queryFn: (): Promise<{ items: Group[]; total: number }> =>
      kind === "user" ? groupsApi.listUserGroups(table.query) : groupsApi.listDeviceGroups(table.query),
    placeholderData: keepPreviousData,
  });
  const del = useMutation({
    mutationFn: (g: Group) => (kind === "user" ? groupsApi.deleteUserGroup(g.id) : groupsApi.deleteDeviceGroup(g.id)),
    onSuccess: () => {
      toast.success(t("groups.deleted"));
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: kind === "user" ? groupKeys.user : groupKeys.device });
    },
    onError: toastError,
  });

  const columns = useMemo<ColumnDef<Group, unknown>[]>(() => {
    const cols: ColumnDef<Group, unknown>[] = [
      {
        id: "name",
        header: t("groups.name"),
        enableHiding: false,
        meta: { label: t("groups.name") } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) => (
          <div className="min-w-0">
            <div className="font-medium">{g.name}</div>
            {g.note && <div className="line-clamp-1 text-xs text-muted-foreground">{g.note}</div>}
          </div>
        ),
      },
    ];
    if (kind === "user") {
      cols.push({
        id: "visibility",
        header: t("groups.visibility"),
        enableSorting: false,
        meta: { label: t("groups.visibility") } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) =>
          "visibility" in g ? (
            <div className="flex flex-wrap gap-1">
              <Badge variant="outline">{t(`groups.visibilityOption.${g.visibility}`)}</Badge>
              {shareLinks && g.can_share_links && <Badge variant="secondary">{t("share.groupBadge")}</Badge>}
            </div>
          ) : null,
      });
    }
    if (kind === "device") {
      cols.push({
        id: "strategy",
        header: t("strategies.title"),
        enableSorting: false,
        meta: { label: t("strategies.title") } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) =>
          "strategy" in g && g.strategy ? (
            <Badge variant="outline">{g.strategy.name}</Badge>
          ) : (
            <span className="text-xs text-muted-foreground">{t("strategies.inheritedOrDefault")}</span>
          ),
      });
    }
    cols.push(
      {
        id: kind === "user" ? "member_count" : "device_count",
        header: kind === "user" ? t("groups.members") : t("groups.devices"),
        meta: { label: kind === "user" ? t("groups.members") : t("groups.devices") } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) => (
          <span className="tabular-nums">{"member_count" in g ? g.member_count : "device_count" in g ? g.device_count : 0}</span>
        ),
      },
      {
        id: "created_at",
        header: t("groups.created"),
        meta: { label: t("groups.created") } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) => <RelativeTime iso={g.created_at} className="text-muted-foreground" />,
      },
      {
        id: "_actions",
        header: () => <span className="sr-only">{t("common.actions")}</span>,
        enableSorting: false,
        enableHiding: false,
        meta: { className: "w-10" } satisfies ColumnMeta,
        cell: ({ row: { original: g } }) => (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="h-7 w-7" aria-label={t("common.actions")} onClick={(e) => e.stopPropagation()}>
                <MoreHorizontal />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => {
                  setEditing(g);
                  setDialogOpen(true);
                }}
              >
                <Pencil />
                {t("common.edit")}
              </DropdownMenuItem>
              <DropdownMenuItem className="text-destructive focus:text-destructive" onSelect={() => setDeleting(g)}>
                <Trash2 />
                {t("common.delete")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ),
      },
    );
    return cols;
  }, [kind, t, shareLinks]);

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <SearchInput value={table.text} onChange={table.setText} />
        <Button
          size="sm"
          onClick={() => {
            setEditing(null);
            setDialogOpen(true);
          }}
        >
          <Plus />
          {kind === "user" ? t("groups.createUserGroup") : t("groups.createDeviceGroup")}
        </Button>
      </div>
      <DataTable
        id={`${kind}-groups`}
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
        onSortChange={(sort) => update({ sort: sort ?? "name" })}
        getRowId={(g) => String(g.id)}
        onRowClick={(g) => {
          setEditing(g);
          setDialogOpen(true);
        }}
        empty={
          <EmptyState
            icon={kind === "user" ? UsersRound : FolderTree}
            title={t("groups.empty")}
            description={kind === "user" ? t("groups.userGroupsHint") : t("groups.deviceGroupsHint")}
          />
        }
      />
      <GroupDialog kind={kind} group={editing} open={dialogOpen} onOpenChange={setDialogOpen} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("groups.deleteTitle", { name: deleting?.name ?? "" })}
        description={kind === "user" ? t("groups.deleteUserGroupDescription") : t("groups.deleteDeviceGroupDescription")}
        confirmLabel={t("common.delete")}
        destructive
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting)}
      />
    </div>
  );
}

export function GroupsPage() {
  const { t } = useTranslation();
  const table = useTableSearch({});
  const tab = table.raw.tab === "device" ? "device" : "user";
  return (
    <div>
      <PageHeader title={t("nav.groups")} description={t("groups.description")} />
      <Tabs value={tab} onValueChange={(v) => table.setSearch({ tab: v === "user" ? undefined : v })}>
        <TabsList>
          <TabsTrigger value="user">
            <UsersRound />
            {t("groups.userGroups")}
          </TabsTrigger>
          <TabsTrigger value="device">
            <FolderTree />
            {t("groups.deviceGroups")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="user">
          <GroupTable kind="user" />
        </TabsContent>
        <TabsContent value="device">
          <GroupTable kind="device" />
        </TabsContent>
      </Tabs>
    </div>
  );
}
