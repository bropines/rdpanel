import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { LogOut } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { security } from "@/api/security";
import { auditApi } from "@/api/system";
import { ConfirmDialog, EmptyState, ErrorState, Mono, PageHeader, RelativeTime, useToastError } from "@/components";
import { SessionsList } from "@/components/SessionsList";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";

export function MySessionsPage() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const sessions = useQuery({ queryKey: ["me", "sessions"], queryFn: security.sessions });
  const logins = useQuery({ queryKey: ["audit", "logins", "mine"], queryFn: () => auditApi.logins({ page_size: 15 }) });
  const [confirm, setConfirm] = useState(false);
  const revoke = useMutation({
    mutationFn: (id: number) => security.revokeSession(id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["me", "sessions"] }),
    onError: toastError,
  });
  const revokeOthers = useMutation({
    mutationFn: security.revokeOtherSessions,
    onSuccess: (r) => {
      toast.success(t("sessions.revokedCount", { count: r.revoked }));
      setConfirm(false);
      void qc.invalidateQueries({ queryKey: ["me", "sessions"] });
    },
    onError: toastError,
  });
  const others = (sessions.data?.items ?? []).filter((s) => !s.current).length;

  return (
    <div className="max-w-4xl space-y-4">
      <PageHeader
        title={t("nav.mySessions")}
        description={t("sessions.description")}
        actions={
          <Button variant="outline" size="sm" disabled={others === 0} onClick={() => setConfirm(true)}>
            <LogOut />
            {t("sessions.revokeOthers")}
          </Button>
        }
      />
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t("sessions.active")}</CardTitle>
        </CardHeader>
        <CardContent>
          {sessions.isError ? (
            <ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} />
          ) : (
            <SessionsList
              sessions={sessions.data?.items}
              isLoading={sessions.isLoading}
              onRevoke={(s) => revoke.mutate(s.id)}
              revoking={revoke.isPending ? (revoke.variables ?? null) : null}
            />
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">{t("sessions.recentLogins")}</CardTitle>
        </CardHeader>
        <CardContent>
          {logins.isLoading ? (
            <Skeleton className="h-24" />
          ) : !logins.data?.items.length ? (
            <EmptyState title={t("audit.empty")} className="py-6" />
          ) : (
            <ul className="divide-y rounded-md border text-sm">
              {logins.data.items.map((l) => (
                <li key={l.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2">
                  {l.result === "success" ? <Badge variant="success">{t("audit.success")}</Badge> : <Badge variant="destructive">{t("audit.failure")}</Badge>}
                  <span>
                    {t(`audit.methods.${l.method}`)} · {t(`audit.clientKind.${l.client_kind}`)}
                    {l.platform && ` · ${l.platform}`}
                  </span>
                  {l.ip && <Mono className="text-muted-foreground">{l.ip}</Mono>}
                  <span className="ml-auto text-xs text-muted-foreground">
                    <RelativeTime iso={l.created_at} />
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t("sessions.revokeOthersTitle")}
        description={t("sessions.revokeOthersDescription", { count: others })}
        confirmLabel={t("sessions.revokeOthers")}
        destructive
        pending={revokeOthers.isPending}
        onConfirm={() => revokeOthers.mutate()}
      />
    </div>
  );
}
