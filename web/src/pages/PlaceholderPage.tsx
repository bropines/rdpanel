import { Construction } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

/** Stand-in for pages that stream D will implement. */
export function PlaceholderPage({ titleKey }: { titleKey: string }) {
  const { t } = useTranslation();
  return (
    <div className="mx-auto max-w-5xl space-y-6">
      <h1 className="text-2xl font-semibold tracking-tight">{t(titleKey)}</h1>
      <Card>
        <CardHeader className="flex flex-row items-center gap-3 space-y-0">
          <Construction className="h-5 w-5 text-muted-foreground" aria-hidden />
          <div>
            <CardTitle className="text-base">{t(titleKey)}</CardTitle>
            <CardDescription>{t("placeholder.comingSoon")}</CardDescription>
          </div>
        </CardHeader>
        <CardContent />
      </Card>
    </div>
  );
}
