import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Activity, BookUser, LogIn, Monitor, Plug, Server, Users, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { Schemas } from "@/api/client";
import { auditApi, dashboardApi } from "@/api/system";
import { ErrorState, Markdown, RelativeTime, Sparkline } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { formatNumber } from "@/lib/format";
import { displayName, useMe } from "@/lib/session";
import { cn } from "@/lib/utils";
import { ConnTypeBadge } from "@/pages/devices/DeviceDrawer";

function StatCard({
  icon: Icon,
  label,
  value,
  sub,
  series,
  to,
}: {
  icon: LucideIcon;
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  series?: number[];
  to?: string;
}) {
  const body = (
    <Card className={cn("h-full transition-colors", to && "hover:border-primary/40")}>
      <CardContent className="flex h-full flex-col gap-2 p-4">
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>{label}</span>
          <Icon className="h-4 w-4" aria-hidden />
        </div>
        <div className="text-2xl font-semibold tabular-nums tracking-tight">{value}</div>
        {sub && <div className="text-xs text-muted-foreground">{sub}</div>}
        {series && <Sparkline values={series} className="mt-auto" label={label} />}
      </CardContent>
    </Card>
  );
  return to ? (
    <Link to={to} className="block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
      {body}
    </Link>
  ) : (
    body
  );
}

function HealthRow({ name, h }: { name: string; h: Schemas["ComponentHealth"] }) {
  const { t } = useTranslation();
  const variant = h.status === "up" ? "success" : h.status === "down" ? "destructive" : "secondary";
  return (
    <div className="flex items-center justify-between gap-3 py-2">
      <div className="min-w-0">
        <div className="text-sm font-medium">{name}</div>
        <div className="truncate text-xs text-muted-foreground">
          {h.address ?? "—"}
          {h.latency_ms != null && ` · ${h.latency_ms} ms`}
          {h.error && ` · ${h.error}`}
        </div>
      </div>
      <div className="flex flex-col items-end gap-1">
        <Badge variant={variant}>{t(`health.${h.status}`)}</Badge>
        {h.up_since && h.status === "up" && (
          <span className="text-[11px] text-muted-foreground">
            {t("health.upSince")} <RelativeTime iso={h.up_since} />
          </span>
        )}
      </div>
    </div>
  );
}

export function DashboardPage() {
  const { t, i18n } = useTranslation();
  const me = useMe();
  const isAdmin = me.user.role === "admin";
  const q = useQuery({ queryKey: ["dashboard"], queryFn: dashboardApi.get, refetchInterval: 60_000 });
  const branding = useQuery({ queryKey: ["branding"], queryFn: dashboardApi.branding, staleTime: 300_000 });
  const n = (v: number) => formatNumber(v, i18n.language);
  const d = q.data;

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold tracking-tight md:text-2xl">
          {t("dashboard.greeting", { name: displayName(me) })}
        </h1>
        <p className="text-sm text-muted-foreground">{t("dashboard.subtitle")}</p>
      </div>

      {q.isError ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
          {!d ? (
            Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-32" />)
          ) : (
            <>
              <StatCard
                icon={Monitor}
                label={t("dashboard.onlineNow")}
                value={
                  <span>
                    <span className="text-emerald-600 dark:text-emerald-400">{n(d.devices_online)}</span>
                    <span className="text-base font-normal text-muted-foreground"> / {n(d.devices_total)}</span>
                  </span>
                }
                sub={t("dashboard.devicesTotal", { count: d.devices_total })}
                series={d.series.online.map((p) => p.v)}
                to={isAdmin ? "/devices" : "/my/devices"}
              />
              <StatCard
                icon={Plug}
                label={t("dashboard.connections24h")}
                value={n(d.connections_24h)}
                sub={t("dashboard.activeConnections", { count: d.active_connections })}
                series={d.series.connections.map((p) => p.v)}
                to={isAdmin ? "/audit" : undefined}
              />
              <StatCard
                icon={LogIn}
                label={t("dashboard.logins24h")}
                value={n(d.logins_24h)}
                sub={
                  d.failed_logins_24h > 0 ? (
                    <span className="text-amber-600 dark:text-amber-400">
                      {t("dashboard.failedLogins", { count: d.failed_logins_24h })}
                    </span>
                  ) : (
                    t("dashboard.noFailedLogins")
                  )
                }
                series={d.series.logins.map((p) => p.v)}
              />
              <StatCard
                icon={isAdmin ? Users : BookUser}
                label={isAdmin ? t("dashboard.users") : t("dashboard.addressBooks")}
                value={isAdmin ? n(d.users_total) : n(d.address_books_total)}
                sub={isAdmin ? t("dashboard.addressBooksCount", { count: d.address_books_total }) : undefined}
                to={isAdmin ? "/users" : "/my/address-books"}
              />
            </>
          )}
        </div>
      )}

      <div className="grid gap-4 lg:grid-cols-3">
        {isAdmin && (
          <Card>
            <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle className="flex items-center gap-2 text-base">
                <Server className="h-4 w-4" /> {t("dashboard.serverHealth")}
              </CardTitle>
              <Link to="/server" className="text-xs text-primary hover:underline">
                {t("common.details")}
              </Link>
            </CardHeader>
            <CardContent className="divide-y">
              {!d?.health ? (
                <Skeleton className="h-24" />
              ) : (
                <>
                  <HealthRow name={t("health.hbbs")} h={d.health.hbbs} />
                  <HealthRow name={t("health.hbbr")} h={d.health.hbbr} />
                  <div className="flex items-center justify-between py-2 text-sm">
                    <span className="flex items-center gap-2 text-muted-foreground">
                      <Activity className="h-4 w-4" /> {t("health.presenceSource")}
                    </span>
                    <span>
                      {t(`health.source.${d.health.presence.source}`)}
                      {d.health.presence.last_poll_at && (
                        <span className="text-muted-foreground">
                          {" · "}
                          <RelativeTime iso={d.health.presence.last_poll_at} />
                        </span>
                      )}
                    </span>
                  </div>
                </>
              )}
            </CardContent>
          </Card>
        )}

        <Card className={cn(isAdmin ? "lg:col-span-2" : "lg:col-span-3")}>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">{t("dashboard.welcome")}</CardTitle>
          </CardHeader>
          <CardContent>
            {branding.data?.welcome_markdown ? (
              <Markdown source={branding.data.welcome_markdown} vars={{ username: displayName(me) }} />
            ) : (
              <p className="text-sm text-muted-foreground">{t("dashboard.welcomeEmpty")}</p>
            )}
            {d && (
              <p className="mt-4 border-t pt-3 text-xs text-muted-foreground">
                {t("dashboard.version", { version: d.version.panel })}
                {d.version.commit && ` · ${d.version.commit.slice(0, 7)}`}
                {d.version.build_date && ` · ${d.version.build_date}`}
              </p>
            )}
          </CardContent>
        </Card>
      </div>

      <RecentConnections isAdmin={isAdmin} />
    </div>
  );
}

function RecentConnections({ isAdmin }: { isAdmin: boolean }) {
  const { t } = useTranslation();
  const q = useQuery({
    queryKey: ["audit", "connections", "recent"],
    queryFn: () => auditApi.connections({ page_size: 6, sort: "-started_at" }),
    refetchInterval: 60_000,
  });
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="flex items-center gap-2 text-base">
          <Plug className="h-4 w-4" /> {t("dashboard.recentConnections")}
        </CardTitle>
        {isAdmin && (
          <Link to="/audit" className="text-xs text-primary hover:underline">
            {t("dashboard.viewAll")}
          </Link>
        )}
      </CardHeader>
      <CardContent>
        {q.isLoading ? (
          <Skeleton className="h-32" />
        ) : !q.data?.items.length ? (
          <p className="py-4 text-sm text-muted-foreground">{t("audit.empty")}</p>
        ) : (
          <ul className="divide-y text-sm">
            {q.data.items.map((c) => (
              <li key={c.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
                <span className="min-w-0 flex-1 truncate">
                  <span className="font-medium">{c.controller_name || c.controller_rustdesk_id || "—"}</span>
                  <span className="text-muted-foreground"> → </span>
                  <span className="font-medium">{c.device_alias || c.device_rustdesk_id}</span>
                </span>
                <ConnTypeBadge type={c.conn_type} />
                <span className="w-28 text-right text-xs text-muted-foreground">
                  {c.ended_at ? <RelativeTime iso={c.started_at} /> : <Badge variant="success">{t("audit.active")}</Badge>}
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
