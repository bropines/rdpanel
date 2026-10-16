import { AlertTriangle, Fingerprint } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Passkey } from "@/api/security";
import { RelativeTime } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/misc";

/** Passkeys as a bordered list; `actions` renders per-row buttons. */
export function PasskeyList({
  passkeys,
  isLoading,
  actions,
}: {
  passkeys: Passkey[] | undefined;
  isLoading?: boolean;
  actions?: (p: Passkey) => ReactNode;
}) {
  const { t } = useTranslation();
  if (isLoading) return <Skeleton className="h-16" />;
  if (!passkeys?.length) return <p className="text-sm text-muted-foreground">{t("passkeys.none")}</p>;
  return (
    <ul className="divide-y rounded-md border text-sm">
      {passkeys.map((p) => (
        <li key={p.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
          <Fingerprint className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="min-w-0 flex-1 truncate font-medium">{p.name}</span>
          {p.clone_warning && (
            <Badge variant="destructive" className="gap-1" title={t("passkeys.cloneWarningHint")}>
              <AlertTriangle className="h-3 w-3" />
              {t("passkeys.cloneWarning")}
            </Badge>
          )}
          {p.synced && <Badge variant="secondary">{t("passkeys.synced")}</Badge>}
          <span className="text-xs text-muted-foreground">
            {t("passkeys.added")} <RelativeTime iso={p.created_at} />
          </span>
          <span className="text-xs text-muted-foreground">
            {t("passkeys.lastUsed")} {p.last_used_at ? <RelativeTime iso={p.last_used_at} /> : t("passkeys.never")}
          </span>
          {actions?.(p)}
        </li>
      ))}
    </ul>
  );
}
