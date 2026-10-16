import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { security, type ApiToken, type ApiTokenCreated } from "@/api/security";
import { ConfirmDialog, CopyButton, Field, Mono, useErrorMessage, useToastError } from "@/components";
import { ApiTokenList } from "@/components/ApiTokenList";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useMe } from "@/lib/session";

export const apiTokenKeys = { mine: ["me", "api-tokens"] as const };

type Expiry = "30" | "90" | "365" | "never";

function CreateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useErrorMessage();
  const [name, setName] = useState("");
  const [scope, setScope] = useState<"read" | "write">("read");
  const [expiry, setExpiry] = useState<Expiry>("90");
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<ApiTokenCreated | null>(null);
  useEffect(() => {
    if (open) {
      setName("");
      setScope("read");
      setExpiry("90");
      setError(null);
      setCreated(null);
    }
  }, [open]);
  const create = useMutation({
    mutationFn: () =>
      security.createApiToken({ name: name.trim(), scope, expires_in_days: expiry === "never" ? null : Number(expiry) }),
    onSuccess: (res) => {
      setCreated(res);
      void qc.invalidateQueries({ queryKey: apiTokenKeys.mine });
    },
    onError: (e) => setError(errMsg(e)),
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        {created ? (
          <div className="min-w-0 space-y-3">
            <DialogHeader>
              <DialogTitle>{t("apiTokens.createdTitle", { name: created.api_token.name })}</DialogTitle>
              <DialogDescription>{t("apiTokens.createdHint")}</DialogDescription>
            </DialogHeader>
            <div className="flex items-center gap-2 rounded-md border bg-muted/40 p-2">
              <Mono className="min-w-0 flex-1 break-all text-sm">{created.token}</Mono>
              <CopyButton value={created.token} />
            </div>
            <p role="note" className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-xs text-amber-800 dark:text-amber-300">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{t("apiTokens.shownOnce")}</span>
            </p>
            <DialogFooter>
              <Button type="button" onClick={() => onOpenChange(false)}>
                {t("apiTokens.done")}
              </Button>
            </DialogFooter>
          </div>
        ) : (
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              setError(null);
              create.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>{t("apiTokens.createTitle")}</DialogTitle>
              <DialogDescription>{t("apiTokens.createHint")}</DialogDescription>
            </DialogHeader>
            <Field label={t("apiTokens.name")} htmlFor="at-name" hint={t("apiTokens.nameHint")}>
              <Input id="at-name" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />
            </Field>
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t("apiTokens.scope")} htmlFor="at-scope">
                <Select value={scope} onValueChange={(v) => setScope(v as "read" | "write")}>
                  <SelectTrigger id="at-scope">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="read">{t("apiTokens.scope_read")}</SelectItem>
                    <SelectItem value="write">{t("apiTokens.scope_write")}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t("apiTokens.expiry")} htmlFor="at-expiry">
                <Select value={expiry} onValueChange={(v) => setExpiry(v as Expiry)}>
                  <SelectTrigger id="at-expiry">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(["30", "90", "365"] as const).map((d) => (
                      <SelectItem key={d} value={d}>
                        {t("apiTokens.days", { count: Number(d) })}
                      </SelectItem>
                    ))}
                    <SelectItem value="never">{t("apiTokens.noExpiry")}</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <p className="text-xs text-muted-foreground">{t(scope === "write" ? "apiTokens.scopeWriteHint" : "apiTokens.scopeReadHint")}</p>
            {error && (
              <p role="alert" className="text-sm text-destructive">
                {error}
              </p>
            )}
            <DialogFooter className="gap-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" disabled={create.isPending || !name.trim()}>
                {t("apiTokens.create")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** "API tokens" card of My profile: list, create (secret shown once), revoke. */
export function ApiTokensSection() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const me = useMe();
  // Until a forced 2FA enrolment is done the API answers 403; skip the request.
  const blocked = me.must_enroll_totp;
  const list = useQuery({ queryKey: apiTokenKeys.mine, queryFn: security.apiTokens, enabled: !blocked });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<ApiToken | null>(null);
  const revoke = useMutation({
    mutationFn: (id: number) => security.revokeApiToken(id),
    onSuccess: () => {
      toast.success(t("apiTokens.revoked"));
      setRevoking(null);
      void qc.invalidateQueries({ queryKey: apiTokenKeys.mine });
    },
    onError: toastError,
  });
  return (
    <div className="space-y-3">
      <ApiTokenList
        tokens={list.data?.items}
        isLoading={list.isLoading}
        actions={(tok) => (
          <Button size="icon" variant="ghost" className="h-7 w-7 text-destructive" onClick={() => setRevoking(tok)} aria-label={t("apiTokens.revoke")}>
            <Trash2 />
          </Button>
        )}
      />
      <Button size="sm" variant="outline" disabled={blocked} onClick={() => setCreating(true)}>
        <Plus />
        {t("apiTokens.add")}
      </Button>
      <p className="text-xs text-muted-foreground">{t("apiTokens.docsNote")}</p>
      <CreateDialog open={creating} onOpenChange={setCreating} />
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={t("apiTokens.revokeTitle", { name: revoking?.name ?? "" })}
        description={t("apiTokens.revokeDescription")}
        confirmLabel={t("apiTokens.revoke")}
        destructive
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking.id)}
      />
    </div>
  );
}
