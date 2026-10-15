import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Apple, BookPlus, Laptop, Monitor, Plug, Power, Save, Smartphone, Terminal, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import type { Schemas } from "@/api/client";
import { deviceKeys, devicesApi, type DeviceDetail } from "@/api/devices";
import {
  ConfirmDialog,
  CopyButton,
  ErrorState,
  Field,
  KeyValue,
  Mono,
  OnlineDot,
  RelativeTime,
  SectionTitle,
  useToastError,
} from "@/components";
import { DeviceGroupPicker, UserPicker } from "@/components/pickers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton, Textarea } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { formatDuration, formatRustdeskId, userLabel } from "@/lib/format";
import { DeviceBulkDialog } from "@/pages/devices/dialogs";

export function OsIcon({ family, className }: { family: Schemas["OsFamily"] | undefined; className?: string }) {
  const Icon =
    family === "windows" ? Monitor : family === "macos" || family === "ios" ? Apple : family === "linux" ? Terminal : family === "android" ? Smartphone : Laptop;
  return <Icon className={className ?? "h-3.5 w-3.5 text-muted-foreground"} aria-hidden />;
}

export function deviceName(d: Pick<Schemas["Device"], "alias" | "hostname" | "rustdesk_id">): string {
  return d.alias || d.hostname || d.rustdesk_id;
}

export function ConnTypeBadge({ type }: { type: Schemas["ConnType"] }) {
  const { t } = useTranslation();
  return (
    <Badge variant="outline" className="font-normal">
      {t(`audit.connType.${type}`)}
    </Badge>
  );
}

function EditForm({ device, isAdmin }: { device: DeviceDetail; isAdmin: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [alias, setAlias] = useState(device.alias ?? "");
  const [note, setNote] = useState(device.note ?? "");
  const [owner, setOwner] = useState<number | null>(device.owner_user_id ?? null);
  const [ownerLabel, setOwnerLabel] = useState<string | undefined>(device.owner ? userLabel(device.owner) : undefined);
  const [group, setGroup] = useState<number | null>(device.device_group_id ?? null);
  const [groupLabel, setGroupLabel] = useState<string | undefined>(device.device_group?.name);

  useEffect(() => {
    setAlias(device.alias ?? "");
    setNote(device.note ?? "");
    setOwner(device.owner_user_id ?? null);
    setOwnerLabel(device.owner ? userLabel(device.owner) : undefined);
    setGroup(device.device_group_id ?? null);
    setGroupLabel(device.device_group?.name);
  }, [device.id, device.alias, device.note, device.owner_user_id, device.device_group_id, device.owner, device.device_group]);

  const dirty =
    alias !== (device.alias ?? "") ||
    note !== (device.note ?? "") ||
    owner !== (device.owner_user_id ?? null) ||
    group !== (device.device_group_id ?? null);

  const save = useMutation({
    mutationFn: () =>
      devicesApi.update(device.id, {
        alias,
        note,
        ...(isAdmin ? { owner_user_id: owner, device_group_id: group } : {}),
      }),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: deviceKeys.all });
    },
    onError: toastError,
  });

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("devices.alias")} htmlFor="dev-alias">
          <Input id="dev-alias" value={alias} maxLength={128} onChange={(e) => setAlias(e.target.value)} />
        </Field>
        {isAdmin && (
          <Field label={t("devices.owner")} htmlFor="dev-owner">
            <UserPicker
              id="dev-owner"
              value={owner}
              valueLabel={ownerLabel}
              allowNone
              onChange={(id, u) => {
                setOwner(id);
                setOwnerLabel(u?.display_name);
              }}
            />
          </Field>
        )}
        {isAdmin && (
          <Field label={t("devices.group")} htmlFor="dev-group">
            <DeviceGroupPicker
              id="dev-group"
              value={group}
              valueLabel={groupLabel}
              onChange={(id, g) => {
                setGroup(id);
                setGroupLabel(g?.name);
              }}
            />
          </Field>
        )}
      </div>
      <Field label={t("devices.note")} htmlFor="dev-note">
        <Textarea id="dev-note" value={note} maxLength={4096} rows={3} onChange={(e) => setNote(e.target.value)} />
      </Field>
      <div className="flex justify-end">
        <Button type="submit" size="sm" disabled={!dirty || save.isPending}>
          <Save />
          {t("common.save")}
        </Button>
      </div>
    </form>
  );
}

export function DeviceDrawer({
  deviceId,
  onClose,
  isAdmin,
}: {
  deviceId: number | undefined;
  onClose: () => void;
  isAdmin: boolean;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const open = deviceId !== undefined;
  const q = useQuery({
    queryKey: deviceKeys.detail(deviceId ?? 0),
    queryFn: () => devicesApi.get(deviceId!),
    enabled: open,
  });
  const [confirm, setConfirm] = useState<"delete" | "kick" | null>(null);
  const [bookOpen, setBookOpen] = useState(false);
  const d = q.data;

  const del = useMutation({
    mutationFn: () => devicesApi.remove(deviceId!),
    onSuccess: () => {
      toast.success(t("devices.deleted"));
      setConfirm(null);
      onClose();
      qc.removeQueries({ queryKey: deviceKeys.detail(deviceId!) });
      void qc.invalidateQueries({ queryKey: ["devices", "list"] });
      void qc.invalidateQueries({ queryKey: deviceKeys.facets });
    },
    onError: toastError,
  });
  const kick = useMutation({
    mutationFn: () => devicesApi.kick(deviceId!),
    onSuccess: () => {
      toast.success(t("devices.kickQueued"));
      setConfirm(null);
    },
    onError: toastError,
  });

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent closeLabel={t("common.close")} aria-describedby={undefined}>
        <SheetHeader>
          {d ? (
            <>
              <SheetTitle className="flex items-center gap-2">
                <OnlineDot online={d.online} />
                <span className="truncate">{deviceName(d)}</span>
              </SheetTitle>
              <SheetDescription className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <span className="inline-flex items-center gap-1">
                  <Mono>{formatRustdeskId(d.rustdesk_id)}</Mono>
                  <CopyButton value={d.rustdesk_id} label={t("devices.copyId")} />
                </span>
                <span>
                  {d.online ? t("devices.onlineSince") : t("devices.lastSeen")} <RelativeTime iso={d.online ? d.online_since : d.last_seen_at} />
                </span>
              </SheetDescription>
            </>
          ) : (
            <>
              <SheetTitle>{t("devices.device")}</SheetTitle>
              <Skeleton className="h-4 w-40" />
            </>
          )}
        </SheetHeader>
        <SheetBody className="space-y-6">
          {q.isError ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : !d ? (
            <div className="space-y-3">
              <Skeleton className="h-24" />
              <Skeleton className="h-40" />
            </div>
          ) : (
            <>
              <section>
                <SectionTitle>{t("devices.settings")}</SectionTitle>
                <EditForm device={d} isAdmin={isAdmin} />
              </section>
              <section>
                <SectionTitle>{t("devices.sysinfo")}</SectionTitle>
                <KeyValue
                  items={[
                    [t("devices.hostname"), d.hostname || "—"],
                    [
                      t("devices.os"),
                      <span key="os" className="inline-flex items-center gap-1.5">
                        <OsIcon family={d.os_family} />
                        {d.os || "—"}
                      </span>,
                    ],
                    [t("devices.version"), d.client_version || "—"],
                    [t("devices.osUser"), d.os_username || "—"],
                    [t("devices.cpu"), d.cpu || "—"],
                    [t("devices.memory"), d.memory || "—"],
                    [t("devices.ip"), d.last_seen_ip ? <Mono key="ip">{d.last_seen_ip}</Mono> : "—"],
                    [t("devices.owner"), userLabel(d.owner)],
                    [t("devices.group"), d.device_group?.name ?? "—"],
                    [t("devices.uuid"), d.uuid ? <Mono key="uuid" className="break-all text-xs">{d.uuid}</Mono> : "—"],
                    [t("devices.lastHeartbeat"), <RelativeTime key="hb" iso={d.last_heartbeat_at} />],
                    [t("devices.sysinfoAt"), <RelativeTime key="si" iso={d.sysinfo_at} />],
                    [t("devices.created"), <RelativeTime key="cr" iso={d.created_at} />],
                  ]}
                />
              </section>
              <section>
                <SectionTitle>{t("devices.recentConnections")}</SectionTitle>
                {d.recent_connections.length === 0 ? (
                  <p className="text-sm text-muted-foreground">{t("devices.noConnections")}</p>
                ) : (
                  <ul className="divide-y rounded-md border text-sm">
                    {d.recent_connections.map((c) => (
                      <li key={c.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                        <Plug className="h-3.5 w-3.5 text-muted-foreground" />
                        <span className="font-medium">{c.controller_name || c.controller_rustdesk_id || "—"}</span>
                        {c.controller_ip && <Mono className="text-muted-foreground">{c.controller_ip}</Mono>}
                        <ConnTypeBadge type={c.conn_type} />
                        <span className="ml-auto text-xs text-muted-foreground">
                          <RelativeTime iso={c.started_at} /> · {c.ended_at ? formatDuration(c.started_at, c.ended_at) : t("audit.active")}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </section>
              {d.address_books.length > 0 && (
                <section>
                  <SectionTitle>{t("devices.inBooks")}</SectionTitle>
                  <div className="flex flex-wrap gap-1.5">
                    {d.address_books.map((b) => (
                      <Badge key={b.id} variant="secondary">
                        {b.name}
                      </Badge>
                    ))}
                  </div>
                </section>
              )}
            </>
          )}
        </SheetBody>
        {d && (
          <SheetFooter className="justify-between">
            <Button variant="outline" size="sm" onClick={() => setBookOpen(true)}>
              <BookPlus />
              {t("devices.addToBook")}
            </Button>
            {isAdmin && (
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => setConfirm("kick")} disabled={!d.online}>
                  <Power />
                  {t("devices.kick")}
                </Button>
                <Button variant="destructive" size="sm" onClick={() => setConfirm("delete")}>
                  <Trash2 />
                  {t("common.delete")}
                </Button>
              </div>
            )}
          </SheetFooter>
        )}
      </SheetContent>
      {d && (
        <>
          <ConfirmDialog
            open={confirm === "delete"}
            onOpenChange={(o) => !o && setConfirm(null)}
            title={t("devices.deleteTitle", { name: deviceName(d) })}
            description={t("devices.deleteDescription")}
            confirmLabel={t("common.delete")}
            destructive
            pending={del.isPending}
            onConfirm={() => del.mutate()}
          />
          <ConfirmDialog
            open={confirm === "kick"}
            onOpenChange={(o) => !o && setConfirm(null)}
            title={t("devices.kickTitle", { name: deviceName(d) })}
            description={t("devices.kickDescription")}
            confirmLabel={t("devices.kick")}
            pending={kick.isPending}
            onConfirm={() => kick.mutate()}
          />
          <DeviceBulkDialog
            mode="book"
            deviceIds={[d.id]}
            open={bookOpen}
            onOpenChange={setBookOpen}
            bookScope={isAdmin ? "all" : "accessible"}
          />
        </>
      )}
    </Sheet>
  );
}
