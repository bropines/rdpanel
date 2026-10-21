import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ImageUp, Lock, RotateCcw, Save, Trash2 } from "lucide-react";
import { useEffect, useRef, type ReactNode } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { dashboardApi, settingsApi, type PanelSettings, type PanelSettingsPatch } from "@/api/system";
import { ErrorState, Field, Markdown, PageHeader, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch, Tabs, TabsContent, TabsList, TabsTrigger, Textarea } from "@/components/ui/misc";
import { displayName, useMe } from "@/lib/session";
import { EmailSettingsCard } from "@/pages/EmailSettingsCard";
import { LDAPSettingsCard } from "@/pages/LDAPSettingsCard";

const int = (min: number, max = Number.MAX_SAFE_INTEGER) =>
  z
    .string()
    .trim()
    .superRefine((v, ctx) => {
      if (!/^\d+$/.test(v)) ctx.addIssue({ code: "custom", message: "validation.integer" });
      else if (Number(v) < min || Number(v) > max) ctx.addIssue({ code: "custom", message: "validation.range" });
    });

const httpUrl = z
  .string()
  .trim()
  .refine((v) => {
    if (!v) return true;
    try {
      const u = new URL(v);
      return (u.protocol === "https:" || u.protocol === "http:") && !u.search && !u.hash;
    } catch {
      return false;
    }
  }, "settings.urlInvalid");

export const settingsSchema = z.object({
  web_client_url: httpUrl,
  title: z.string().trim().min(1, "validation.required").max(64, "validation.tooLong"),
  welcome_markdown: z.string().max(20000, "validation.tooLong"),
  share_page_title: z.string().trim().max(80, "validation.tooLong"),
  share_description: z.string().max(500, "validation.tooLong"),
  password_login_enabled: z.boolean(),
  require_admin_2fa: z.boolean(),
  require_email_2fa: z.boolean(),
  session_idle_hours: int(1, 720),
  session_absolute_days: int(1, 365),
  client_token_lifetime_hours: int(1, 87600),
  login_events_days: int(0),
  connection_audits_days: int(0),
  file_transfer_audits_days: int(0),
  alarm_audits_days: int(0),
  admin_audit_days: int(0),
  punch_requests_days: int(0),
});
export type SettingsValues = z.infer<typeof settingsSchema>;

const RETENTION = [
  "login_events_days",
  "connection_audits_days",
  "file_transfer_audits_days",
  "alarm_audits_days",
  "admin_audit_days",
  "punch_requests_days",
] as const;

function toValues(s: PanelSettings): SettingsValues {
  return {
    web_client_url: s.branding.web_client_url ?? "",
    title: s.branding.title,
    welcome_markdown: s.branding.welcome_markdown,
    share_page_title: s.share_links.page_title,
    share_description: s.share_links.description,
    password_login_enabled: s.auth.password_login_enabled,
    require_admin_2fa: s.auth.require_admin_2fa,
    require_email_2fa: s.auth.require_email_2fa,
    session_idle_hours: String(s.auth.session_idle_hours),
    session_absolute_days: String(s.auth.session_absolute_days),
    client_token_lifetime_hours: String(s.auth.client_token_lifetime_hours),
    ...Object.fromEntries(RETENTION.map((k) => [k, String(s.retention[k])])),
  } as SettingsValues;
}

/**
 * Form values → PATCH body. public_url, admin_url, trusted_proxies and rate
 * limits are configuration (environment) values and never sent. Exported for tests.
 */
export function toPatch(v: SettingsValues): PanelSettingsPatch {
  return {
    branding: { title: v.title.trim(), welcome_markdown: v.welcome_markdown, web_client_url: v.web_client_url.trim() || null },
    share_links: { page_title: v.share_page_title.trim(), description: v.share_description.trim() },
    auth: {
      password_login_enabled: v.password_login_enabled,
      require_admin_2fa: v.require_admin_2fa,
      require_email_2fa: v.require_email_2fa,
      session_idle_hours: Number(v.session_idle_hours),
      session_absolute_days: Number(v.session_absolute_days),
      client_token_lifetime_hours: Number(v.client_token_lifetime_hours),
    },
    retention: Object.fromEntries(RETENTION.map((k) => [k, Number(v[k])])),
  };
}

/** Host name of a URL ("" when it is not one). */
function hostOf(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  );
}

function SwitchRow({ id, label, hint, control }: { id: string; label: string; hint?: string; control: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
      <div>
        <label htmlFor={id} className="text-sm font-medium">
          {label}
        </label>
        {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
      </div>
      {control}
    </div>
  );
}

/** A value that comes from the server configuration (read-only in the UI). */
function ConfigValue({ label, env, children }: { label: string; env: string; children: ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
        {label}
        <Badge variant="outline" className="gap-1 font-normal text-muted-foreground">
          <Lock className="h-3 w-3" />
          {t("settings.fromConfig", { env })}
        </Badge>
      </div>
      <div className="rounded-md border bg-muted/40 px-3 py-2 font-mono text-sm">{children}</div>
    </div>
  );
}

function LogoUploader({ hasLogo }: { hasLogo: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const input = useRef<HTMLInputElement>(null);
  const branding = useQuery({ queryKey: ["branding"], queryFn: dashboardApi.branding });
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["branding"] });
    void qc.invalidateQueries({ queryKey: ["settings"] });
  };
  const upload = useMutation({
    mutationFn: (f: File) => settingsApi.uploadLogo(f),
    onSuccess: () => {
      toast.success(t("settings.logoUploaded"));
      refresh();
    },
    onError: toastError,
  });
  const remove = useMutation({ mutationFn: settingsApi.deleteLogo, onSuccess: refresh, onError: toastError });
  return (
    <div className="flex flex-wrap items-center gap-3">
      <div className="flex h-14 w-14 items-center justify-center overflow-hidden rounded-md border bg-muted">
        {branding.data?.logo_url ? <img src={branding.data.logo_url} alt="" className="h-full w-full object-contain" /> : <ImageUp className="h-5 w-5 text-muted-foreground" />}
      </div>
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/webp"
        className="hidden"
        onChange={(e) => {
          const f = e.target.files?.[0];
          e.target.value = "";
          if (!f) return;
          if (f.size > 256 * 1024) return void toast.error(t("settings.logoTooBig"));
          if (!["image/png", "image/jpeg", "image/webp"].includes(f.type)) return void toast.error(t("settings.logoType"));
          upload.mutate(f);
        }}
      />
      <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()} disabled={upload.isPending}>
        <ImageUp />
        {t("settings.uploadLogo")}
      </Button>
      {hasLogo && (
        <Button type="button" variant="ghost" size="sm" onClick={() => remove.mutate()} disabled={remove.isPending}>
          <Trash2 />
          {t("settings.removeLogo")}
        </Button>
      )}
      <p className="w-full text-xs text-muted-foreground">{t("settings.logoHint")}</p>
    </div>
  );
}

export function SettingsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const me = useMe();
  const toastError = useToastError();
  const q = useQuery({ queryKey: ["settings"], queryFn: settingsApi.get });
  const form = useForm<SettingsValues>({ resolver: zodResolver(settingsSchema) });
  useEffect(() => {
    if (q.data) form.reset(toValues(q.data));
  }, [q.data, form]);

  const save = useMutation({
    mutationFn: (v: SettingsValues) => settingsApi.update(toPatch(v)),
    onSuccess: (s) => {
      qc.setQueryData(["settings"], s);
      void qc.invalidateQueries({ queryKey: ["branding"] });
      form.reset(toValues(s));
      toast.success(t("common.saved"));
    },
    onError: toastError,
  });

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-[60vh]" />;

  const errors = form.formState.errors as Record<string, { message?: string } | undefined>;
  const err = (k: keyof SettingsValues) => (errors[k]?.message ? t(errors[k]!.message!) : undefined);
  const num = (k: keyof SettingsValues, label: string, hint?: string) => (
    <Field key={k} label={label} htmlFor={`s-${k}`} error={err(k)} hint={hint}>
      <Input id={`s-${k}`} inputMode="numeric" className="max-w-[10rem]" {...form.register(k)} />
    </Field>
  );
  const welcome = form.watch("welcome_markdown") ?? "";

  return (
    <form
      onSubmit={form.handleSubmit(
        (v) => save.mutate(v),
        (errs) => toast.error(t("settings.fixErrors", { count: Object.keys(errs).length })),
      )}
      noValidate
      className="pb-20"
    >
      <PageHeader title={t("nav.settings")} description={t("settings.description")} />
      <Tabs defaultValue="general">
        <TabsList>
          <TabsTrigger value="general">{t("settings.tabs.general")}</TabsTrigger>
          <TabsTrigger value="branding">{t("settings.tabs.branding")}</TabsTrigger>
          <TabsTrigger value="security">{t("settings.tabs.security")}</TabsTrigger>
          <TabsTrigger value="ldap">{t("settings.tabs.ldap")}</TabsTrigger>
          <TabsTrigger value="email">{t("settings.tabs.email")}</TabsTrigger>
          <TabsTrigger value="retention">{t("settings.tabs.retention")}</TabsTrigger>
        </TabsList>

        <TabsContent value="general" className="space-y-4">
          <Section title={t("settings.addresses")} description={t("settings.addressesHint")}>
            <ConfigValue label={t("settings.publicUrl")} env="RDPANEL_PUBLIC_URL">
              {q.data.public_url || <span className="text-muted-foreground">{t("settings.notSet")}</span>}
            </ConfigValue>
            <ConfigValue label={t("settings.adminUrl")} env="RDPANEL_ADMIN_URL">
              {q.data.admin_url || <span className="text-muted-foreground">{t("settings.notSet")}</span>}
            </ConfigValue>
            <ConfigValue label={t("settings.trustedProxies")} env="RDPANEL_TRUSTED_PROXIES">
              {q.data.trusted_proxies.length ? q.data.trusted_proxies.join(", ") : <span className="text-muted-foreground">{t("settings.notSet")}</span>}
            </ConfigValue>
            <Field label={t("settings.webClientUrl")} htmlFor="s-web_client_url" error={err("web_client_url")} hint={t("settings.webClientUrlHint")}>
              <Input id="s-web_client_url" placeholder="https://web.example.com" {...form.register("web_client_url")} />
            </Field>
          </Section>
        </TabsContent>

        <TabsContent value="branding" className="space-y-4">
          <Section title={t("settings.branding")}>
            <Field label={t("settings.title")} htmlFor="s-title" error={err("title")}>
              <Input id="s-title" maxLength={64} {...form.register("title")} />
            </Field>
            <Field label={t("settings.logo")}>
              <LogoUploader hasLogo={q.data.branding.has_logo} />
            </Field>
          </Section>
          <Section title={t("settings.welcome")} description={t("settings.welcomeHint", { username: "{{username}}" })}>
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <Field label={t("settings.markdown")} htmlFor="s-welcome" error={err("welcome_markdown")}>
                <Textarea id="s-welcome" rows={12} className="font-mono text-xs" {...form.register("welcome_markdown")} />
              </Field>
              <div className="space-y-1.5">
                <div className="text-sm font-medium">{t("settings.preview")}</div>
                <div className="min-h-[12rem] rounded-md border bg-muted/30 p-3">
                  {welcome.trim() ? <Markdown source={welcome} vars={{ username: displayName(me) }} /> : <p className="text-sm text-muted-foreground">{t("dashboard.welcomeEmpty")}</p>}
                </div>
              </div>
            </div>
          </Section>
          {q.data.share_links.enabled && (
            <Section title={t("share.settings.title")} description={t("share.settings.hint")}>
              <Field label={t("share.settings.pageTitle")} htmlFor="s-share_page_title" error={err("share_page_title")}>
                <Input
                  id="s-share_page_title"
                  maxLength={80}
                  placeholder={t("share.public.defaultTitle", { host: hostOf(q.data.public_url) })}
                  {...form.register("share_page_title")}
                />
              </Field>
              <Field label={t("share.settings.description")} htmlFor="s-share_description" error={err("share_description")} hint={t("share.settings.descriptionHint")}>
                <Textarea id="s-share_description" rows={3} maxLength={500} {...form.register("share_description")} />
              </Field>
            </Section>
          )}
        </TabsContent>

        <TabsContent value="security" className="space-y-4">
          <Section title={t("settings.authentication")}>
            <SwitchRow
              id="s-password_login_enabled"
              label={t("settings.passwordLogin")}
              hint={t("settings.passwordLoginHint")}
              control={<Controller control={form.control} name="password_login_enabled" render={({ field }) => <Switch id="s-password_login_enabled" checked={field.value} onCheckedChange={field.onChange} />} />}
            />
            <SwitchRow
              id="s-require_admin_2fa"
              label={t("settings.requireAdmin2fa")}
              hint={t("settings.requireAdmin2faHint")}
              control={<Controller control={form.control} name="require_admin_2fa" render={({ field }) => <Switch id="s-require_admin_2fa" checked={field.value} onCheckedChange={field.onChange} />} />}
            />
            <SwitchRow
              id="s-require_email_2fa"
              label={t("settings.requireEmail2fa")}
              hint={t("settings.requireEmail2faHint")}
              control={<Controller control={form.control} name="require_email_2fa" render={({ field }) => <Switch id="s-require_email_2fa" checked={field.value} onCheckedChange={field.onChange} />} />}
            />
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              {num("session_idle_hours", t("settings.sessionIdle"), t("settings.hours"))}
              {num("session_absolute_days", t("settings.sessionAbsolute"), t("settings.days"))}
              {num("client_token_lifetime_hours", t("settings.clientToken"), t("settings.hours"))}
            </div>
          </Section>
          <Section title={t("settings.rateLimits")} description={t("settings.rateLimitsHint")}>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <ConfigValue label={t("settings.loginBackoff")} env={t("settings.serverConfig")}>
                {t("settings.loginBackoffValue", {
                  free: q.data.rate_limits.login_free_attempts,
                  base: q.data.rate_limits.login_base_delay_seconds,
                  max: Math.round(q.data.rate_limits.login_max_delay_seconds / 60),
                })}
              </ConfigValue>
              <ConfigValue label={t("settings.deviceRate")} env={t("settings.serverConfig")}>
                {t("settings.deviceRateValue", { rate: q.data.rate_limits.device_requests_per_second, burst: q.data.rate_limits.device_burst })}
              </ConfigValue>
            </div>
          </Section>
        </TabsContent>

        <TabsContent value="ldap" className="space-y-4">
          <LDAPSettingsCard />
        </TabsContent>

        <TabsContent value="email" className="space-y-4">
          <EmailSettingsCard />
        </TabsContent>

        <TabsContent value="retention" className="space-y-4">
          <Section title={t("settings.retention")} description={t("settings.retentionHint")}>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {RETENTION.map((k) => num(k, t(`settings.retentionKeys.${k}`), t("settings.daysZeroForever")))}
            </div>
          </Section>
        </TabsContent>
      </Tabs>

      {form.formState.isDirty && (
        <div className="fixed inset-x-3 bottom-3 z-20 mx-auto flex max-w-3xl flex-wrap items-center gap-3 rounded-lg border bg-background/95 px-4 py-3 shadow-lg backdrop-blur md:left-64">
          <span className="text-sm">{t("settings.unsaved")}</span>
          <div className="ml-auto flex gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => form.reset(toValues(q.data))}>
              <RotateCcw />
              {t("common.discard")}
            </Button>
            <Button type="submit" size="sm" disabled={save.isPending}>
              <Save />
              {t("common.save")}
            </Button>
          </div>
        </div>
      )}
    </form>
  );
}
