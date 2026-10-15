import { useMutation, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { Copy, Download, KeyRound, Loader2, ShieldCheck, ShieldOff } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { meQueryKey } from "@/api/auth";
import { saveBlob } from "@/api/client";
import { security, type TotpEnrollment } from "@/api/security";
import { copyText, CopyButton, Field, Mono, useErrorMessage } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/misc";
import { useMe } from "@/lib/session";

export const TOTP_CODE_RE = /^\d{6}$/;

/** Draws the otpauth:// URI as a QR code on a canvas (no data: URLs, CSP-friendly). */
export function QrCanvas({ value, size = 192 }: { value: string; size?: number }) {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    void QRCode.toCanvas(ref.current, value, { width: size, margin: 1, errorCorrectionLevel: "M" }).catch(() => undefined);
  }, [value, size]);
  return <canvas ref={ref} width={size} height={size} className="rounded-md border bg-white" aria-label="QR" role="img" />;
}

/** Groups a base32 secret in blocks of four for manual entry. */
export function formatSecret(secret: string): string {
  return secret.replace(/\s/g, "").replace(/(.{4})/g, "$1 ").trim();
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation();
  const [saved, setSaved] = useState(false);
  const text = codes.join("\n");
  return (
    <div className="space-y-4">
      <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">
        {t("security.recoveryOnce")}
      </div>
      <ol className="grid grid-cols-2 gap-2 rounded-md border bg-muted/40 p-3 font-mono text-sm">
        {codes.map((c) => (
          <li key={c} className="tabular-nums">
            {c}
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={async () => {
            if (await copyText(text)) toast.success(t("common.copied"));
          }}
        >
          <Copy />
          {t("common.copy")}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={() => saveBlob(new Blob([`${text}\n`], { type: "text/plain" }), "rustdesk-panel-recovery-codes.txt")}>
          <Download />
          {t("security.download")}
        </Button>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <Checkbox checked={saved} onCheckedChange={(v) => setSaved(!!v)} />
        {t("security.savedCodes")}
      </label>
      <div className="flex justify-end">
        <Button disabled={!saved} onClick={onDone}>
          {t("common.done")}
        </Button>
      </div>
    </div>
  );
}

function EnrollDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useErrorMessage();
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null);
  const [code, setCode] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const start = useMutation({ mutationFn: security.enrollTotp, onSuccess: setEnrollment, onError: (e) => setError(errMsg(e)) });
  const confirm = useMutation({
    mutationFn: (c: string) => security.confirmTotp(c),
    onSuccess: (r) => {
      setCodes(r.recovery_codes);
      void qc.invalidateQueries({ queryKey: meQueryKey });
    },
    onError: (e) => setError(errMsg(e)),
  });

  useEffect(() => {
    if (open) {
      setEnrollment(null);
      setCodes(null);
      setCode("");
      setError(null);
      start.mutate();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  return (
    <Dialog open={open} onOpenChange={(o) => !codes && onOpenChange(o)}>
      <DialogContent className="max-w-lg" closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{codes ? t("security.recoveryTitle") : t("security.enrollTitle")}</DialogTitle>
          <DialogDescription>{codes ? t("security.recoveryHint") : t("security.enrollHint")}</DialogDescription>
        </DialogHeader>
        {codes ? (
          <RecoveryCodes codes={codes} onDone={() => onOpenChange(false)} />
        ) : !enrollment ? (
          <div className="flex h-48 items-center justify-center">
            {error ? <p className="text-sm text-destructive">{error}</p> : <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />}
          </div>
        ) : (
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              setError(null);
              if (!TOTP_CODE_RE.test(code)) return setError(t("security.codeFormat"));
              confirm.mutate(code);
            }}
          >
            <div className="flex flex-col items-center gap-3 sm:flex-row sm:items-start">
              <QrCanvas value={enrollment.otpauth_uri} />
              <div className="min-w-0 space-y-2 text-sm">
                <ol className="list-decimal space-y-1 pl-4 text-muted-foreground">
                  <li>{t("security.step1")}</li>
                  <li>{t("security.step2")}</li>
                </ol>
                <div className="text-xs text-muted-foreground">{t("security.manualKey")}</div>
                <div className="flex items-center gap-1">
                  <Mono className="break-all rounded bg-muted px-2 py-1 text-xs">{formatSecret(enrollment.secret)}</Mono>
                  <CopyButton value={enrollment.secret} />
                </div>
              </div>
            </div>
            <Field label={t("security.code")} htmlFor="totp-code" error={error ?? undefined}>
              <Input
                id="totp-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                maxLength={6}
                className="max-w-[10rem] text-center font-mono text-lg tracking-widest"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                autoFocus
              />
            </Field>
            <DialogFooter className="gap-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" disabled={confirm.isPending || code.length !== 6}>
                {t("security.activate")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function CodeDialog({
  open,
  onOpenChange,
  mode,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  mode: "disable" | "regenerate";
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useErrorMessage();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  useEffect(() => {
    if (open) {
      setPassword("");
      setCode("");
      setError(null);
      setCodes(null);
    }
  }, [open]);
  const run = useMutation({
    mutationFn: async () => {
      if (mode === "disable") {
        await security.disableTotp({ password, code });
        return null;
      }
      return (await security.regenerateRecoveryCodes(code)).recovery_codes;
    },
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: meQueryKey });
      if (r) setCodes(r);
      else {
        toast.success(t("security.disabled"));
        onOpenChange(false);
      }
    },
    onError: (e) => setError(errMsg(e)),
  });
  return (
    <Dialog open={open} onOpenChange={(o) => !codes && onOpenChange(o)}>
      <DialogContent closeLabel={t("common.close")}>
        <DialogHeader>
          <DialogTitle>{codes ? t("security.recoveryTitle") : mode === "disable" ? t("security.disableTitle") : t("security.regenerateTitle")}</DialogTitle>
          <DialogDescription>{codes ? t("security.recoveryHint") : mode === "disable" ? t("security.disableHint") : t("security.regenerateHint")}</DialogDescription>
        </DialogHeader>
        {codes ? (
          <RecoveryCodes codes={codes} onDone={() => onOpenChange(false)} />
        ) : (
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              setError(null);
              run.mutate();
            }}
          >
            {mode === "disable" && (
              <Field label={t("security.currentPassword")} htmlFor="cd-pass">
                <Input id="cd-pass" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
              </Field>
            )}
            <Field label={t("security.codeOrRecovery")} htmlFor="cd-code" error={error ?? undefined}>
              <Input id="cd-code" autoComplete="one-time-code" className="font-mono" value={code} onChange={(e) => setCode(e.target.value.trim())} autoFocus />
            </Field>
            <DialogFooter className="gap-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t("common.cancel")}
              </Button>
              <Button type="submit" variant={mode === "disable" ? "destructive" : "default"} disabled={run.isPending || !code || (mode === "disable" && !password)}>
                {mode === "disable" ? t("security.disable") : t("security.regenerate")}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function TotpSection({ autoEnroll }: { autoEnroll: boolean }) {
  const { t } = useTranslation();
  const me = useMe();
  const enabled = me.user.totp_enabled;
  const [dialog, setDialog] = useState<"enroll" | "disable" | "regenerate" | null>(autoEnroll && !enabled ? "enroll" : null);
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        {enabled ? (
          <Badge variant="success" className="gap-1">
            <ShieldCheck className="h-3.5 w-3.5" />
            {t("security.on")}
          </Badge>
        ) : (
          <Badge variant="outline" className="gap-1">
            <ShieldOff className="h-3.5 w-3.5" />
            {t("security.off")}
          </Badge>
        )}
        <span className="text-sm text-muted-foreground">{enabled ? t("security.onHint") : t("security.offHint")}</span>
      </div>
      {me.must_enroll_totp && !enabled && (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-900 dark:text-amber-200">{t("security.required")}</div>
      )}
      <div className="flex flex-wrap gap-2">
        {enabled ? (
          <>
            <Button size="sm" variant="outline" onClick={() => setDialog("regenerate")}>
              <KeyRound />
              {t("security.regenerate")}
            </Button>
            {!me.user.totp_required && (
              <Button size="sm" variant="outline" className="text-destructive" onClick={() => setDialog("disable")}>
                <ShieldOff />
                {t("security.disable")}
              </Button>
            )}
          </>
        ) : (
          <Button size="sm" onClick={() => setDialog("enroll")}>
            <ShieldCheck />
            {t("security.enable")}
          </Button>
        )}
      </div>
      <EnrollDialog open={dialog === "enroll"} onOpenChange={(o) => setDialog(o ? "enroll" : null)} />
      <CodeDialog open={dialog === "disable" || dialog === "regenerate"} mode={dialog === "disable" ? "disable" : "regenerate"} onOpenChange={(o) => !o && setDialog(null)} />
    </div>
  );
}
