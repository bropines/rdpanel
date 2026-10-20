import { ChevronDown, GripVertical, MoreHorizontal, Pin, PinOff, PowerOff, SlidersHorizontal, X } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Checkbox } from "@/components/ui/misc";
import { cn } from "@/lib/utils";

export type ActionId =
  | "sound"
  | "settings"
  | "files"
  | "keyboard"
  | "inputMode"
  | "viewMode"
  | "nextDisplay"
  | "ctrlAltDel"
  | "lockScreen"
  | "refresh"
  | "mute"
  | "viewOnly"
  | "remoteCursor"
  | "stats"
  | "clipboard"
  | "typeText"
  | "chat"
  | "screenshot"
  | "privacy"
  | "blockInput"
  | "fullscreen"
  | "forgetPassword"
  | "restart";

export interface ToolbarAction {
  id: ActionId;
  label: string;
  icon: LucideIcon;
  /** Pressed state for toggles. */
  active?: boolean;
  disabled?: boolean;
  /** Not available for this device/session: neither shown nor offered for pinning. */
  hidden?: boolean;
  danger?: boolean;
  /** Shown on the bar even when not pinned (temporary, e.g. "enable sound"). */
  forced?: boolean;
  run: () => void;
}

export const DEFAULT_PINNED_DESKTOP: ActionId[] = ["settings", "ctrlAltDel", "viewMode", "fullscreen"];
export const DEFAULT_PINNED_TOUCH: ActionId[] = ["keyboard", "inputMode", "settings"];

export interface ToolbarProps {
  actions: ToolbarAction[];
  pinned: ActionId[];
  onPinned: (ids: ActionId[]) => void;
  defaultPinned: ActionId[];
  /** Keep the bar expanded instead of collapsing it to a handle. */
  keepOpen: boolean;
  onKeepOpen: (v: boolean) => void;
  /** Horizontal centre of the bar, 0..1 of the screen width. */
  position: number;
  onPosition: (x: number) => void;
  title: string;
  subtitle: string;
  status: "ok" | "busy" | "error";
  /** While false the bar stays expanded (connecting, errors). */
  canCollapse: boolean;
  onDisconnect: () => void;
  onLeave: () => void;
  ended: boolean;
}

const COLLAPSE_AFTER_MS = 2500;

export function Toolbar(p: ToolbarProps) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(true);
  const [menuOpen, setMenuOpen] = useState(false);
  const [customizeOpen, setCustomizeOpen] = useState(false);
  const [dragX, setDragX] = useState<number | null>(null);
  const hovering = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const visible = p.actions.filter((a) => !a.hidden);
  const byId = new Map(visible.map((a) => [a.id, a]));
  const forced = visible.filter((a) => a.forced && !p.pinned.includes(a.id));
  const pinnedActions = [...forced, ...p.pinned.map((id) => byId.get(id)).filter((a): a is ToolbarAction => !!a)];
  const rest = visible.filter((a) => !p.pinned.includes(a.id) && !a.forced);
  const stayOpen = p.keepOpen || !p.canCollapse || menuOpen || customizeOpen || dragX !== null;

  const scheduleCollapse = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      if (!hovering.current) setExpanded(false);
    }, COLLAPSE_AFTER_MS);
  }, []);

  useEffect(() => {
    if (stayOpen) {
      if (timer.current) clearTimeout(timer.current);
      setExpanded(true);
    } else {
      scheduleCollapse();
    }
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [stayOpen, scheduleCollapse]);

  const x = dragX ?? p.position;
  const style = { left: `${x * 100}%` };

  const onGripDown = (e: React.PointerEvent) => {
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
    setDragX(p.position);
  };
  const onGripMove = (e: React.PointerEvent) => {
    if (dragX === null) return;
    setDragX(Math.min(0.85, Math.max(0.15, e.clientX / window.innerWidth)));
  };
  const onGripUp = () => {
    if (dragX === null) return;
    p.onPosition(dragX);
    setDragX(null);
  };

  const dot = p.status === "ok" ? "bg-emerald-400" : p.status === "error" ? "bg-red-400" : "animate-pulse bg-amber-400";

  if (!expanded) {
    return (
      <button
        type="button"
        style={style}
        className="absolute top-0 z-30 flex h-5 -translate-x-1/2 items-center gap-1 rounded-b-md bg-neutral-900/80 px-2 text-white/80 shadow backdrop-blur hover:bg-neutral-800"
        aria-label={t("webclient.toolbar.show")}
        title={t("webclient.toolbar.show")}
        onClick={() => {
          setExpanded(true);
          scheduleCollapse();
        }}
        onPointerEnter={(e) => {
          if (e.pointerType === "mouse") {
            setExpanded(true);
            scheduleCollapse();
          }
        }}
      >
        <span className={cn("h-1.5 w-1.5 rounded-full", dot)} />
        <ChevronDown className="h-3.5 w-3.5" />
      </button>
    );
  }

  return (
    <>
      <div
        style={style}
        className="absolute top-0 z-30 flex h-9 max-w-[calc(100vw-0.5rem)] -translate-x-1/2 items-center gap-0.5 rounded-b-xl border border-t-0 border-white/10 bg-neutral-900/90 px-1 text-white shadow-lg backdrop-blur"
        onPointerEnter={() => {
          hovering.current = true;
        }}
        onPointerLeave={() => {
          hovering.current = false;
          if (!stayOpen) scheduleCollapse();
        }}
        onPointerDown={() => {
          if (!stayOpen) scheduleCollapse();
        }}
      >
        <span
          className="hidden h-7 w-4 shrink-0 cursor-grab touch-none items-center justify-center text-white/40 hover:text-white/80 active:cursor-grabbing sm:flex"
          aria-hidden="true"
          title={t("webclient.toolbar.drag")}
          onPointerDown={onGripDown}
          onPointerMove={onGripMove}
          onPointerUp={onGripUp}
          onPointerCancel={onGripUp}
        >
          <GripVertical className="h-3.5 w-3.5" />
        </span>
        <div className="flex min-w-0 shrink items-center gap-1.5 px-1" title={`${p.title} · ${p.subtitle}`}>
          <span className={cn("h-2 w-2 shrink-0 rounded-full", dot)} />
          <span className="hidden max-w-[9rem] truncate text-xs font-medium sm:inline">{p.title}</span>
        </div>
        <div className="flex min-w-0 items-center gap-0.5 overflow-x-auto [scrollbar-width:none]">
          {pinnedActions.map((a) => (
            <BarButton key={a.id} action={a} />
          ))}
        </div>
        <DropdownMenu modal={false} open={menuOpen} onOpenChange={setMenuOpen}>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 shrink-0 text-white hover:bg-white/10 hover:text-white"
              aria-label={t("webclient.toolbar.more")}
              title={t("webclient.toolbar.more")}
            >
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="center" className="max-h-[70vh] w-64 overflow-y-auto">
            {rest.length > 0 && <DropdownMenuLabel className="text-xs text-muted-foreground">{p.subtitle}</DropdownMenuLabel>}
            {rest.map((a) => (
              <DropdownMenuItem
                key={a.id}
                disabled={a.disabled}
                onSelect={a.run}
                className={cn(a.danger && "text-destructive")}
              >
                <a.icon className="mr-2 h-4 w-4" />
                <span className="flex-1">{a.label}</span>
                {a.active !== undefined && (
                  <span className={cn("ml-2 h-2 w-2 rounded-full", a.active ? "bg-emerald-500" : "bg-muted")} />
                )}
              </DropdownMenuItem>
            ))}
            {rest.length > 0 && <DropdownMenuSeparator />}
            <DropdownMenuItem onSelect={() => setCustomizeOpen(true)}>
              <SlidersHorizontal className="mr-2 h-4 w-4" />
              {t("webclient.toolbar.customize")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => p.onKeepOpen(!p.keepOpen)}>
              {p.keepOpen ? <PinOff className="mr-2 h-4 w-4" /> : <Pin className="mr-2 h-4 w-4" />}
              {p.keepOpen ? t("webclient.toolbar.unpin") : t("webclient.toolbar.pin")}
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={p.onLeave}>
              <X className="mr-2 h-4 w-4" />
              {t("webclient.toolbar.back")}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        <Button
          variant="ghost"
          size="icon"
          className={cn("h-7 w-7 shrink-0 hover:bg-white/10", p.keepOpen ? "text-white" : "text-white/50 hover:text-white")}
          aria-label={p.keepOpen ? t("webclient.toolbar.unpin") : t("webclient.toolbar.pin")}
          aria-pressed={p.keepOpen}
          title={p.keepOpen ? t("webclient.toolbar.unpin") : t("webclient.toolbar.pin")}
          onClick={() => p.onKeepOpen(!p.keepOpen)}
        >
          {p.keepOpen ? <Pin className="h-3.5 w-3.5" /> : <PinOff className="h-3.5 w-3.5" />}
        </Button>
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 shrink-0 text-red-300 hover:bg-white/10 hover:text-red-200"
          aria-label={p.ended ? t("common.back") : t("webclient.disconnect")}
          title={p.ended ? t("common.back") : t("webclient.disconnect")}
          onClick={p.ended ? p.onLeave : p.onDisconnect}
        >
          {p.ended ? <X className="h-4 w-4" /> : <PowerOff className="h-4 w-4" />}
        </Button>
      </div>
      <CustomizeDialog
        open={customizeOpen}
        onOpenChange={setCustomizeOpen}
        actions={visible}
        pinned={p.pinned}
        onPinned={p.onPinned}
        defaultPinned={p.defaultPinned}
      />
    </>
  );
}

function BarButton({ action: a }: { action: ToolbarAction }) {
  return (
    <Button
      variant="ghost"
      size="icon"
      title={a.label}
      aria-label={a.label}
      aria-pressed={a.active}
      disabled={a.disabled}
      onClick={a.run}
      className={cn(
        "h-7 w-7 shrink-0 text-white hover:bg-white/10 hover:text-white",
        a.active && "bg-white/15",
        a.danger && "text-red-300",
      )}
    >
      <a.icon className="h-4 w-4" />
    </Button>
  );
}

function CustomizeDialog({
  open,
  onOpenChange,
  actions,
  pinned,
  onPinned,
  defaultPinned,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  actions: ToolbarAction[];
  pinned: ActionId[];
  onPinned: (ids: ActionId[]) => void;
  defaultPinned: ActionId[];
}) {
  const { t } = useTranslation();
  const toggle = (id: ActionId, on: boolean) => {
    if (on) onPinned([...pinned.filter((x) => x !== id), id]);
    else onPinned(pinned.filter((x) => x !== id));
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[calc(100vw-2rem)] sm:max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("webclient.toolbar.customize")}</DialogTitle>
          <DialogDescription>{t("webclient.toolbar.customizeDesc")}</DialogDescription>
        </DialogHeader>
        <div className="max-h-[55vh] space-y-0.5 overflow-y-auto pr-1">
          {actions.filter((a) => !a.forced).map((a) => (
            <label key={a.id} className="flex cursor-pointer items-center gap-3 rounded-md px-2 py-1.5 hover:bg-muted">
              <Checkbox checked={pinned.includes(a.id)} onCheckedChange={(v) => toggle(a.id, v === true)} aria-label={a.label} />
              <a.icon className="h-4 w-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 truncate text-sm">{a.label}</span>
            </label>
          ))}
        </div>
        <DialogFooter className="gap-2">
          <Button variant="outline" onClick={() => onPinned(defaultPinned)}>
            {t("webclient.toolbar.reset")}
          </Button>
          <Button onClick={() => onOpenChange(false)}>{t("common.done")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
