import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Link2, Plus, QrCode, Share2, Trash2 } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { shareKeys, shareLinksApi, type ShareLink, type ShareScope } from "@/api/share";
import { ConfirmDialog, CopyButton, EmptyState, ErrorState, Field, Mono, RelativeTime, useToastError } from "@/components";
import { QrCanvas } from "@/components/QrCanvas";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton, Tabs, TabsList, TabsTrigger } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { formatDateTime } from "@/lib/format";
import { useMe } from "@/lib/session";
import { cn } from "@/lib/utils";
import {
  buildShareInput,
  defaultShareForm,
  downloadsText,
  SHARE_DURATIONS,
  sortLinks,
  STATUS_VARIANT,
  type ShareForm,
  type ShareFormResult,
} from "./share-links";

type FieldErrors = Partial<Record<Extract<ShareFormResult, { ok: false }>["field"], string>>;

/** "Share this server" on the Install client page (share links on and permitted). */
export function ShareLinksCard({ host }: { host: string }) {
  const me = useMe();
  if (!me.can_share_links) return null;
  return <ShareLinks host={host} isAdmin={me.user.role === "admin"} />;
}

function ShareLinks({ host, isAdmin }: { host: string; isAdmin: boolean }) {
  const { t } = useTranslation();
  const [scope, setScope] = useState<ShareScope>("mine");
  const [shown, setShown] = useState<{ link: ShareLink; fresh: boolean } | null>(null);
  const q = useQuery({ queryKey: shareKeys.list(scope), queryFn: () => shareLinksApi.list(scope) });

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-base">
          <Share2 className="h-4 w-4" />
          {t("share.card.title")}
        </CardTitle>
        <CardDescription>{t("share.card.description", { host })}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <CreateForm onCreated={(link) => setShown({ link, fresh: true })} />
        {isAdmin && (
          <Tabs value={scope} onValueChange={(v) => setScope(v as ShareScope)}>
            <TabsList>
              <TabsTrigger value="mine">{t("share.card.mine")}</TabsTrigger>
              <TabsTrigger value="all">{t("share.card.all")}</TabsTrigger>
            </TabsList>
          </Tabs>
        )}
        {q.isError ? (
          <ErrorState error={q.error} onRetry={() => void q.refetch()} />
        ) : !q.data ? (
          <Skeleton className="h-24" />
        ) : q.data.items.length === 0 ? (
          <EmptyState icon={Link2} title={t("share.card.empty")} className="py-6" />
        ) : (
          <ul className="space-y-2">
            {sortLinks(q.data.items).map((l) => (
              <LinkRow key={l.id} link={l} showOwner={scope === "all"} onShow={() => setShown({ link: l, fresh: false })} />
            ))}
          </ul>
        )}
      </CardContent>
      <LinkDialog shown={shown} onClose={() => setShown(null)} />
    </Card>
  );
}

function CreateForm({ onCreated }: { onCreated: (l: ShareLink) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [f, setF] = useState<ShareForm>(() => defaultShareForm());
  const [errors, setErrors] = useState<FieldErrors>({});
  const set = (patch: Partial<ShareForm>) => setF((old) => ({ ...old, ...patch }));
  const temporary = f.expiry !== "permanent";

  const create = useMutation({
    mutationFn: shareLinksApi.create,
    onSuccess: (l) => {
      void qc.invalidateQueries({ queryKey: shareKeys.all });
      setF(defaultShareForm());
      setErrors({});
      onCreated(l);
    },
    onError: (e) => {
      if (e instanceof ApiError && e.code === "share_link_limit") toast.error(t("share.card.limit"));
      else if (e instanceof ApiError && e.fields?.expires_at) setErrors({ customDate: "share.card.errors.date" });
      else if (e instanceof ApiError && e.fields?.max_downloads) setErrors({ maxDownloads: "share.card.errors.downloads" });
      else toastError(e);
    },
  });

  const submit = () => {
    const r = buildShareInput(f);
    if (!r.ok) {
      setErrors({ [r.field]: r.error });
      return;
    }
    setErrors({});
    create.mutate(r.body);
  };

  return (
    <form
      className="space-y-3 rounded-md border bg-muted/20 p-3"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t("share.card.validity")}>
          <div className="flex rounded-md border bg-background p-0.5" role="radiogroup" aria-label={t("share.card.validity")}>
            {(["permanent", "temporary"] as const).map((k) => {
              const on = k === "permanent" ? !temporary : temporary;
              return (
                <button
                  key={k}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  className={cn(
                    "flex-1 rounded px-3 py-1.5 text-sm transition-colors",
                    on ? "bg-primary text-primary-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                  )}
                  onClick={() => set({ expiry: k === "permanent" ? "permanent" : temporary ? f.expiry : "7d" })}
                >
                  {t(`share.card.${k}`)}
                </button>
              );
            })}
          </div>
        </Field>
        {temporary && (
          <Field label={t("share.card.expiresIn")} htmlFor="share-expiry">
            <Select value={f.expiry} onValueChange={(v) => set({ expiry: v as ShareForm["expiry"] })}>
              <SelectTrigger id="share-expiry">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SHARE_DURATIONS.map((d) => (
                  <SelectItem key={d} value={d}>
                    {t(`share.card.durations.${d}`)}
                  </SelectItem>
                ))}
                <SelectItem value="custom">{t("share.card.durations.custom")}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
        {f.expiry === "custom" && (
          <Field label={t("share.card.customDate")} htmlFor="share-date" error={errors.customDate ? t(errors.customDate) : undefined}>
            <Input id="share-date" type="datetime-local" value={f.customDate} onChange={(e) => set({ customDate: e.target.value })} />
          </Field>
        )}
        <Field
          label={t("share.card.maxDownloads")}
          htmlFor="share-max"
          error={errors.maxDownloads ? t(errors.maxDownloads) : undefined}
          hint={t("share.card.maxDownloadsHint")}
        >
          <Input
            id="share-max"
            inputMode="numeric"
            placeholder={t("share.card.unlimited")}
            value={f.maxDownloads}
            onChange={(e) => set({ maxDownloads: e.target.value })}
          />
        </Field>
        <Field label={t("share.card.label")} htmlFor="share-label" error={errors.label ? t(errors.label) : undefined}>
          <Input id="share-label" maxLength={64} placeholder={t("share.card.labelPlaceholder")} value={f.label} onChange={(e) => set({ label: e.target.value })} />
        </Field>
      </div>
      <Button type="submit" disabled={create.isPending}>
        <Plus />
        {t("share.card.create")}
      </Button>
    </form>
  );
}

function LinkRow({ link, showOwner, onShow }: { link: ShareLink; showOwner: boolean; onShow: () => void }) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [confirm, setConfirm] = useState(false);
  const revoke = useMutation({
    mutationFn: () => shareLinksApi.revoke(link.id),
    onSuccess: () => {
      toast.success(t("share.card.revoked"));
      setConfirm(false);
      void qc.invalidateQueries({ queryKey: shareKeys.all });
    },
    onError: toastError,
  });
  const expiry = !link.expires_at
    ? t("share.card.never")
    : Date.parse(link.expires_at) > Date.now()
      ? t("share.card.expires", { time: formatDateTime(link.expires_at, i18n.language) })
      : t("share.card.expiredAt", { time: formatDateTime(link.expires_at, i18n.language) });

  return (
    <li className={cn("space-y-2 rounded-md border p-3", link.status !== "active" && "bg-muted/30")}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 truncate text-sm font-medium">{link.label || t("share.card.untitled")}</span>
        <Badge variant={STATUS_VARIANT[link.status]} title={link.status === "suspended" ? t("share.card.suspendedHint") : undefined}>
          {t(`share.card.status.${link.status}`)}
        </Badge>
        {showOwner && <span className="text-xs text-muted-foreground">{t("share.card.ownedBy", { name: link.owner.display_name || link.owner.username })}</span>}
        <div className="ml-auto flex items-center gap-0.5">
          {link.url && (
            <>
              <CopyButton value={link.url} label={t("share.card.copy")} />
              <Button type="button" variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" aria-label={t("share.card.qr")} title={t("share.card.qr")} onClick={onShow}>
                <QrCode />
              </Button>
              <Button asChild variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" aria-label={t("share.card.open")} title={t("share.card.open")}>
                <a href={link.url} target="_blank" rel="noreferrer noopener">
                  <ExternalLink />
                </a>
              </Button>
            </>
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-muted-foreground hover:text-destructive"
            aria-label={t("share.card.revoke")}
            title={t("share.card.revoke")}
            onClick={() => setConfirm(true)}
          >
            <Trash2 />
          </Button>
        </div>
      </div>
      {link.url ? <Mono className="block truncate text-xs text-muted-foreground">{link.url}</Mono> : <p className="text-xs text-muted-foreground">{t("share.card.noUrl")}</p>}
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>{expiry}</span>
        <span>
          {t("share.card.views")}: <span className="tabular-nums text-foreground">{link.views}</span>
        </span>
        <span>
          {t("share.card.downloads")}: <span className="tabular-nums text-foreground">{downloadsText(link)}</span>
        </span>
        <span className="inline-flex gap-1">
          {t("share.card.createdAt")}: <RelativeTime iso={link.created_at} />
        </span>
      </div>
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t("share.card.revokeTitle")}
        description={t("share.card.revokeDescription")}
        confirmLabel={t("share.card.revoke")}
        destructive
        pending={revoke.isPending}
        onConfirm={() => revoke.mutate()}
      />
    </li>
  );
}

function LinkDialog({ shown, onClose }: { shown: { link: ShareLink; fresh: boolean } | null; onClose: () => void }) {
  const { t } = useTranslation();
  const url = shown?.link.url;
  return (
    <Dialog open={!!shown} onOpenChange={(o) => !o && onClose()}>
      <DialogContent closeLabel={t("common.close")} className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{shown?.fresh ? t("share.card.created") : shown?.link.label || t("share.card.untitled")}</DialogTitle>
          <DialogDescription>{shown?.fresh ? t("share.card.createdHint") : t("share.card.qrHint")}</DialogDescription>
        </DialogHeader>
        {url && (
          <div className="space-y-3">
            <div className="flex justify-center">
              <QrCanvas value={url} size={208} label={t("share.card.qr")} />
            </div>
            <div className="flex items-center gap-1 rounded-md border bg-muted/40 p-2">
              <Mono className="min-w-0 flex-1 break-all text-xs">{url}</Mono>
              <CopyButton value={url} label={t("share.card.copy")} />
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
