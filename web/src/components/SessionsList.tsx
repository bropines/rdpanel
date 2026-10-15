import { Globe, Laptop, LogOut } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { Schemas } from "@/api/client";
import { EmptyState, Mono, RelativeTime } from "@/components/common";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/misc";

/** Short browser/OS label from a user agent string. */
export function describeUserAgent(ua: string | null | undefined): string {
  if (!ua) return "—";
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : ua.split(/[\s/]/)[0] || "—";
  const os = /Windows/.test(ua)
    ? "Windows"
    : /Mac OS X|Macintosh/.test(ua)
      ? "macOS"
      : /Android/.test(ua)
        ? "Android"
        : /iPhone|iPad/.test(ua)
          ? "iOS"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return os ? `${browser} · ${os}` : browser;
}

export function SessionsList({
  sessions,
  isLoading,
  onRevoke,
  revoking,
}: {
  sessions: Schemas["Session"][] | undefined;
  isLoading: boolean;
  onRevoke: (s: Schemas["Session"]) => void;
  revoking?: number | null;
}) {
  const { t } = useTranslation();
  if (isLoading) return <Skeleton className="h-24" />;
  if (!sessions?.length) return <EmptyState title={t("sessions.empty")} className="py-6" />;
  return (
    <ul className="divide-y rounded-md border">
      {sessions.map((s) => (
        <li key={s.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 text-sm">
          {s.kind === "web" ? (
            <Globe className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          ) : (
            <Laptop className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">
                {s.kind === "web"
                  ? describeUserAgent(s.user_agent)
                  : `${t("sessions.client")}${s.client_platform ? ` · ${s.client_platform}` : ""}`}
              </span>
              {s.device_rustdesk_id && <Mono className="text-muted-foreground">{s.device_rustdesk_id}</Mono>}
              {s.current && <Badge variant="success">{t("sessions.current")}</Badge>}
            </div>
            <div className="text-xs text-muted-foreground">
              {s.ip && <Mono>{s.ip}</Mono>}
              {s.ip && " · "}
              {t("sessions.lastUsed")} <RelativeTime iso={s.last_used_at ?? s.created_at} /> · {t("sessions.expires")}{" "}
              <RelativeTime iso={s.expires_at} />
            </div>
          </div>
          {!s.current && (
            <Button variant="ghost" size="sm" onClick={() => onRevoke(s)} disabled={revoking === s.id}>
              <LogOut />
              {t("sessions.revoke")}
            </Button>
          )}
        </li>
      ))}
    </ul>
  );
}
