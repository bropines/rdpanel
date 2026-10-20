import { AlertTriangle, Check, Copy, Inbox, RotateCw } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/misc";
import { argbToCss, readableTextColor } from "@/lib/argb";
import { formatDateTime, relativeTime } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="mb-5 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <h1 className="truncate text-xl font-semibold tracking-tight md:text-2xl">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: {
  icon?: React.ComponentType<{ className?: string }>;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 px-4 py-12 text-center", className)}>
      <div className="rounded-full bg-muted p-3">
        <Icon className="h-5 w-5 text-muted-foreground" />
      </div>
      <p className="font-medium">{title}</p>
      {description && <p className="max-w-sm text-sm text-muted-foreground">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

/** i18n key for an API error (falls back to the server message). */
export function useErrorMessage() {
  const { t, i18n } = useTranslation();
  return (err: unknown): string => {
    if (err instanceof ApiError) {
      const key = `errors.${err.code}`;
      if (i18n.exists(key)) return t(key);
      return err.message || t("errors.generic");
    }
    return t("errors.generic");
  };
}

export function ErrorState({ error, onRetry, className }: { error: unknown; onRetry?: () => void; className?: string }) {
  const { t } = useTranslation();
  const msg = useErrorMessage();
  return (
    <div className={cn("flex flex-col items-center justify-center gap-2 px-4 py-12 text-center", className)} role="alert">
      <div className="rounded-full bg-destructive/10 p-3">
        <AlertTriangle className="h-5 w-5 text-destructive" />
      </div>
      <p className="font-medium">{t("common.loadFailed")}</p>
      <p className="max-w-sm text-sm text-muted-foreground">{msg(error)}</p>
      {onRetry && (
        <Button variant="outline" size="sm" className="mt-2" onClick={onRetry}>
          <RotateCw />
          {t("common.retry")}
        </Button>
      )}
    </div>
  );
}

/** Shows a toast for a failed mutation. */
export function useToastError() {
  const msg = useErrorMessage();
  return (err: unknown) => toast.error(msg(err));
}

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  destructive,
  pending,
  onConfirm,
  confirmText,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  confirmLabel: ReactNode;
  destructive?: boolean;
  pending?: boolean;
  onConfirm: () => void;
  /** When set, the user must type this text to enable the confirm button. */
  confirmText?: string;
  children?: ReactNode;
}) {
  const { t } = useTranslation();
  const [typed, setTyped] = useState("");
  const blocked = confirmText !== undefined && typed.trim() !== confirmText;
  return (
    <AlertDialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setTyped("");
        onOpenChange(o);
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription>{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        {children}
        {confirmText !== undefined && (
          <div className="space-y-2">
            <Label htmlFor="confirm-text" className="text-sm font-normal text-muted-foreground">
              {t("common.typeToConfirm", { text: confirmText })}
            </Label>
            <Input id="confirm-text" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{t("common.cancel")}</AlertDialogCancel>
          <Button
            variant={destructive ? "destructive" : "default"}
            disabled={pending || blocked}
            onClick={() => onConfirm()}
          >
            {confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export function CopyButton({ value, label, className }: { value: string; label?: string; className?: string }) {
  const { t } = useTranslation();
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className={cn("h-7 w-7 text-muted-foreground hover:text-foreground", className)}
      aria-label={label ?? t("common.copy")}
      title={label ?? t("common.copy")}
      onClick={async (e) => {
        e.stopPropagation();
        const ok = await copyText(value);
        if (ok) {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        } else {
          toast.error(t("common.copyFailed"));
        }
      }}
    >
      {done ? <Check className="text-emerald-600" /> : <Copy />}
    </Button>
  );
}

export function RelativeTime({ iso, className }: { iso: string | null | undefined; className?: string }) {
  const { i18n } = useTranslation();
  const now = useNow(15_000);
  if (!iso) return <span className={cn("text-muted-foreground", className)}>—</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <time dateTime={iso} className={cn("whitespace-nowrap", className)}>
          {relativeTime(iso, i18n.language, now)}
        </time>
      </TooltipTrigger>
      <TooltipContent>{formatDateTime(iso, i18n.language)}</TooltipContent>
    </Tooltip>
  );
}

export function OnlineDot({
  online,
  notRegistered,
  className,
}: {
  online: boolean | null | undefined;
  /** Heartbeat to the API is recent, but hbbs has no registration: amber. */
  notRegistered?: boolean;
  className?: string;
}) {
  const { t } = useTranslation();
  const label = online ? t("devices.online") : notRegistered ? t("devices.notRegistered") : t("devices.offline");
  return (
    <span className={cn("relative inline-flex h-2.5 w-2.5 shrink-0", className)} title={label} role="img" aria-label={label}>
      {online && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-500 opacity-40" />}
      <span
        className={cn(
          "relative inline-flex h-2.5 w-2.5 rounded-full",
          online ? "bg-emerald-500" : notRegistered ? "bg-amber-500" : "bg-zinc-300 dark:bg-zinc-600",
        )}
      />
    </span>
  );
}

export function TagChip({
  name,
  color,
  onRemove,
  className,
}: {
  name: string;
  color: number;
  onRemove?: () => void;
  className?: string;
}) {
  return (
    <span
      className={cn("inline-flex max-w-[12rem] items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium", className)}
      style={{ backgroundColor: argbToCss(color), color: readableTextColor(color) }}
    >
      <span className="truncate">{name}</span>
      {onRemove && (
        <button type="button" onClick={onRemove} className="-mr-0.5 rounded-full px-0.5 opacity-70 hover:opacity-100" aria-label={`× ${name}`}>
          ×
        </button>
      )}
    </span>
  );
}

export function Field({
  label,
  htmlFor,
  error,
  hint,
  children,
  className,
}: {
  label: ReactNode;
  htmlFor?: string;
  error?: string | undefined;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : hint ? (
        <p className="text-xs text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

export function KeyValue({ items }: { items: [ReactNode, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[minmax(7rem,auto)_1fr] gap-x-4 gap-y-2 text-sm">
      {items.map(([k, v], i) => (
        <div key={i} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v ?? "—"}</dd>
        </div>
      ))}
    </dl>
  );
}

/** Tiny inline SVG sparkline (no chart library). */
export function Sparkline({
  values,
  className,
  label,
}: {
  values: number[];
  className?: string;
  label?: string;
}) {
  const w = 120;
  const h = 32;
  if (values.length < 2) return <svg viewBox={`0 0 ${w} ${h}`} className={className} aria-hidden />;
  const max = Math.max(1, ...values);
  const step = w / (values.length - 1);
  const pts = values.map((v, i) => [i * step, h - 2 - (v / max) * (h - 4)] as const);
  const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
  const area = `${line} L${w},${h} L0,${h} Z`;
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className={cn("h-8 w-full", className)} role="img" aria-label={label}>
      <path d={area} className="fill-primary/10" />
      <path d={line} className="fill-none stroke-primary" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

export function SectionTitle({ children, actions }: { children: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-2 flex items-center justify-between gap-2">
      <h3 className="text-sm font-semibold">{children}</h3>
      {actions}
    </div>
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cn("font-mono text-[0.8125rem] tabular-nums", className)}>{children}</span>;
}
