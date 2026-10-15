import { Link } from "@tanstack/react-router";
import { ShieldOff } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

export function ForbiddenPage() {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-[50vh] flex-col items-center justify-center gap-4 text-center">
      <div className="rounded-full bg-muted p-4">
        <ShieldOff className="h-6 w-6 text-muted-foreground" />
      </div>
      <p className="text-4xl font-bold text-muted-foreground">403</p>
      <h1 className="text-xl font-semibold">{t("forbidden.title")}</h1>
      <p className="max-w-md text-sm text-muted-foreground">{t("forbidden.description")}</p>
      <Button asChild variant="outline">
        <Link to="/">{t("placeholder.backHome")}</Link>
      </Button>
    </div>
  );
}
