import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { MonitorCog } from "lucide-react";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { z } from "zod";

import { auth, type LoginRequest } from "@/api/auth";
import { ApiError } from "@/api/client";
import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { loginErrorKey } from "@/pages/login-errors";

const schema = z.object({
  username: z.string().trim().min(1, "login.usernameRequired"),
  password: z.string().min(1, "login.passwordRequired"),
  totp_code: z.string().trim().optional(),
});
type FormValues = z.infer<typeof schema>;

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [needTotp, setNeedTotp] = useState(false);
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { username: "", password: "" } });

  const login = useMutation({
    mutationFn: (body: LoginRequest) => auth.login(body),
    onSuccess: () => void navigate({ to: "/" }),
    onError: (err) => {
      if (err instanceof ApiError && err.code === "totp_required") {
        setNeedTotp(true);
        return;
      }
      toast.error(t(loginErrorKey(err)));
    },
  });

  const onSubmit = handleSubmit((values) => {
    const body: LoginRequest = { username: values.username, password: values.password };
    if (needTotp && values.totp_code) body.totp_code = values.totp_code;
    login.mutate(body);
  });

  return (
    <div className="flex min-h-screen flex-col bg-muted/40">
      <div className="flex justify-end gap-1 p-4">
        <LanguageSwitcher />
        <ThemeToggle />
      </div>
      <div className="flex flex-1 items-center justify-center p-4">
        <Card className="w-full max-w-sm">
          <form onSubmit={onSubmit} noValidate>
            <CardHeader className="items-center text-center">
              <MonitorCog className="mb-2 h-8 w-8 text-primary" aria-hidden />
              <CardTitle className="text-xl">{t("login.title")}</CardTitle>
              <CardDescription>{t("login.description")}</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="username">{t("login.username")}</Label>
                <Input
                  id="username"
                  autoComplete="username"
                  autoFocus
                  aria-invalid={!!errors.username}
                  {...register("username")}
                />
                {errors.username?.message && (
                  <p className="text-sm text-destructive">{t(errors.username.message)}</p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">{t("login.password")}</Label>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  aria-invalid={!!errors.password}
                  {...register("password")}
                />
                {errors.password?.message && (
                  <p className="text-sm text-destructive">{t(errors.password.message)}</p>
                )}
              </div>
              {needTotp && (
                <div className="space-y-2">
                  <Label htmlFor="totp_code">{t("login.totp")}</Label>
                  <Input
                    id="totp_code"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    autoFocus
                    {...register("totp_code")}
                  />
                  <p className="text-xs text-muted-foreground">{t("login.totpHint")}</p>
                </div>
              )}
            </CardContent>
            <CardFooter>
              <Button type="submit" className="w-full" disabled={login.isPending}>
                {login.isPending ? t("login.submitting") : t("login.submit")}
              </Button>
            </CardFooter>
          </form>
        </Card>
      </div>
    </div>
  );
}
