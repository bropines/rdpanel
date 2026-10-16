import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fingerprint, Pencil, Plus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { security, type Passkey } from "@/api/security";
import { ConfirmDialog, Field, useErrorMessage, useToastError } from "@/components";
import { PasskeyList } from "@/components/PasskeyList";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useMe } from "@/lib/session";
import { passkeysSupported, webauthnErrorKey } from "@/lib/webauthn";

export const passkeyKeys = { mine: ["me", "passkeys"] as const, overview: ["me", "security"] as const };

/** Suggests a passkey name from the browser's platform ("Windows", "macOS", ...). */
export function defaultPasskeyName(ua: string = typeof navigator === "undefined" ? "" : navigator.userAgent): string {
  if (/iPhone|iPad/.test(ua)) return "iPhone / iPad";
  if (/Android/.test(ua)) return "Android";
  if (/Mac OS X|Macintosh/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows";
  if (/Linux/.test(ua)) return "Linux";
  return "";
}

function AddDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useErrorMessage();
  const me = useMe();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const needsPassword = me.user.has_password;
  const needsCode = !me.user.has_password && me.user.totp_enabled;
  useEffect(() => {
    if (open) {
      setName(defaultPasskeyName());
      setPassword("");
      setCode("");
      setError(null);
    }
  }, [open]);
  const add = useMutation({
    mutationFn: () => security.addPasskey({ password: password || undefined, code: code || undefined }, name.trim()),
    onSuccess: () => {
      toast.success(t("passkeys.addedToast"));
      void qc.invalidateQueries({ queryKey: passkeyKeys.mine });
      void qc.invalidateQueries({ queryKey: passkeyKeys.overview });
      onOpenChange(false);
    },
    onError: (e) => {
      const key = webauthnErrorKey(e);
      setError(key ? t(key) : errMsg(e));
    },
  });
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            add.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("passkeys.addTitle")}</DialogTitle>
            <DialogDescription>{t("passkeys.addHint")}</DialogDescription>
          </DialogHeader>
          <Field label={t("passkeys.name")} htmlFor="pk-name" hint={t("passkeys.nameHint")}>
            <Input id="pk-name" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          {needsPassword && (
            <>
              <input type="text" autoComplete="username" value={me.user.username} readOnly hidden />
              <Field label={t("security.currentPassword")} htmlFor="pk-pass">
                <Input id="pk-pass" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
            </>
          )}
          {needsCode && (
            <Field label={t("security.codeOrRecovery")} htmlFor="pk-code">
              <Input id="pk-code" autoComplete="one-time-code" className="font-mono" value={code} onChange={(e) => setCode(e.target.value.trim())} />
            </Field>
          )}
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={add.isPending || (needsPassword && !password) || (needsCode && !code)}>
              <Fingerprint />
              {t("passkeys.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RenameDialog({ passkey, onOpenChange }: { passkey: Passkey | null; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const [name, setName] = useState("");
  useEffect(() => setName(passkey?.name ?? ""), [passkey]);
  const rename = useMutation({
    mutationFn: () => security.renamePasskey(passkey!.id, name.trim()),
    onSuccess: () => {
      toast.success(t("common.saved"));
      void qc.invalidateQueries({ queryKey: passkeyKeys.mine });
      onOpenChange(false);
    },
    onError: toastError,
  });
  return (
    <Dialog open={passkey !== null} onOpenChange={onOpenChange}>
      <DialogContent closeLabel={t("common.close")}>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            rename.mutate();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("passkeys.renameTitle")}</DialogTitle>
            <DialogDescription>{t("passkeys.nameHint")}</DialogDescription>
          </DialogHeader>
          <Field label={t("passkeys.name")} htmlFor="pk-rename">
            <Input id="pk-rename" maxLength={64} value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={rename.isPending || !name.trim()}>
              {t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** "Passkeys" card of My profile: list, add (re-auth), rename, delete. */
export function PasskeysSection() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const toastError = useToastError();
  const overview = useQuery({ queryKey: passkeyKeys.overview, queryFn: security.overview });
  const list = useQuery({ queryKey: passkeyKeys.mine, queryFn: security.passkeys });
  const [adding, setAdding] = useState(false);
  const [renaming, setRenaming] = useState<Passkey | null>(null);
  const [deleting, setDeleting] = useState<Passkey | null>(null);
  const del = useMutation({
    mutationFn: (id: number) => security.deletePasskey(id),
    onSuccess: () => {
      toast.success(t("passkeys.removed"));
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: passkeyKeys.mine });
      void qc.invalidateQueries({ queryKey: passkeyKeys.overview });
    },
    onError: toastError,
  });
  const available = overview.data?.passkeys_available !== false;
  const supported = passkeysSupported();
  return (
    <div className="space-y-3">
      <PasskeyList
        passkeys={list.data?.items}
        isLoading={list.isLoading}
        actions={(p) => (
          <span className="flex gap-1">
            <Button size="icon" variant="ghost" className="h-7 w-7" onClick={() => setRenaming(p)} aria-label={t("passkeys.rename")}>
              <Pencil />
            </Button>
            <Button size="icon" variant="ghost" className="h-7 w-7 text-destructive" onClick={() => setDeleting(p)} aria-label={t("passkeys.remove")}>
              <Trash2 />
            </Button>
          </span>
        )}
      />
      {!available ? (
        <p className="text-sm text-muted-foreground">{t("passkeys.unavailable")}</p>
      ) : !supported ? (
        <p className="text-sm text-muted-foreground">{t("passkeys.unsupported")}</p>
      ) : (
        <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
          <Plus />
          {t("passkeys.add")}
        </Button>
      )}
      <p className="text-xs text-muted-foreground">{t("passkeys.clientNote")}</p>
      <AddDialog open={adding} onOpenChange={setAdding} />
      <RenameDialog passkey={renaming} onOpenChange={(o) => !o && setRenaming(null)} />
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t("passkeys.removeTitle", { name: deleting?.name ?? "" })}
        description={t("passkeys.removeDescription")}
        confirmLabel={t("passkeys.remove")}
        destructive
        pending={del.isPending}
        onConfirm={() => deleting && del.mutate(deleting.id)}
      />
    </div>
  );
}
