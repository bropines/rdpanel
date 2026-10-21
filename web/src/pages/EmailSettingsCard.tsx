import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock, Save, Send } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { settingsApi, type EmailSettings, type EmailSettingsInput } from "@/api/system";
import { ErrorState, Field, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMe } from "@/lib/session";

type Encryption = EmailSettings["encryption"];

export interface EmailDraft {
  host: string;
  port: string;
  username: string;
  password: string;
  passwordTouched: boolean;
  from: string;
  encryption: Encryption;
  insecure_skip_verify: boolean;
  default_locale: "en" | "ru";
}

export function emailDraft(s: EmailSettings): EmailDraft {
  return {
    host: s.host,
    port: s.port ? String(s.port) : "",
    username: s.username,
    password: "",
    passwordTouched: false,
    from: s.from,
    encryption: s.encryption,
    insecure_skip_verify: s.insecure_skip_verify,
    default_locale: s.default_locale,
  };
}

/** Draft → PUT body. SMTP fields are omitted when the configuration owns them; an untouched password is kept. */
export function emailInput(d: EmailDraft, fromConfig: boolean): EmailSettingsInput {
  if (fromConfig) return { default_locale: d.default_locale };
  const body: EmailSettingsInput = {
    host: d.host.trim(),
    port: d.port.trim() ? Number(d.port) : 0,
    username: d.username.trim(),
    from: d.from.trim(),
    encryption: d.encryption,
    insecure_skip_verify: d.insecure_skip_verify,
    default_locale: d.default_locale,
  };
  if (d.passwordTouched) body.password = d.password;
  return body;
}

/** Settings → Email: SMTP server (panel-managed unless RDPANEL_SMTP_* is set), default language, test message. */
export function EmailSettingsCard() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const me = useMe();
  const toastError = useToastError();
  const q = useQuery({ queryKey: ["settings", "email"], queryFn: settingsApi.email });
  const [d, setD] = useState<EmailDraft | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [to, setTo] = useState(me.user.email ?? "");
  useEffect(() => {
    if (q.data) setD(emailDraft(q.data));
  }, [q.data]);

  const save = useMutation({
    mutationFn: (body: EmailSettingsInput) => settingsApi.updateEmail(body),
    onSuccess: (s) => {
      qc.setQueryData(["settings", "email"], s);
      void qc.invalidateQueries({ queryKey: ["auth", "options"] });
      setErrors({});
      toast.success(t("common.saved"));
    },
    onError: (err) => {
      if (err instanceof ApiError && err.fields) setErrors(err.fields);
      toastError(err);
    },
  });
  const test = useMutation({
    mutationFn: () => settingsApi.testEmail({ to: to.trim() || undefined }),
    onSuccess: (r) => toast.success(t("emailSettings.testSent", { to: r.to })),
    onError: (err) => (err instanceof ApiError && err.code === "email_send_failed" ? toast.error(t("emailSettings.testFailed", { error: err.message })) : toastError(err)),
  });

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data || !d) return <Skeleton className="h-64" />;
  const s = q.data;
  const fromConfig = s.source === "config";
  const set = (patch: Partial<EmailDraft>) => setD({ ...d, ...patch });

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex flex-wrap items-center gap-2 text-base">
          {t("emailSettings.title")}
          {fromConfig ? (
            <Badge variant="outline" className="gap-1 font-normal text-muted-foreground">
              <Lock className="h-3 w-3" />
              {t("settings.fromConfig", { env: "RDPANEL_SMTP_*" })}
            </Badge>
          ) : (
            <Badge variant={s.enabled ? "secondary" : "outline"}>{s.enabled ? t("emailSettings.on") : t("emailSettings.off")}</Badge>
          )}
        </CardTitle>
        <CardDescription>{t("emailSettings.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <Field label={t("emailSettings.host")} htmlFor="smtp-host" error={errors.host} hint={fromConfig ? undefined : t("emailSettings.hostHint")}>
            <Input id="smtp-host" value={d.host} disabled={fromConfig} placeholder="smtp.example.com" onChange={(e) => set({ host: e.target.value })} />
          </Field>
          <Field label={t("emailSettings.encryption")}>
            <Select value={d.encryption} disabled={fromConfig} onValueChange={(v) => set({ encryption: v as Encryption })}>
              <SelectTrigger aria-label={t("emailSettings.encryption")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="starttls">STARTTLS</SelectItem>
                <SelectItem value="tls">TLS (SMTPS)</SelectItem>
                <SelectItem value="none">{t("emailSettings.noEncryption")}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label={t("emailSettings.port")} htmlFor="smtp-port" error={errors.port} hint={t("emailSettings.portHint")}>
            <Input id="smtp-port" inputMode="numeric" value={d.port} disabled={fromConfig} onChange={(e) => set({ port: e.target.value.replace(/\D/g, "") })} />
          </Field>
          <Field label={t("emailSettings.username")} htmlFor="smtp-user" error={errors.username}>
            <Input id="smtp-user" autoComplete="off" value={d.username} disabled={fromConfig} onChange={(e) => set({ username: e.target.value })} />
          </Field>
          <Field label={t("emailSettings.password")} htmlFor="smtp-pass" error={errors.password} hint={s.password_set ? t("emailSettings.passwordSet") : undefined}>
            <Input
              id="smtp-pass"
              type="password"
              autoComplete="new-password"
              value={d.password}
              disabled={fromConfig}
              placeholder={s.password_set ? "••••••••" : ""}
              onChange={(e) => set({ password: e.target.value, passwordTouched: true })}
            />
          </Field>
          <Field label={t("emailSettings.from")} htmlFor="smtp-from" error={errors.from}>
            <Input id="smtp-from" value={d.from} disabled={fromConfig} placeholder="Panel <panel@example.com>" onChange={(e) => set({ from: e.target.value })} />
          </Field>
        </div>
        <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
          <div>
            <label htmlFor="smtp-insecure" className="text-sm font-medium">
              {t("emailSettings.insecure")}
            </label>
            <p className="text-xs text-muted-foreground">{t("emailSettings.insecureHint")}</p>
          </div>
          <Switch id="smtp-insecure" checked={d.insecure_skip_verify} disabled={fromConfig} onCheckedChange={(v) => set({ insecure_skip_verify: v })} />
        </div>
        <Field label={t("emailSettings.defaultLocale")} hint={t("emailSettings.defaultLocaleHint")}>
          <Select value={d.default_locale} onValueChange={(v) => set({ default_locale: v as "en" | "ru" })}>
            <SelectTrigger className="w-48" aria-label={t("emailSettings.defaultLocale")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="en">English</SelectItem>
              <SelectItem value="ru">Русский</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {!s.password_reset_available && s.enabled && <p className="text-xs text-muted-foreground">{t("emailSettings.resetNeedsUrl")}</p>}
        <div className="flex justify-end">
          <Button type="button" size="sm" disabled={save.isPending} onClick={() => save.mutate(emailInput(d, fromConfig))}>
            <Save />
            {t("common.save")}
          </Button>
        </div>
        <div className="flex flex-wrap items-end gap-2 border-t pt-4">
          <Field label={t("emailSettings.testTo")} htmlFor="smtp-test-to">
            <Input id="smtp-test-to" type="email" className="w-64" value={to} onChange={(e) => setTo(e.target.value)} />
          </Field>
          <Button type="button" variant="outline" size="sm" disabled={!s.enabled || test.isPending} onClick={() => test.mutate()}>
            <Send />
            {t("emailSettings.sendTest")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
