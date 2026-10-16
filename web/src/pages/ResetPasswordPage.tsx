import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { CheckCircle2, KeyRound, Loader2 } from "lucide-react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import { auth } from "@/api/auth";
import { dashboardApi } from "@/api/system";
import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { loginErrorKey } from "@/pages/login-errors";

const route = getRouteApi("/reset-password");

export const resetSchema = z
  .object({
    password: z.string().min(8, "validation.passwordMin").max(1024, "validation.tooLong"),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, { path: ["confirm"], message: "security.passwordMismatch" });

/** Target of the e-mailed reset link: /reset-password?token=… */
export function ResetPasswordPage() {
  const { t } = useTranslation();
  const { token } = route.useSearch();
  const branding = useQuery({ queryKey: ["branding"], queryFn: () => dashboardApi.branding(), staleTime: 300_000 });
  const form = useForm<z.infer<typeof resetSchema>>({ resolver: zodResolver(resetSchema), defaultValues: { password: "", confirm: "" } });
  const reset = useMutation({ mutationFn: (pw: string) => auth.resetPassword(token ?? "", pw) });
  const errors = form.formState.errors;

  return (
    <div className="flex min-h-screen flex-col bg-muted/40">
      <div className="flex justify-end gap-1 p-4">
        <LanguageSwitcher />
        <ThemeToggle />
      </div>
      <div className="flex flex-1 items-start justify-center p-4 sm:items-center">
        <Card className="w-full max-w-sm shadow-sm">
          <CardHeader className="items-center text-center">
            <KeyRound className="mb-2 h-9 w-9 text-primary" aria-hidden />
            <CardTitle className="text-xl">{t("reset.title")}</CardTitle>
            <CardDescription>{branding.data?.title || t("app.title")}</CardDescription>
          </CardHeader>
          {!token ? (
            <CardContent>
              <p role="alert" className="text-sm text-destructive">
                {t("login.resetTokenInvalid")}
              </p>
            </CardContent>
          ) : reset.isSuccess ? (
            <CardContent className="space-y-3 text-center">
              <CheckCircle2 className="mx-auto h-8 w-8 text-green-600" aria-hidden />
              <p className="text-sm">{t("reset.done")}</p>
            </CardContent>
          ) : (
            <form onSubmit={form.handleSubmit((v) => reset.mutate(v.password))} noValidate>
              <CardContent className="space-y-4">
                {reset.isError && (
                  <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                    {t(loginErrorKey(reset.error))}
                  </div>
                )}
                <div className="space-y-2">
                  <Label htmlFor="new-password">{t("reset.newPassword")}</Label>
                  <Input id="new-password" type="password" autoComplete="new-password" autoFocus {...form.register("password")} />
                  {errors.password?.message && <p className="text-sm text-destructive">{t(errors.password.message, { min: 8 })}</p>}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="confirm-password">{t("reset.confirm")}</Label>
                  <Input id="confirm-password" type="password" autoComplete="new-password" {...form.register("confirm")} />
                  {errors.confirm?.message && <p className="text-sm text-destructive">{t(errors.confirm.message)}</p>}
                </div>
                <p className="text-xs text-muted-foreground">{t("reset.hint")}</p>
              </CardContent>
              <CardFooter>
                <Button type="submit" className="w-full" disabled={reset.isPending}>
                  {reset.isPending && <Loader2 className="animate-spin" />}
                  {t("reset.submit")}
                </Button>
              </CardFooter>
            </form>
          )}
          <CardFooter className="justify-center">
            <Link to="/login" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
              {t("reset.toLogin")}
            </Link>
          </CardFooter>
        </Card>
      </div>
    </div>
  );
}
