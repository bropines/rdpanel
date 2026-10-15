import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link2, Save, Unlink } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { auth, meQueryKey, navigateExternal } from "@/api/auth";
import { ApiError } from "@/api/client";
import { security } from "@/api/security";
import { ConfirmDialog, Field, Mono, PageHeader, RelativeTime, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/misc";
import { useMe } from "@/lib/session";
import { useTableSearch } from "@/lib/use-table";
import { oidcErrorKey } from "@/pages/login-errors";
import { TotpSection } from "@/pages/my/TotpSection";

function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

const profileSchema = z.object({
  display_name: z.string().trim().max(128, "validation.tooLong"),
  email: z.union([z.literal(""), z.string().trim().email("validation.email")]),
});

function ProfileForm() {
  const { t } = useTranslation();
  const me = useMe();
  const qc = useQueryClient();
  const toastError = useToastError();
  const form = useForm<z.infer<typeof profileSchema>>({
    resolver: zodResolver(profileSchema),
    defaultValues: { display_name: me.user.display_name ?? "", email: me.user.email ?? "" },
  });
  useEffect(() => form.reset({ display_name: me.user.display_name ?? "", email: me.user.email ?? "" }), [me.user.display_name, me.user.email, form]);
  const save = useMutation({
    mutationFn: (v: z.infer<typeof profileSchema>) => security.updateProfile({ display_name: v.display_name, email: v.email || null }),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: meQueryKey });
    },
    onError: toastError,
  });
  const e = form.formState.errors;
  return (
    <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-3">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("users.username")}>
          <Input value={me.user.username} disabled />
        </Field>
        <Field label={t("users.role")}>
          <Input value={t(`roles.${me.user.role}`)} disabled />
        </Field>
        <Field label={t("users.displayName")} htmlFor="p-name" error={e.display_name?.message && t(e.display_name.message)}>
          <Input id="p-name" {...form.register("display_name")} />
        </Field>
        <Field label={t("users.email")} htmlFor="p-email" error={e.email?.message && t(e.email.message)}>
          <Input id="p-email" type="email" {...form.register("email")} />
        </Field>
      </div>
      <div className="flex justify-end">
        <Button type="submit" size="sm" disabled={!form.formState.isDirty || save.isPending}>
          <Save />
          {t("common.save")}
        </Button>
      </div>
    </form>
  );
}

export const passwordChangeSchema = z
  .object({
    current_password: z.string().min(1, "validation.required"),
    new_password: z.string().min(8, "validation.passwordMin").max(1024, "validation.tooLong"),
    confirm: z.string(),
  })
  .refine((v) => v.new_password === v.confirm, { path: ["confirm"], message: "security.passwordMismatch" })
  .refine((v) => v.new_password !== v.current_password, { path: ["new_password"], message: "security.passwordSame" });

function PasswordForm() {
  const { t } = useTranslation();
  const me = useMe();
  const toastError = useToastError();
  const form = useForm<z.infer<typeof passwordChangeSchema>>({
    resolver: zodResolver(passwordChangeSchema),
    defaultValues: { current_password: "", new_password: "", confirm: "" },
  });
  const save = useMutation({
    mutationFn: (v: z.infer<typeof passwordChangeSchema>) => security.changePassword({ current_password: v.current_password, new_password: v.new_password }),
    onSuccess: () => {
      toast.success(t("security.passwordChanged"));
      form.reset();
    },
    onError: (err) => {
      if (err instanceof ApiError && (err.status === 401 || err.code === "invalid_password")) form.setError("current_password", { message: "security.wrongPassword" });
      else toastError(err);
    },
  });
  const e = form.formState.errors;
  if (!me.user.has_password) return <p className="text-sm text-muted-foreground">{t("security.noLocalPassword")}</p>;
  return (
    <form onSubmit={form.handleSubmit((v) => save.mutate(v))} noValidate className="space-y-3">
      <input type="text" autoComplete="username" value={me.user.username} readOnly hidden />
      <div className="grid gap-3 sm:grid-cols-3">
        <Field label={t("security.currentPassword")} htmlFor="pw-cur" error={e.current_password?.message && t(e.current_password.message)}>
          <Input id="pw-cur" type="password" autoComplete="current-password" {...form.register("current_password")} />
        </Field>
        <Field label={t("security.newPassword")} htmlFor="pw-new" error={e.new_password?.message && t(e.new_password.message)}>
          <Input id="pw-new" type="password" autoComplete="new-password" {...form.register("new_password")} />
        </Field>
        <Field label={t("security.confirmPassword")} htmlFor="pw-conf" error={e.confirm?.message && t(e.confirm.message)}>
          <Input id="pw-conf" type="password" autoComplete="new-password" {...form.register("confirm")} />
        </Field>
      </div>
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">{t("security.passwordChangeHint")}</p>
        <Button type="submit" size="sm" disabled={save.isPending}>
          {t("security.changePassword")}
        </Button>
      </div>
    </form>
  );
}

function Identities() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const ids = useQuery({ queryKey: ["me", "identities"], queryFn: security.identities });
  const providers = useQuery({ queryKey: ["auth", "providers"], queryFn: () => auth.providers(), staleTime: 300_000 });
  const [unlinking, setUnlinking] = useState<number | null>(null);
  const link = useMutation({
    mutationFn: (slug: string) => security.linkIdentity(slug, "/my/profile"),
    onSuccess: (url) => navigateExternal(url),
    onError: toastError,
  });
  const unlink = useMutation({
    mutationFn: (id: number) => security.unlinkIdentity(id),
    onSuccess: () => {
      toast.success(t("identities.unlinked"));
      setUnlinking(null);
      void qc.invalidateQueries({ queryKey: ["me", "identities"] });
    },
    onError: toastError,
  });
  if (ids.isLoading) return <Skeleton className="h-20" />;
  const linked = ids.data?.items ?? [];
  const linkable = (providers.data ?? []).filter((p) => !linked.some((i) => i.provider_slug === p.slug));
  return (
    <div className="space-y-3">
      {linked.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("identities.none")}</p>
      ) : (
        <ul className="divide-y rounded-md border text-sm">
          {linked.map((i) => (
            <li key={i.id} className="flex items-center gap-3 px-3 py-2">
              <Badge variant="secondary">{i.provider_name}</Badge>
              <span className="min-w-0 flex-1 truncate">{i.email ?? i.username ?? <Mono>{i.subject}</Mono>}</span>
              <span className="hidden text-xs text-muted-foreground sm:inline">
                {t("identities.lastUsed")} <RelativeTime iso={i.last_login_at ?? i.created_at} />
              </span>
              <Button size="sm" variant="ghost" onClick={() => setUnlinking(i.id)}>
                <Unlink />
                {t("identities.unlink")}
              </Button>
            </li>
          ))}
        </ul>
      )}
      {linkable.length > 0 && (
        <div className="flex flex-wrap gap-2">
          {linkable.map((p) => (
            <Button key={p.slug} size="sm" variant="outline" disabled={link.isPending} onClick={() => link.mutate(p.slug)}>
              <Link2 />
              {t("identities.link", { name: p.display_name })}
            </Button>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={unlinking !== null}
        onOpenChange={(o) => !o && setUnlinking(null)}
        title={t("identities.unlinkTitle")}
        description={t("identities.unlinkDescription")}
        confirmLabel={t("identities.unlink")}
        destructive
        pending={unlink.isPending}
        onConfirm={() => unlinking !== null && unlink.mutate(unlinking)}
      />
    </div>
  );
}

export function ProfilePage() {
  const { t } = useTranslation();
  const table = useTableSearch({});
  const autoEnroll = table.raw.enroll === true || table.raw.enroll === "true";
  const linked = typeof table.raw.linked === "string" ? table.raw.linked : undefined;
  const linkError = typeof table.raw.link_error === "string" ? table.raw.link_error : undefined;
  const qc = useQueryClient();
  // Result of an identity link round-trip through the IdP (?linked= / ?link_error=).
  useEffect(() => {
    if (linked) {
      toast.success(t("identities.linked", { name: linked }));
      void qc.invalidateQueries({ queryKey: ["me", "identities"] });
    } else if (linkError) {
      toast.error(t(oidcErrorKey(linkError)));
    }
    if (linked || linkError) table.setSearch({ linked: undefined, link_error: undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [linked, linkError]);
  return (
    <div className="max-w-4xl space-y-4">
      <PageHeader title={t("nav.myProfile")} description={t("security.description")} />
      <Section title={t("security.profile")}>
        <ProfileForm />
      </Section>
      <Section title={t("security.twoFactor")} description={t("security.twoFactorHint")}>
        <TotpSection autoEnroll={autoEnroll} />
      </Section>
      <Section title={t("security.password")}>
        <PasswordForm />
      </Section>
      <Section title={t("identities.title")} description={t("identities.hint")}>
        <Identities />
      </Section>
    </div>
  );
}
