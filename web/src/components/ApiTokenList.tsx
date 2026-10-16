import { KeyRound } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { ApiToken } from "@/api/security";
import { Mono, RelativeTime } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/misc";

/** Personal API tokens as a bordered list; `actions` renders per-row buttons. */
export function ApiTokenList({
  tokens,
  isLoading,
  actions,
}: {
  tokens: ApiToken[] | undefined;
  isLoading?: boolean;
  actions?: (t: ApiToken) => ReactNode;
}) {
  const { t } = useTranslation();
  if (isLoading) return <Skeleton className="h-16" />;
  if (!tokens?.length) return <p className="text-sm text-muted-foreground">{t("apiTokens.none")}</p>;
  return (
    <ul className="divide-y rounded-md border text-sm">
      {tokens.map((tok) => (
        <li key={tok.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
          <KeyRound className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 truncate font-medium">{tok.name}</span>
          <Mono className="text-xs text-muted-foreground">{tok.prefix}…</Mono>
          <Badge variant={tok.scope === "write" ? "default" : "secondary"}>{t(`apiTokens.scope_${tok.scope}`)}</Badge>
          {tok.expired && <Badge variant="destructive">{t("apiTokens.expired")}</Badge>}
          <span className="flex-1" />
          <span className="text-xs text-muted-foreground">
            {t("apiTokens.created")} <RelativeTime iso={tok.created_at} />
          </span>
          <span className="text-xs text-muted-foreground">
            {t("apiTokens.lastUsed")} {tok.last_used_at ? <RelativeTime iso={tok.last_used_at} /> : t("apiTokens.never")}
            {tok.last_used_ip ? ` (${tok.last_used_ip})` : ""}
          </span>
          <span className="text-xs text-muted-foreground">
            {t("apiTokens.expires")} {tok.expires_at ? <RelativeTime iso={tok.expires_at} /> : t("apiTokens.noExpiry")}
          </span>
          {actions?.(tok)}
        </li>
      ))}
    </ul>
  );
}
