import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="flex min-h-[50vh] flex-col items-center justify-center gap-4 text-center">
      <p className="text-5xl font-bold text-muted-foreground">404</p>
      <h1 className="text-xl font-semibold">{t("placeholder.notFound")}</h1>
      <Button asChild variant="outline">
        <Link to="/">{t("placeholder.backHome")}</Link>
      </Button>
    </div>
  );
}
