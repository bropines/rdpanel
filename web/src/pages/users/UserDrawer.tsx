import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { KeyRound, LogOut, Save, ShieldOff, Trash2, Unlink, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { addressBooksApi } from "@/api/addressBooks";
import { devicesApi } from "@/api/devices";
import { userKeys, usersApi, type UserDetail } from "@/api/users";
import { ConfirmDialog, EmptyState, ErrorState, Field, KeyValue, Mono, OnlineDot, RelativeTime, SectionTitle, useToastError } from "@/components";
import { ApiTokenList } from "@/components/ApiTokenList";
import { PasskeyList } from "@/components/PasskeyList";
import { SessionsList } from "@/components/SessionsList";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Checkbox, Skeleton, Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { useMe } from "@/lib/session";
import { deviceName } from "@/pages/devices/DeviceDrawer";
import { applyServerErrors, UserFields, userProfileSchema, type UserProfileValues } from "@/pages/users/UserForm";

function ProfileTab({ user }: { user: UserDetail }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const values = (u: UserDetail): UserProfileValues => ({
    username: u.username,
    display_name: u.display_name ?? "",
    email: u.email ?? "",
    role: u.role,
    status: u.status,
    user_group_id: u.user_group_id ?? null,
    note: u.note ?? "",
    totp_required: !!u.totp_required,
    email_verified: !!u.email_verified,
  });
  const form = useForm<UserProfileValues>({ resolver: zodResolver(userProfileSchema), defaultValues: values(user) });
  useEffect(() => form.reset(values(user)), [user, form]);

  const save = useMutation({
    mutationFn: (v: UserProfileValues) =>
      usersApi.update(user.id, {
        ...v,
        email: v.email || null,
        // Sent only when toggled: a changed address is unverified unless re-confirmed.
        email_verified: v.email_verified !== !!user.email_verified && !!v.email ? v.email_verified : undefined,
      }),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: userKeys.all });
    },
    onError: (e) => {
      if (!applyServerErrors(form, e)) toastError(e);
    },
  });
  return (
    <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-4">
      {user.is_last_admin && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300">
          {t("users.lastAdminBanner")}
        </div>
      )}
      <UserFields form={form} lastAdmin={user.is_last_admin} groupLabel={user.user_group?.name} />
      <div className="flex justify-end">
        <Button type="submit" size="sm" disabled={!form.formState.isDirty || save.isPending}>
          <Save />
          {t("common.save")}
        </Button>
      </div>
    </form>
  );
}

const resetSchema = z.object({
  password: z.string().min(8, "validation.passwordMin"),
  revoke: z.boolean(),
});

function ResetPasswordDialog({ userId, open, onOpenChange }: { userId: number; open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const toastError = useToastError();
  const form = useForm<z.infer<typeof resetSchema>>({ resolver: zodResolver(resetSchema), defaultValues: { password: "", revoke: true } });
  const reset = useMutation({
    mutationFn: (v: z.infer<typeof resetSchema>) => usersApi.resetPassword(userId, { password: v.password, revoke_sessions: v.revoke }),
    onSuccess: () => {
      toast.success(t("users.passwordReset"));
      form.reset();
      onOpenChange(false);
    },
    onError: toastError,
  });
  const err = form.formState.errors.password?.message;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form onSubmit={form.handleSubmit((v) => reset.mutate(v))} noValidate className="space-y-4">
          <DialogHeader>
            <DialogTitle>{t("users.resetPassword")}</DialogTitle>
            <DialogDescription>{t("users.resetPasswordDescription")}</DialogDescription>
          </DialogHeader>
          <Field label={t("users.newPassword")} htmlFor="rp-pass" error={err ? t(err) : undefined}>
            <Input id="rp-pass" type="password" autoComplete="new-password" {...form.register("password")} />
          </Field>
          <label className="flex items-center gap-2 text-sm">
            <Checkbox checked={form.watch("revoke")} onCheckedChange={(v) => form.setValue("revoke", !!v)} />
            {t("users.revokeOnReset")}
          </label>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={reset.isPending}>
              {t("users.resetPassword")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SecurityTab({ user }: { user: UserDetail }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [pwOpen, setPwOpen] = useState(false);
  const [confirm, setConfirm] = useState<"totp" | "revoke" | null>(null);
  const [unlinkId, setUnlinkId] = useState<number | null>(null);
  const [revokePasskey, setRevokePasskey] = useState<{ id: number; name: string } | null>(null);
  const [revokeToken, setRevokeToken] = useState<{ id: number; name: string } | null>(null);
  const apiTokens = useQuery({ queryKey: userKeys.apiTokens(user.id), queryFn: () => usersApi.apiTokens(user.id) });
  const revokeApiToken = useMutation({
    mutationFn: (tid: number) => usersApi.revokeApiToken(user.id, tid),
    onSuccess: () => {
      toast.success(t("apiTokens.revoked"));
      setRevokeToken(null);
      void qc.invalidateQueries({ queryKey: userKeys.apiTokens(user.id) });
    },
    onError: toastError,
  });
  const sessions = useQuery({ queryKey: userKeys.sessions(user.id), queryFn: () => usersApi.sessions(user.id) });
  const passkeys = useQuery({ queryKey: userKeys.passkeys(user.id), queryFn: () => usersApi.passkeys(user.id) });
  const revokeKey = useMutation({
    mutationFn: (pid: number) => usersApi.revokePasskey(user.id, pid),
    onSuccess: () => {
      toast.success(t("passkeys.revoked"));
      setRevokePasskey(null);
      void qc.invalidateQueries({ queryKey: userKeys.passkeys(user.id) });
    },
    onError: toastError,
  });

  const resetTotp = useMutation({
    mutationFn: () => usersApi.resetTotp(user.id),
    onSuccess: () => {
      toast.success(t("users.totpReset"));
      setConfirm(null);
      void qc.invalidateQueries({ queryKey: userKeys.all });
    },
    onError: toastError,
  });
  const revokeAll = useMutation({
    mutationFn: () => usersApi.revokeAll(user.id, "all"),
    onSuccess: (r) => {
      toast.success(t("sessions.revokedCount", { count: r.revoked }));
      setConfirm(null);
      void qc.invalidateQueries({ queryKey: userKeys.sessions(user.id) });
    },
    onError: toastError,
  });
  const revokeOne = useMutation({
    mutationFn: (sid: number) => usersApi.revokeSession(user.id, sid),
    onSuccess: () => void qc.invalidateQueries({ queryKey: userKeys.sessions(user.id) }),
    onError: toastError,
  });
  const unlink = useMutation({
    mutationFn: (id: number) => usersApi.unlinkIdentity(user.id, id),
    onSuccess: () => {
      toast.success(t("identities.unlinked"));
      setUnlinkId(null);
      void qc.invalidateQueries({ queryKey: userKeys.detail(user.id) });
    },
    onError: toastError,
  });

  return (
    <div className="space-y-6">
      <section>
        <SectionTitle>{t("users.credentials")}</SectionTitle>
        <KeyValue
          items={[
            [t("users.password"), user.has_password ? t("users.passwordSet") : t("users.noPassword")],
            [
              t("users.twoFactor"),
              user.totp_enabled ? (
                <Badge variant="success">{t("users.totpOn")}</Badge>
              ) : user.totp_required ? (
                <Badge variant="outline">{t("users.totpPending")}</Badge>
              ) : (
                <span className="text-muted-foreground">{t("users.totpOff")}</span>
              ),
            ],
            [
              t("users.email"),
              !user.email ? (
                <span className="text-muted-foreground">—</span>
              ) : (
                <span className="flex flex-wrap items-center gap-1">
                  {user.email_verified ? <Badge variant="success">{t("email.verifiedBadge")}</Badge> : <Badge variant="outline">{t("email.unverifiedBadge")}</Badge>}
                  {user.email_2fa_enabled && <Badge variant="secondary">{t("users.email2fa")}</Badge>}
                </span>
              ),
            ],
          ]}
        />
        <div className="mt-3 flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => setPwOpen(true)}>
            <KeyRound />
            {t("users.resetPassword")}
          </Button>
          <Button size="sm" variant="outline" disabled={!user.totp_enabled} onClick={() => setConfirm("totp")}>
            <ShieldOff />
            {t("users.reset2fa")}
          </Button>
        </div>
      </section>

      <section>
        <SectionTitle>{t("passkeys.title")}</SectionTitle>
        <PasskeyList
          passkeys={passkeys.data?.items}
          isLoading={passkeys.isLoading}
          actions={(p) => (
            <Button size="icon" variant="ghost" className="h-7 w-7" onClick={() => setRevokePasskey(p)} aria-label={t("passkeys.revoke")}>
              <X />
            </Button>
          )}
        />
      </section>

      <section>
        <SectionTitle>{t("apiTokens.title")}</SectionTitle>
        <ApiTokenList
          tokens={apiTokens.data?.items}
          isLoading={apiTokens.isLoading}
          actions={(tok) => (
            <Button size="icon" variant="ghost" className="h-7 w-7" onClick={() => setRevokeToken(tok)} aria-label={t("apiTokens.revoke")}>
              <X />
            </Button>
          )}
        />
      </section>

      <section>
        <SectionTitle>{t("identities.title")}</SectionTitle>
        {user.identities.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("identities.none")}</p>
        ) : (
          <ul className="divide-y rounded-md border text-sm">
            {user.identities.map((i) => (
              <li key={i.id} className="flex items-center gap-3 px-3 py-2">
                <Badge variant="secondary">{i.provider_name}</Badge>
                <span className="min-w-0 flex-1 truncate">
                  {i.email ?? i.username ?? <Mono>{i.subject}</Mono>}
                  {i.email && !i.email_verified && <span className="ml-1 text-xs text-amber-600">({t("identities.unverified")})</span>}
                </span>
                <span className="hidden text-xs text-muted-foreground sm:inline">
                  <RelativeTime iso={i.last_login_at ?? i.created_at} />
                </span>
                <Button size="icon" variant="ghost" className="h-7 w-7" onClick={() => setUnlinkId(i.id)} aria-label={t("identities.unlink")}>
                  <Unlink />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section>
        <SectionTitle
          actions={
            <Button size="sm" variant="ghost" onClick={() => setConfirm("revoke")} disabled={!sessions.data?.items.length}>
              <LogOut />
              {t("sessions.revokeAll")}
            </Button>
          }
        >
          {t("sessions.title")}
        </SectionTitle>
        <SessionsList
          sessions={sessions.data?.items}
          isLoading={sessions.isLoading}
          onRevoke={(s) => revokeOne.mutate(s.id)}
          revoking={revokeOne.isPending ? (revokeOne.variables ?? null) : null}
        />
      </section>

      <ResetPasswordDialog userId={user.id} open={pwOpen} onOpenChange={setPwOpen} />
      <ConfirmDialog
        open={confirm === "totp"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t("users.reset2faTitle", { name: user.username })}
        description={t("users.reset2faDescription")}
        confirmLabel={t("users.reset2fa")}
        destructive
        pending={resetTotp.isPending}
        onConfirm={() => resetTotp.mutate()}
      />
      <ConfirmDialog
        open={confirm === "revoke"}
        onOpenChange={(o) => !o && setConfirm(null)}
        title={t("sessions.revokeAllTitle", { name: user.username })}
        description={t("sessions.revokeAllDescription")}
        confirmLabel={t("sessions.revokeAll")}
        destructive
        pending={revokeAll.isPending}
        onConfirm={() => revokeAll.mutate()}
      />
      <ConfirmDialog
        open={revokePasskey !== null}
        onOpenChange={(o) => !o && setRevokePasskey(null)}
        title={t("passkeys.revokeTitle", { name: revokePasskey?.name ?? "", user: user.username })}
        description={t("passkeys.revokeDescription")}
        confirmLabel={t("passkeys.revoke")}
        destructive
        pending={revokeKey.isPending}
        onConfirm={() => revokePasskey && revokeKey.mutate(revokePasskey.id)}
      />
      <ConfirmDialog
        open={revokeToken !== null}
        onOpenChange={(o) => !o && setRevokeToken(null)}
        title={t("apiTokens.revokeUserTitle", { name: revokeToken?.name ?? "", user: user.username })}
        description={t("apiTokens.revokeDescription")}
        confirmLabel={t("apiTokens.revoke")}
        destructive
        pending={revokeApiToken.isPending}
        onConfirm={() => revokeToken && revokeApiToken.mutate(revokeToken.id)}
      />
      <ConfirmDialog
        open={unlinkId !== null}
        onOpenChange={(o) => !o && setUnlinkId(null)}
        title={t("identities.unlinkTitle")}
        description={t("identities.unlinkDescription")}
        confirmLabel={t("identities.unlink")}
        destructive
        pending={unlink.isPending}
        onConfirm={() => unlinkId !== null && unlink.mutate(unlinkId)}
      />
    </div>
  );
}

function DevicesTab({ userId }: { userId: number }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["devices", "list", { owner: String(userId), page_size: 100 }],
    queryFn: () => devicesApi.list({ owner: String(userId), page_size: 100, sort: "-last_seen_at" }),
  });
  if (q.isLoading) return <Skeleton className="h-24" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data?.items.length) return <EmptyState title={t("users.noDevices")} className="py-6" />;
  return (
    <ul className="divide-y rounded-md border text-sm">
      {q.data.items.map((d) => (
        <li key={d.id}>
          <Link to="/devices" search={{ device: d.id }} className="flex items-center gap-3 px-3 py-2 hover:bg-muted/50">
            <OnlineDot online={d.online} />
            <span className="min-w-0 flex-1 truncate font-medium">{deviceName(d)}</span>
            <Mono className="text-muted-foreground">{d.rustdesk_id}</Mono>
            <span className="hidden text-xs text-muted-foreground sm:inline">
              <RelativeTime iso={d.last_seen_at} />
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function BooksTab({ userId }: { userId: number }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["address-books", "list", { owner_user_id: userId, scope: "all" }],
    queryFn: () => addressBooksApi.list({ owner_user_id: userId, scope: "all", page_size: 100 }),
  });
  if (q.isLoading) return <Skeleton className="h-24" />;
  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data?.items.length) return <EmptyState title={t("users.noBooks")} className="py-6" />;
  return (
    <ul className="divide-y rounded-md border text-sm">
      {q.data.items.map((b) => (
        <li key={b.id}>
          <Link to="/address-books" search={{ book: b.id }} className="flex items-center gap-3 px-3 py-2 hover:bg-muted/50">
            <span className="min-w-0 flex-1 truncate font-medium">{b.name}</span>
            <Badge variant={b.is_personal ? "secondary" : "outline"}>{b.is_personal ? t("books.personal") : t("books.shared")}</Badge>
            <span className="w-16 text-right text-xs text-muted-foreground">{t("books.entriesCount", { count: b.entry_count })}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function UserDrawer({ userId, onClose }: { userId: number | undefined; onClose: () => void }) {
  const { t } = useTranslation();
  const me = useMe();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const open = userId !== undefined;
  const q = useQuery({ queryKey: userKeys.detail(userId ?? 0), queryFn: () => usersApi.get(userId!), enabled: open });
  const u = q.data;
  const del = useMutation({
    mutationFn: () => usersApi.remove(userId!),
    onSuccess: () => {
      toast.success(t("users.deleted"));
      setConfirmDelete(false);
      void qc.invalidateQueries({ queryKey: userKeys.all });
      onClose();
    },
    onError: toastError,
  });
  const isSelf = u?.id === me.user.id;

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="sm:max-w-2xl" closeLabel={t("common.close")} aria-describedby={undefined}>
        <SheetHeader>
          {u ? (
            <>
              <SheetTitle className="flex flex-wrap items-center gap-2">
                {u.display_name || u.username}
                <Badge variant={u.role === "admin" ? "default" : "secondary"}>{t(`roles.${u.role}`)}</Badge>
                {u.is_ldap && (
                  <Badge variant="outline" className="border-blue-500/30 text-blue-600 dark:text-blue-400">
                    LDAP
                  </Badge>
                )}
                {u.status === "disabled" && <Badge variant="destructive">{t("users.disabled")}</Badge>}
              </SheetTitle>
              <SheetDescription>
                {u.username}
                {u.email && ` · ${u.email}`} · {t("users.lastLogin")} <RelativeTime iso={u.last_login_at} />
              </SheetDescription>
            </>
          ) : (
            <>
              <SheetTitle>{t("users.user")}</SheetTitle>
              <Skeleton className="h-4 w-48" />
            </>
          )}
        </SheetHeader>
        <SheetBody>
          {q.isError ? (
            <ErrorState error={q.error} onRetry={() => void q.refetch()} />
          ) : !u ? (
            <Skeleton className="h-64" />
          ) : (
            <Tabs defaultValue="profile">
              <TabsList>
                <TabsTrigger value="profile">{t("users.tabProfile")}</TabsTrigger>
                <TabsTrigger value="security">{t("users.tabSecurity")}</TabsTrigger>
                <TabsTrigger value="devices">
                  {t("users.tabDevices")} <span className="text-xs text-muted-foreground">{u.device_count}</span>
                </TabsTrigger>
                <TabsTrigger value="books">
                  {t("users.tabBooks")} <span className="text-xs text-muted-foreground">{u.address_book_count}</span>
                </TabsTrigger>
              </TabsList>
              <TabsContent value="profile">
                <ProfileTab user={u} />
                <div className="mt-8 rounded-md border border-destructive/30 p-3">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <div className="text-sm font-medium">{t("users.deleteUser")}</div>
                      <div className="text-xs text-muted-foreground">
                        {u.is_last_admin ? t("users.lastAdminNoDelete") : isSelf ? t("users.selfNoDelete") : t("users.deleteHint")}
                      </div>
                    </div>
                    <Button variant="destructive" size="sm" disabled={u.is_last_admin || isSelf} onClick={() => setConfirmDelete(true)}>
                      <Trash2 />
                      {t("common.delete")}
                    </Button>
                  </div>
                </div>
              </TabsContent>
              <TabsContent value="security">
                <SecurityTab user={u} />
              </TabsContent>
              <TabsContent value="devices">
                <DevicesTab userId={u.id} />
              </TabsContent>
              <TabsContent value="books">
                <BooksTab userId={u.id} />
              </TabsContent>
            </Tabs>
          )}
        </SheetBody>
      </SheetContent>
      {u && (
        <ConfirmDialog
          open={confirmDelete}
          onOpenChange={setConfirmDelete}
          title={t("users.deleteTitle", { name: u.username })}
          description={t("users.deleteDescription")}
          confirmLabel={t("common.delete")}
          destructive
          confirmText={u.username}
          pending={del.isPending}
          onConfirm={() => del.mutate()}
        />
      )}
    </Sheet>
  );
}
