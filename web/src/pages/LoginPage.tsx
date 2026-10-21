import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { ArrowLeft, Fingerprint, KeyRound, Loader2, Mail } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { auth, meQueryKey, navigateExternal, type AuthProvider, type Me, type MfaMethod } from "@/api/auth";
import { dashboardApi } from "@/api/system";
import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/misc";
import { isServerPath, safeNext } from "@/lib/redirect";
import { passkeysSupported, webauthnErrorKey } from "@/lib/webauthn";
import { loginErrorKey, oidcErrorKey } from "@/pages/login-errors";

const route = getRouteApi("/login");

export const passwordSchema = z.object({
  username: z.string().trim().min(1, "login.usernameRequired"),
  password: z.string().min(1, "login.passwordRequired"),
});

/** 6-digit TOTP or a recovery code (letters/digits, optional dashes). */
export const mfaSchema = z.object({
  code: z
    .string()
    .trim()
    .min(1, "login.codeRequired")
    .refine((v) => /^\d{6}$/.test(v.replace(/\s/g, "")) || /^[A-Za-z0-9]{4,}(-[A-Za-z0-9]{4,})*$/.test(v), "login.codeInvalid"),
});

function ProviderIcon({ kind }: { kind: string }) {
  if (kind === "github")
    return (
      <svg viewBox="0 0 16 16" aria-hidden className="h-4 w-4 fill-current">
        <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
      </svg>
    );
  if (kind === "google")
    return (
      <svg viewBox="0 0 24 24" aria-hidden className="h-4 w-4">
        <path fill="#EA4335" d="M12 10.2v3.9h5.5c-.2 1.3-1.6 3.8-5.5 3.8-3.3 0-6-2.7-6-6.1s2.7-6.1 6-6.1c1.9 0 3.1.8 3.8 1.5l2.6-2.5C16.8 3.2 14.6 2.2 12 2.2 6.6 2.2 2.2 6.6 2.2 12s4.4 9.8 9.8 9.8c5.7 0 9.4-4 9.4-9.6 0-.6-.1-1.1-.2-1.6H12z" />
      </svg>
    );
  return <KeyRound className="h-4 w-4" aria-hidden />;
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const search = route.useSearch();
  const next = safeNext(search.next);
  const [step, setStep] = useState<"password" | "mfa" | "forgot">(search.mfa ? "mfa" : "password");
  const [error, setError] = useState<string | null>(search.error ? oidcErrorKey(search.error) : null);
  // Second factors offered by /auth/login; unknown after an OIDC redirect
  // (except "?mfa=email": an e-mailed code is owed).
  const [mfaMethods, setMfaMethods] = useState<MfaMethod[] | undefined>(search.mfa === "email" ? ["email"] : undefined);
  const [emailHint, setEmailHint] = useState<string | undefined>(undefined);
  const [notice, setNotice] = useState<string | null>(null);
  const [forgotLogin, setForgotLogin] = useState("");
  const isEmail = !!mfaMethods?.includes("email");
  const authOptions = useQuery({ queryKey: ["auth", "options"], queryFn: () => auth.options(), staleTime: 300_000 });

  const branding = useQuery({ queryKey: ["branding"], queryFn: () => dashboardApi.branding(), staleTime: 300_000 });
  const options = useQuery({ queryKey: ["auth", "providers", "login-options"], queryFn: () => auth.loginOptions(), staleTime: 300_000 });
  const canPasskey = !!options.data?.passkey && passkeysSupported();
  const passkeyError = (err: unknown) => setError(webauthnErrorKey(err) ?? loginErrorKey(err));

  const done = (me: Me) => {
    qc.setQueryData(meQueryKey, me);
    if (isServerPath(next)) navigateExternal(next);
    else void navigate({ href: next, replace: true });
  };

  const pwForm = useForm<z.infer<typeof passwordSchema>>({
    resolver: zodResolver(passwordSchema),
    defaultValues: { username: "", password: "" },
  });
  const mfaForm = useForm<z.infer<typeof mfaSchema>>({ resolver: zodResolver(mfaSchema), defaultValues: { code: "" } });

  const login = useMutation({
    mutationFn: auth.login,
    onSuccess: (res) => {
      setError(null);
      if (res.kind === "mfa") {
        setMfaMethods(res.methods);
        setEmailHint(res.emailHint);
        setNotice(null);
        setStep("mfa");
        return;
      }
      done(res.me);
    },
    onError: (err) => setError(loginErrorKey(err)),
  });

  const oidcStart = useMutation({
    mutationFn: (slug: string) => auth.oidcStart(slug, next),
    onSuccess: (url) => navigateExternal(url),
    onError: (err) => setError(loginErrorKey(err)),
  });

  const mfa = useMutation({
    mutationFn: (code: string) => auth.mfa(code.replace(/\s/g, "")),
    onSuccess: done,
    onError: (err) => {
      setError(loginErrorKey(err));
      mfaForm.reset({ code: "" });
    },
  });

  const resend = useMutation({
    mutationFn: () => auth.mfaResend(),
    onSuccess: () => {
      setError(null);
      setNotice("login.emailCodeResent");
    },
    onError: (err) => setError(loginErrorKey(err)),
  });

  const forgot = useMutation({
    mutationFn: (login: string) => auth.forgotPassword(login.trim()),
    onSuccess: () => {
      setError(null);
      setNotice("login.forgotSent");
    },
    onError: (err) => setError(loginErrorKey(err)),
  });

  const passkeyLogin = useMutation({ mutationFn: () => auth.passkeyLogin(), onSuccess: done, onError: passkeyError });
  const passkeyMfa = useMutation({ mutationFn: () => auth.passkeyMfa(), onSuccess: done, onError: passkeyError });

  const title = branding.data?.title || t("app.title");
  const oidc: AuthProvider[] = options.data?.providers ?? [];
  const mfaPasskey = canPasskey && !isEmail && (mfaMethods === undefined || mfaMethods.includes("passkey"));
  const headTitle = step === "mfa" ? t("login.mfaTitle") : step === "forgot" ? t("login.forgotTitle") : title;
  const headDesc =
    step === "mfa"
      ? isEmail
        ? t("login.emailMfaDescription")
        : t("login.mfaDescription")
      : step === "forgot"
        ? t("login.forgotDescription")
        : t("login.description");
  const backToPassword = () => {
    setStep("password");
    setError(null);
    setNotice(null);
  };
  const pwErrors = pwForm.formState.errors;

  return (
    <div className="flex min-h-screen flex-col bg-muted/40">
      <div className="flex justify-end gap-1 p-4">
        <LanguageSwitcher />
        <ThemeToggle />
      </div>
      <div className="flex flex-1 items-start justify-center p-4 sm:items-center">
        <Card className="w-full max-w-sm shadow-sm">
          <CardHeader className="items-center text-center">
            {branding.data?.logo_url ? (
              <img src={branding.data.logo_url} alt="" className="mb-2 h-10 w-10 rounded object-contain" />
            ) : (
              <img src="/favicon.svg" alt="" className="mb-2 h-10 w-10" />
            )}
            <CardTitle className="text-xl">{headTitle}</CardTitle>
            <CardDescription>{headDesc}</CardDescription>
          </CardHeader>

          {error && (
            <div role="alert" className="mx-6 mb-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {t(error)}
            </div>
          )}
          {notice && !error && (
            <div role="status" className="mx-6 mb-2 rounded-md border bg-muted px-3 py-2 text-sm">
              {t(notice)}
            </div>
          )}

          {step === "forgot" ? (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                if (forgotLogin.trim()) forgot.mutate(forgotLogin);
              }}
              noValidate
            >
              <CardContent className="space-y-2">
                <Label htmlFor="forgot-login">{t("login.forgotLogin")}</Label>
                <Input id="forgot-login" autoComplete="username" autoFocus value={forgotLogin} onChange={(e) => setForgotLogin(e.target.value)} />
              </CardContent>
              <CardFooter className="flex-col gap-2">
                <Button type="submit" className="w-full" disabled={forgot.isPending || !forgotLogin.trim()}>
                  {forgot.isPending && <Loader2 className="animate-spin" />}
                  {t("login.forgotSubmit")}
                </Button>
                <Button type="button" variant="ghost" className="w-full" onClick={backToPassword}>
                  <ArrowLeft />
                  {t("login.back")}
                </Button>
              </CardFooter>
            </form>
          ) : step === "password" ? (
            <form onSubmit={pwForm.handleSubmit((v) => login.mutate(v))} noValidate>
              <CardContent className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="username">{t("login.username")}</Label>
                  <Input id="username" autoComplete="username" autoFocus aria-invalid={!!pwErrors.username} {...pwForm.register("username")} />
                  {pwErrors.username?.message && <p className="text-sm text-destructive">{t(pwErrors.username.message)}</p>}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="password">{t("login.password")}</Label>
                  <Input
                    id="password"
                    type="password"
                    autoComplete="current-password"
                    aria-invalid={!!pwErrors.password}
                    {...pwForm.register("password")}
                  />
                  {pwErrors.password?.message && <p className="text-sm text-destructive">{t(pwErrors.password.message)}</p>}
                  {authOptions.data?.password_reset && (
                    <button
                      type="button"
                      className="text-xs text-muted-foreground underline-offset-4 hover:underline"
                      onClick={() => {
                        setForgotLogin(pwForm.getValues("username"));
                        setError(null);
                        setNotice(null);
                        setStep("forgot");
                      }}
                    >
                      {t("login.forgot")}
                    </button>
                  )}
                </div>
              </CardContent>
              <CardFooter className="flex-col gap-3">
                <Button type="submit" className="w-full" disabled={login.isPending}>
                  {login.isPending && <Loader2 className="animate-spin" />}
                  {login.isPending ? t("login.submitting") : t("login.submit")}
                </Button>
                {(oidc.length > 0 || canPasskey) && (
                  <>
                    <div className="flex w-full items-center gap-3 text-xs text-muted-foreground">
                      <Separator className="flex-1" />
                      {t("login.or")}
                      <Separator className="flex-1" />
                    </div>
                    {canPasskey && (
                      <Button
                        type="button"
                        variant="outline"
                        className="w-full"
                        disabled={passkeyLogin.isPending}
                        onClick={() => {
                          setError(null);
                          passkeyLogin.mutate();
                        }}
                      >
                        {passkeyLogin.isPending ? <Loader2 className="animate-spin" /> : <Fingerprint />}
                        {t("login.passkey")}
                      </Button>
                    )}
                    {oidc.map((p) => (
                      <Button
                        key={p.slug}
                        type="button"
                        variant="outline"
                        className="w-full"
                        disabled={oidcStart.isPending}
                        onClick={() => {
                          setError(null);
                          oidcStart.mutate(p.slug);
                        }}
                      >
                        {oidcStart.isPending && oidcStart.variables === p.slug ? <Loader2 className="animate-spin" /> : <ProviderIcon kind={p.kind} />}
                        {t("login.continueWith", { name: p.display_name })}
                      </Button>
                    ))}
                  </>
                )}
              </CardFooter>
            </form>
          ) : (
            <form onSubmit={mfaForm.handleSubmit((v) => mfa.mutate(v.code))} noValidate>
              <CardContent className="space-y-2">
                <Label htmlFor="code">{isEmail ? t("login.emailCode") : t("login.totp")}</Label>
                <Input
                  id="code"
                  autoComplete="one-time-code"
                  inputMode="text"
                  autoFocus
                  className="text-center font-mono text-lg tracking-widest"
                  aria-invalid={!!mfaForm.formState.errors.code}
                  {...mfaForm.register("code")}
                />
                {mfaForm.formState.errors.code?.message ? (
                  <p className="text-sm text-destructive">{t(mfaForm.formState.errors.code.message)}</p>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    {isEmail ? (emailHint ? t("login.emailCodeHintTo", { email: emailHint }) : t("login.emailCodeHint")) : t("login.totpHint")}
                  </p>
                )}
              </CardContent>
              <CardFooter className="flex-col gap-2">
                <Button type="submit" className="w-full" disabled={mfa.isPending}>
                  {mfa.isPending && <Loader2 className="animate-spin" />}
                  {t("login.verify")}
                </Button>
                {isEmail && (
                  <Button type="button" variant="outline" className="w-full" disabled={resend.isPending} onClick={() => resend.mutate()}>
                    {resend.isPending ? <Loader2 className="animate-spin" /> : <Mail />}
                    {t("login.emailResend")}
                  </Button>
                )}
                {mfaPasskey && (
                  <Button
                    type="button"
                    variant="outline"
                    className="w-full"
                    disabled={passkeyMfa.isPending}
                    onClick={() => {
                      setError(null);
                      passkeyMfa.mutate();
                    }}
                  >
                    {passkeyMfa.isPending ? <Loader2 className="animate-spin" /> : <Fingerprint />}
                    {t("login.usePasskey")}
                  </Button>
                )}
                <Button
                  type="button"
                  variant="ghost"
                  className="w-full"
                  onClick={backToPassword}
                >
                  <ArrowLeft />
                  {t("login.back")}
                </Button>
              </CardFooter>
            </form>
          )}
        </Card>
      </div>
    </div>
  );
}
