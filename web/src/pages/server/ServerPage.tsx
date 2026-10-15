import { useQuery } from "@tanstack/react-query";
import { Activity, Radio, Server, SlidersHorizontal, TerminalSquare, View } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { Schemas } from "@/api/client";
import { serverApi } from "@/api/system";
import { ErrorState, Mono, PageHeader, RelativeTime } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton, Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/misc";
import { useTableSearch } from "@/lib/use-table";
import { cn } from "@/lib/utils";
import { Console } from "@/pages/server/Console";
import { ServerSettingsForm } from "@/pages/server/ServerSettingsForm";
import { Viewers } from "@/pages/server/Viewers";

function HealthCard({ title, icon: Icon, h }: { title: string; icon: typeof Server; h: Schemas["ComponentHealth"] }) {
  const { t } = useTranslation();
  const tone = h.status === "up" ? "bg-emerald-500" : h.status === "down" ? "bg-red-500" : "bg-zinc-400";
  return (
    <Card>
      <CardContent className="flex items-start gap-3 p-4">
        <div className="rounded-md bg-muted p-2">
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-center justify-between gap-2">
            <span className="font-medium">{title}</span>
            <span className="flex items-center gap-1.5 text-sm">
              <span className={cn("h-2 w-2 rounded-full", tone)} />
              {t(`health.${h.status}`)}
            </span>
          </div>
          <div className="mt-1 space-y-0.5 text-xs text-muted-foreground">
            {h.address && <Mono className="block">{h.address}</Mono>}
            <div>
              {h.latency_ms != null && `${h.latency_ms} ms · `}
              {t("health.checked")} <RelativeTime iso={h.checked_at} />
            </div>
            {h.up_since && h.status === "up" && (
              <div>
                {t("health.upSince")} <RelativeTime iso={h.up_since} />
              </div>
            )}
            {h.error && <div className="text-destructive">{h.error}</div>}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

export function ServerPage() {
  const { t } = useTranslation();
  const table = useTableSearch({});
  const tab = typeof table.raw.tab === "string" ? table.raw.tab : "settings";
  const health = useQuery({ queryKey: ["server", "health"], queryFn: serverApi.health, refetchInterval: 30_000 });
  const h = health.data;

  return (
    <div className="space-y-5">
      <PageHeader title={t("nav.server")} description={t("server.description")} />
      {health.isError ? (
        <ErrorState error={health.error} onRetry={() => void health.refetch()} />
      ) : !h ? (
        <div className="grid gap-3 md:grid-cols-3">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-28" />
          ))}
        </div>
      ) : (
        <div className="grid gap-3 md:grid-cols-3">
          <HealthCard title={t("health.hbbs")} icon={Server} h={h.hbbs} />
          <HealthCard title={t("health.hbbr")} icon={Radio} h={h.hbbr} />
          <Card>
            <CardContent className="flex items-start gap-3 p-4">
              <div className="rounded-md bg-muted p-2">
                <Activity className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-medium">{t("health.presence")}</span>
                  <Badge variant={h.presence.source === "hbbs" ? "success" : "outline"}>{t(`health.source.${h.presence.source}`)}</Badge>
                </div>
                <div className="mt-1 space-y-0.5 text-xs text-muted-foreground">
                  <div>{t("health.onlineDevices", { count: h.presence.devices_online })}</div>
                  {h.presence.last_poll_at && (
                    <div>
                      {t("health.lastPoll")} <RelativeTime iso={h.presence.last_poll_at} />
                      {h.presence.poll_duration_ms != null && ` · ${h.presence.poll_duration_ms} ms`}
                    </div>
                  )}
                  {h.presence.source === "heartbeat" && <div className="text-amber-600">{t("health.fallbackHint")}</div>}
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      <Tabs value={tab} onValueChange={(v) => table.setSearch({ tab: v === "settings" ? undefined : v, view: undefined })}>
        <TabsList>
          <TabsTrigger value="settings">
            <SlidersHorizontal />
            {t("server.tabs.settings")}
          </TabsTrigger>
          <TabsTrigger value="viewers">
            <View />
            {t("server.tabs.viewers")}
          </TabsTrigger>
          <TabsTrigger value="console">
            <TerminalSquare />
            {t("server.tabs.console")}
          </TabsTrigger>
        </TabsList>
        <TabsContent value="settings">{tab === "settings" && <ServerSettingsForm />}</TabsContent>
        <TabsContent value="viewers">{tab === "viewers" && <Viewers />}</TabsContent>
        <TabsContent value="console">{tab === "console" && <Console />}</TabsContent>
      </Tabs>
    </div>
  );
}
