import { useQuery } from "@tanstack/react-query";
import {
  ArrowUpCircle,
  BookOpen,
  Bug,
  CheckCircle2,
  Cpu,
  ExternalLink,
  Github,
  Info,
  MonitorCog,
  Scale,
  Server,
  Tag,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { aboutQuery, type About } from "@/api/system";
import { ErrorState, KeyValue, Mono, PageHeader, RelativeTime } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { formatDateTime, formatUptime } from "@/lib/format";
import { useMe } from "@/lib/session";
import { HealthRow } from "@/pages/DashboardPage";

const PANEL_REPO = "https://github.com/bropines/rustdesk-panel";
const SERVER_FORK = "https://github.com/bropines/rustdesk-server";
const UPSTREAM = "https://github.com/rustdesk/rustdesk";

const LINKS: { key: string; href: string; icon: LucideIcon }[] = [
  { key: "repo", href: PANEL_REPO, icon: Github },
  { key: "releases", href: `${PANEL_REPO}/releases`, icon: Tag },
  { key: "issues", href: `${PANEL_REPO}/issues`, icon: Bug },
  { key: "docs", href: `${PANEL_REPO}/blob/main/docs/deploy.md`, icon: BookOpen },
  { key: "serverFork", href: SERVER_FORK, icon: Server },
  { key: "upstream", href: UPSTREAM, icon: MonitorCog },
];

const isSha = (s: string | null | undefined): s is string => !!s && /^[0-9a-f]{7,40}$/i.test(s);

function Ext({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 text-primary hover:underline">
      {children}
      <ExternalLink className="h-3 w-3 shrink-0" aria-hidden />
    </a>
  );
}

function CardIconTitle({ icon: Icon, children, aside }: { icon: LucideIcon; children: ReactNode; aside?: ReactNode }) {
  return (
    <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
      <CardTitle className="flex items-center gap-2 text-base">
        <Icon className="h-4 w-4" aria-hidden /> {children}
      </CardTitle>
      {aside}
    </CardHeader>
  );
}

function UpdateCard({ update }: { update: About["update"] }) {
  const { t } = useTranslation();
  const available = update.status === "update_available";
  return (
    <Card className={available ? "border-primary/50" : undefined}>
      <CardIconTitle
        icon={available ? ArrowUpCircle : CheckCircle2}
        aside={available ? <Badge>{t("about.updates.badge")}</Badge> : undefined}
      >
        {t("about.updates.title")}
      </CardIconTitle>
      <CardContent className="space-y-3 text-sm">
        <p data-testid="update-status" className={update.status === "error" ? "text-amber-600 dark:text-amber-400" : undefined}>
          {t(`about.updates.${update.status}`, { latest: update.latest ?? "" })}
        </p>
        {update.error && <p className="break-words text-xs text-muted-foreground">{update.error}</p>}
        {(update.latest || update.checked_at) && (
          <KeyValue
            items={[
              [
                t("about.updates.latest"),
                update.latest ? (
                  update.latest_url ? (
                    <Ext href={update.latest_url}>
                      <Mono>{update.latest}</Mono>
                    </Ext>
                  ) : (
                    <Mono>{update.latest}</Mono>
                  )
                ) : null,
              ],
              [t("about.updates.checked"), <RelativeTime key="c" iso={update.checked_at} />],
            ]}
          />
        )}
        {available && update.latest_url && (
          <Ext href={update.latest_url}>{t("about.updates.releaseNotes")}</Ext>
        )}
      </CardContent>
    </Card>
  );
}

export function AboutPage() {
  const { t, i18n } = useTranslation();
  const me = useMe();
  const isAdmin = me.user.role === "admin";
  const q = useQuery(aboutQuery);
  const d = q.data;

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;

  const commit = d?.version.commit;
  const hbbRepo = d?.hbb.repo;
  const hbbRef = d?.hbb.ref;
  const refHref = hbbRepo?.startsWith("https://github.com/") && isSha(hbbRef) ? `${hbbRepo}/tree/${hbbRef}` : null;

  return (
    <div>
      <PageHeader title={t("about.title")} description={t("about.subtitle")} />
      {!d ? (
        <div className="grid gap-4 lg:grid-cols-2">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-44" />
          ))}
        </div>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card>
            <CardIconTitle icon={Info}>{d.name}</CardIconTitle>
            <CardContent className="space-y-4">
              <p className="text-sm text-muted-foreground">{t("about.description")}</p>
              <KeyValue
                items={[
                  [
                    t("about.version"),
                    <span key="v" data-testid="panel-version">
                      <Mono>{d.version.panel}</Mono>
                      {d.version.panel === "dev" && <span className="text-muted-foreground"> · {t("about.devBuild")}</span>}
                    </span>,
                  ],
                  [
                    t("about.commit"),
                    commit ? (
                      isSha(commit) ? (
                        <Ext href={`${PANEL_REPO}/commit/${commit}`}>
                          <Mono>{commit.slice(0, 12)}</Mono>
                        </Ext>
                      ) : (
                        <Mono>{commit}</Mono>
                      )
                    ) : null,
                  ],
                  [
                    t("about.buildDate"),
                    d.version.build_date
                      ? Number.isNaN(Date.parse(d.version.build_date))
                        ? d.version.build_date
                        : formatDateTime(d.version.build_date, i18n.language)
                      : null,
                  ],
                  [t("about.goVersion"), d.version.go ? <Mono>{d.version.go}</Mono> : null],
                ]}
              />
            </CardContent>
          </Card>

          <UpdateCard update={d.update} />

          <Card>
            <CardIconTitle icon={Server}>{t("about.hbbTitle")}</CardIconTitle>
            <CardContent className="space-y-3">
              {hbbRepo || hbbRef ? (
                <KeyValue
                  items={[
                    [t("about.hbbRepo"), hbbRepo ? <Ext href={hbbRepo}>{hbbRepo.replace(/^https:\/\//, "")}</Ext> : null],
                    [
                      t("about.hbbRef"),
                      hbbRef ? (
                        refHref ? (
                          <Ext href={refHref}>
                            <Mono>{hbbRef.slice(0, 12)}</Mono>
                          </Ext>
                        ) : (
                          <Mono>{hbbRef}</Mono>
                        )
                      ) : null,
                    ],
                  ]}
                />
              ) : (
                <p className="text-sm text-muted-foreground">{t("about.hbbNone")}</p>
              )}
              {d.server && (
                <div className="divide-y border-t">
                  <HealthRow name={t("health.hbbs")} h={d.server.health.hbbs} />
                  <HealthRow name={t("health.hbbr")} h={d.server.health.hbbr} />
                </div>
              )}
            </CardContent>
          </Card>

          {isAdmin && d.server && (
            <Card>
              <CardIconTitle icon={Cpu}>{t("about.serverTitle")}</CardIconTitle>
              <CardContent>
                <KeyValue
                  items={[
                    [t("about.uptime"), <span key="u" className="tabular-nums">{formatUptime(d.server.uptime_s)}</span>],
                    [t("about.startedAt"), formatDateTime(d.server.started_at, i18n.language)],
                    [
                      t("about.schema"),
                      d.server.schema_version != null
                        ? t("about.schemaValue", { current: d.server.schema_version, latest: d.server.schema_latest ?? "—" })
                        : null,
                    ],
                    [t("about.platform"), <Mono key="p">{`${d.server.os}/${d.server.arch}`}</Mono>],
                  ]}
                />
              </CardContent>
            </Card>
          )}

          <Card>
            <CardIconTitle icon={BookOpen}>{t("about.links.title")}</CardIconTitle>
            <CardContent>
              <ul className="grid gap-2 text-sm sm:grid-cols-2">
                {LINKS.map(({ key, href, icon: Icon }) => (
                  <li key={key} className="flex items-center gap-2">
                    <Icon className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
                    <Ext href={href}>{t(`about.links.${key}`)}</Ext>
                  </li>
                ))}
              </ul>
            </CardContent>
          </Card>

          <Card>
            <CardIconTitle icon={Scale}>{t("about.licenses.title")}</CardIconTitle>
            <CardContent className="space-y-3 text-sm">
              <KeyValue
                items={[
                  [t("about.licenses.panel"), <Ext key="mit" href={`${PANEL_REPO}/blob/HEAD/LICENSE`}>MIT</Ext>],
                  [t("about.licenses.hbb"), <Ext key="agpl" href={`${SERVER_FORK}/blob/HEAD/LICENSE`}>AGPL-3.0</Ext>],
                ]}
              />
              <p className="text-xs text-muted-foreground">{t("about.licenses.note")}</p>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  );
}
