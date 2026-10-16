import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, MailCheck, Send } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { meQueryKey } from "@/api/auth";
import { security, type SecurityOverview } from "@/api/security";
import { Field, useToastError } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { passkeyKeys } from "@/pages/my/PasskeysSection";

export const EMAIL_CODE_RE = /^\d{6}$/;

/**
 * "E-mail" card of My profile: verification of the address (6-digit code),
 * e-mailed sign-in codes as the second factor, and the e-mail language.
 */
export function EmailSection() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const overview = useQuery({ queryKey: passkeyKeys.overview, queryFn: security.overview });
  const [codeSent, setCodeSent] = useState(false);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [askPassword, setAskPassword] = useState(false);

  const apply = (s: SecurityOverview) => {
    qc.setQueryData(passkeyKeys.overview, s);
    void qc.invalidateQueries({ queryKey: meQueryKey });
  };
  const send = useMutation({
    mutationFn: security.sendEmailVerification,
    onSuccess: () => {
      setCodeSent(true);
      toast.success(t("email.codeSent"));
    },
    onError: toastError,
  });
  const confirm = useMutation({
    mutationFn: (c: string) => security.confirmEmail(c),
    onSuccess: (s) => {
      apply(s);
      setCodeSent(false);
      setCode("");
      toast.success(t("email.verified"));
    },
    onError: toastError,
  });
  const toggle = useMutation({
    mutationFn: (v: { enabled: boolean; password?: string }) => security.setEmail2fa(v),
    onSuccess: (s) => {
      apply(s);
      setAskPassword(false);
      setPassword("");
      toast.success(s.email_2fa_enabled ? t("email.twoFaOn") : t("email.twoFaOff"));
    },
    onError: toastError,
  });
  const locale = useMutation({
    mutationFn: (l: string) => security.updateProfile({ locale: l === "default" ? null : (l as "en" | "ru") }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: passkeyKeys.overview });
      toast.success(t("common.saved"));
    },
    onError: toastError,
  });

  const s = overview.data;
  if (!s) return <Skeleton className="h-24" />;
  if (!s.email) return <p className="text-sm text-muted-foreground">{t("email.noAddress")}</p>;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <span className="font-mono">{s.email}</span>
        {s.email_verified ? (
          <Badge variant="secondary" className="gap-1">
            <MailCheck className="h-3 w-3" />
            {t("email.verifiedBadge")}
          </Badge>
        ) : (
          <Badge variant="outline">{t("email.unverifiedBadge")}</Badge>
        )}
      </div>

      {!s.email_available && <p className="text-sm text-muted-foreground">{t("email.unavailable")}</p>}

      {s.email_available && !s.email_verified && (
        <div className="space-y-3 rounded-md border p-3">
          <p className="text-sm text-muted-foreground">{t("email.verifyHint")}</p>
          <div className="flex flex-wrap items-end gap-2">
            <Button type="button" size="sm" variant="outline" disabled={send.isPending} onClick={() => send.mutate()}>
              {send.isPending ? <Loader2 className="animate-spin" /> : <Send />}
              {codeSent ? t("email.resendCode") : t("email.sendCode")}
            </Button>
            {codeSent && (
              <form
                className="flex items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (EMAIL_CODE_RE.test(code.replace(/\s/g, ""))) confirm.mutate(code.replace(/\s/g, ""));
                }}
              >
                <Field label={t("email.code")} htmlFor="email-code">
                  <Input
                    id="email-code"
                    autoComplete="one-time-code"
                    inputMode="numeric"
                    className="w-32 text-center font-mono tracking-widest"
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                  />
                </Field>
                <Button type="submit" size="sm" disabled={confirm.isPending || !EMAIL_CODE_RE.test(code.replace(/\s/g, ""))}>
                  {t("email.confirm")}
                </Button>
              </form>
            )}
          </div>
        </div>
      )}

      <div className="flex items-start justify-between gap-4 rounded-md border px-3 py-2.5">
        <div>
          <label htmlFor="email-2fa" className="text-sm font-medium">
            {t("email.twoFa")}
          </label>
          <p className="text-xs text-muted-foreground">
            {s.totp_enabled ? t("email.twoFaTotpWins") : s.email_2fa_required && s.email_verified ? t("email.twoFaRequired") : t("email.twoFaHint")}
          </p>
        </div>
        <Switch
          id="email-2fa"
          checked={s.email_2fa_enabled || (s.email_2fa_required && s.email_verified && !s.totp_enabled)}
          disabled={!s.email_verified || !s.email_available || toggle.isPending || (s.email_2fa_required && !s.totp_enabled)}
          onCheckedChange={(on) => {
            if (on) toggle.mutate({ enabled: true });
            else if (s.has_password) setAskPassword(true);
            else toggle.mutate({ enabled: false });
          }}
        />
      </div>
      {askPassword && (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            toggle.mutate({ enabled: false, password });
          }}
        >
          <Field label={t("security.currentPassword")} htmlFor="email-2fa-pw">
            <Input id="email-2fa-pw" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <Button type="submit" size="sm" variant="destructive" disabled={!password || toggle.isPending}>
            {t("email.disable")}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setAskPassword(false)}>
            {t("common.cancel")}
          </Button>
        </form>
      )}

      <Field label={t("email.language")} hint={t("email.languageHint")}>
        <Select value={s.locale ?? "default"} onValueChange={(v) => locale.mutate(v)}>
          <SelectTrigger className="w-56" aria-label={t("email.language")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="default">{t("email.languageDefault")}</SelectItem>
            <SelectItem value="en">English</SelectItem>
            <SelectItem value="ru">Русский</SelectItem>
          </SelectContent>
        </Select>
      </Field>
    </div>
  );
}
