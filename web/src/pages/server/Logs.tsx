import { keepPreviousData, useMutation, useQuery } from "@tanstack/react-query";
import { ArrowDown, Download, History, Info, ScrollText, Search } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { saveBlob, type Schemas } from "@/api/client";
import { backoffDelay, type StreamStatus } from "@/api/events";
import { logStreamUrl, serverApi } from "@/api/system";
import { CopyButton, EmptyState, ErrorState, useToastError } from "@/components";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton, Switch } from "@/components/ui/misc";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";

export type LogEntry = Schemas["LogEntry"];
export type LogLevel = Schemas["LogLevel"];

/** Lines kept in the browser (the newest win). */
export const LOG_CAP = 3000;
const PAGE = 500;
const LEVELS: LogLevel[] = ["debug", "info", "warn", "error"];

/** Merges entries by seq (ascending, no duplicates) and keeps the newest `cap`. Exported for tests. */
export function mergeLogs(prev: LogEntry[], incoming: LogEntry[], cap = LOG_CAP): LogEntry[] {
  if (incoming.length === 0) return prev;
  const last = prev.length ? prev[prev.length - 1]!.seq : 0;
  let out: LogEntry[];
  if (incoming[0]!.seq > last) {
    out = prev.concat(incoming); // the common live case: strictly newer
  } else {
    const bySeq = new Map<number, LogEntry>();
    for (const e of prev) bySeq.set(e.seq, e);
    for (const e of incoming) bySeq.set(e.seq, e);
    out = [...bySeq.values()].sort((a, b) => a.seq - b.seq);
  }
  return out.length > cap ? out.slice(out.length - cap) : out;
}

/** Tailwind classes of a level label. Exported for tests. */
export function levelClass(level: LogLevel): string {
  switch (level) {
    case "error":
      return "text-red-600 dark:text-red-400";
    case "warn":
      return "text-amber-600 dark:text-amber-400";
    case "debug":
      return "text-zinc-400 dark:text-zinc-500";
    default:
      return "text-sky-700 dark:text-sky-400";
  }
}

/** Local wall-clock time with milliseconds ("13:27:07.704"); "" when unknown. Exported for tests. */
export function formatLogTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
}

const SOURCE_TONE: Record<string, string> = {
  hbbs: "text-violet-700 dark:text-violet-400",
  hbbr: "text-teal-700 dark:text-teal-400",
  panel: "text-blue-700 dark:text-blue-400",
  other: "text-zinc-500",
};

interface Filter {
  source?: string;
  level?: LogLevel;
  q?: string;
}

/** Follows the SSE stream while `enabled`; reconnects with backoff, resuming after the last seen seq. */
function useLogStream(enabled: boolean, filter: Filter, lastSeq: () => number, onEntries: (e: LogEntry[]) => void) {
  const [status, setStatus] = useState<StreamStatus>("closed");
  const [dropped, setDropped] = useState(0);
  const key = JSON.stringify(filter);
  const cb = useRef(onEntries);
  cb.current = onEntries;
  const seq = useRef(lastSeq);
  seq.current = lastSeq;

  useEffect(() => {
    setDropped(0);
    if (!enabled || typeof EventSource === "undefined") {
      setStatus("closed");
      return;
    }
    const f = JSON.parse(key) as Filter;
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    let pending: LogEntry[] = [];
    let attempt = 0;
    let stopped = false;
    const flush = () => {
      flushTimer = undefined;
      if (pending.length) {
        const batch = pending;
        pending = [];
        cb.current(batch);
      }
    };
    const open = () => {
      if (stopped) return;
      setStatus(attempt > 0 ? "reconnecting" : "connecting");
      const after = seq.current();
      const s = new EventSource(logStreamUrl({ ...f, after: after > 0 ? after : undefined }), { withCredentials: true });
      source = s;
      s.onopen = () => {
        attempt = 0;
        setStatus("open");
      };
      s.onerror = () => {
        s.close();
        if (stopped) return;
        setStatus("reconnecting");
        timer = setTimeout(open, backoffDelay(attempt));
        attempt += 1;
      };
      s.addEventListener("log", (ev: MessageEvent) => {
        try {
          pending.push(JSON.parse(String(ev.data)) as LogEntry);
        } catch {
          return;
        }
        // Batch renders: busy servers log hundreds of lines per second.
        flushTimer ??= setTimeout(flush, 200);
      });
      s.addEventListener("dropped", (ev: MessageEvent) => {
        try {
          const n = (JSON.parse(String(ev.data)) as { dropped: number }).dropped;
          setDropped((d) => d + n);
        } catch {
          /* ignore */
        }
      });
      s.addEventListener("unauthenticated", () => {
        stopped = true;
        s.close();
        setStatus("closed");
      });
    };
    open();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      if (flushTimer) clearTimeout(flushTimer);
      flush();
      source?.close();
    };
  }, [enabled, key]);

  return { status, dropped };
}

function LogRow({ e }: { e: LogEntry }) {
  return (
    <div className="group flex flex-wrap items-start gap-x-2 border-b border-zinc-200/60 px-2 py-0.5 hover:bg-zinc-100 dark:border-zinc-800/60 dark:hover:bg-zinc-900 sm:flex-nowrap">
      <span className="shrink-0 tabular-nums text-zinc-500" title={e.time ?? undefined}>
        {formatLogTime(e.time) || "—"}
      </span>
      <span className={cn("w-10 shrink-0 font-semibold", SOURCE_TONE[e.source] ?? SOURCE_TONE.other)}>{e.source}</span>
      <span className={cn("w-11 shrink-0 font-semibold uppercase", levelClass(e.level))}>{e.level}</span>
      <span className="order-last min-w-0 basis-full whitespace-pre-wrap break-all sm:order-none sm:basis-auto sm:flex-1" title={e.raw}>
        {e.message || e.raw}
      </span>
      <CopyButton value={e.raw} className="-my-1 ml-auto h-6 w-6 shrink-0 opacity-60 sm:ml-0 group-hover:opacity-100 sm:opacity-0 [&_svg]:h-3.5 [&_svg]:w-3.5" />
    </div>
  );
}

export function LogsTab() {
  const { t } = useTranslation();
  const toastError = useToastError();
  const [sources, setSources] = useState<string[]>([]);
  const [level, setLevel] = useState<LogLevel | "all">("all");
  const [qInput, setQInput] = useState("");
  const q = useDebounced(qInput.trim(), 300);
  const [live, setLive] = useState(true);
  const [follow, setFollow] = useState(true);
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [nextBefore, setNextBefore] = useState<number | null>(null);
  const [loadedKey, setLoadedKey] = useState<string | null>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const filter = useMemo<Filter>(
    () => ({ source: sources.length ? sources.join(",") : undefined, level: level === "all" ? undefined : level, q: q || undefined }),
    [sources, level, q],
  );
  const filterKey = JSON.stringify(filter);
  const page = useQuery({
    queryKey: ["server", "logs", filter],
    queryFn: () => serverApi.logs({ ...filter, limit: PAGE }),
    staleTime: Infinity,
    placeholderData: keepPreviousData,
    gcTime: 0,
  });

  // A new first page replaces the list.
  useEffect(() => {
    if (!page.data || page.isPlaceholderData) return;
    setEntries(page.data.items);
    setNextBefore(page.data.next_before);
    setLoadedKey(filterKey);
    setFollow(true);
  }, [page.data, page.isPlaceholderData, filterKey]);

  const lastSeq = useCallback(() => (entries.length ? entries[entries.length - 1]!.seq : 0), [entries]);
  const enabled = !!page.data?.enabled;
  const stream = useLogStream(live && enabled && loadedKey === filterKey, filter, lastSeq, (batch) =>
    setEntries((prev) => mergeLogs(prev, batch)),
  );

  // Auto-scroll while following; scrolling up pauses it.
  useEffect(() => {
    const el = listRef.current;
    if (el && follow) el.scrollTop = el.scrollHeight;
  }, [entries, follow]);
  const onScroll = () => {
    const el = listRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (atBottom !== follow) setFollow(atBottom);
  };

  const older = useMutation({
    mutationFn: () => serverApi.logs({ ...filter, limit: PAGE, before: nextBefore ?? undefined }),
    onSuccess: (p) => {
      setFollow(false);
      setEntries((prev) => mergeLogs(p.items, prev, LOG_CAP + PAGE));
      setNextBefore(p.next_before);
    },
    onError: toastError,
  });
  const download = useMutation({
    mutationFn: () => serverApi.downloadLogs(filter),
    onSuccess: ({ blob, filename }) => saveBlob(blob, filename ?? "rdpanel-logs.txt"),
    onError: toastError,
  });

  if (page.isError) return <ErrorState error={page.error} onRetry={() => void page.refetch()} />;
  const data = page.data;
  if (data && !data.enabled) {
    return <EmptyState icon={ScrollText} title={t("server.logs.disabled")} description={t("server.logs.disabledHint")} />;
  }
  const available = data?.sources.map((s) => s.name) ?? [];
  const toggleSource = (s: string) => setSources((cur) => (cur.includes(s) ? cur.filter((x) => x !== s) : [...cur, s]));
  const statusTone =
    stream.status === "open" ? "bg-emerald-500" : stream.status === "closed" ? "bg-zinc-400" : "bg-amber-500 animate-pulse";

  return (
    <Card>
      <CardHeader className="space-y-3 pb-3">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              <ScrollText className="h-4 w-4" />
              {t("server.logs.title")}
            </CardTitle>
            <CardDescription>{t("server.logs.description", { n: data?.capacity ?? 0 })}</CardDescription>
          </div>
          <div className="flex items-center gap-3">
            <label className="flex items-center gap-2 text-sm">
              <span className={cn("h-2 w-2 rounded-full", live ? statusTone : "bg-zinc-400")} title={t(`server.logs.status.${live ? stream.status : "closed"}`)} />
              {t("server.logs.live")}
              <Switch checked={live} onCheckedChange={setLive} aria-label={t("server.logs.live")} />
            </label>
            <Button variant="outline" size="sm" onClick={() => download.mutate()} disabled={download.isPending || !data}>
              <Download />
              {t("server.logs.download")}
            </Button>
          </div>
        </div>
        {data?.capture === "panel" && (
          <p className="flex items-start gap-1.5 text-xs text-muted-foreground">
            <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            {t("server.logs.panelOnly")}
          </p>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex flex-wrap gap-1" role="group" aria-label={t("server.logs.sources")}>
            <Button size="sm" variant={sources.length === 0 ? "secondary" : "ghost"} className="h-8" aria-pressed={sources.length === 0} onClick={() => setSources([])}>
              {t("server.logs.allSources")}
            </Button>
            {available.map((s) => {
              const on = sources.includes(s);
              return (
                <Button key={s} size="sm" variant={on ? "secondary" : "ghost"} className={cn("h-8 font-mono", on && "ring-1 ring-primary/40")} aria-pressed={on} onClick={() => toggleSource(s)}>
                  <span className={SOURCE_TONE[s]}>{s}</span>
                  <span className="text-xs text-muted-foreground">{data?.sources.find((x) => x.name === s)?.lines ?? 0}</span>
                </Button>
              );
            })}
          </div>
          <Select value={level} onValueChange={(v) => setLevel(v as LogLevel | "all")}>
            <SelectTrigger className="h-8 w-auto min-w-[8.5rem]" aria-label={t("server.logs.level")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("server.logs.levelAll")}</SelectItem>
              {LEVELS.map((l) => (
                <SelectItem key={l} value={l}>
                  <span className={levelClass(l)}>{t(`server.logs.levels.${l}`)}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="relative min-w-[12rem] flex-1">
            <Search className="pointer-events-none absolute left-2.5 top-2 h-4 w-4 text-muted-foreground" />
            <Input
              className="h-8 pl-8"
              value={qInput}
              onChange={(e) => setQInput(e.target.value)}
              placeholder={t("server.logs.search")}
              aria-label={t("server.logs.search")}
              maxLength={200}
              spellCheck={false}
            />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-2">
        {page.isLoading ? (
          <Skeleton className="h-[60vh]" />
        ) : (
          <div className="relative">
            <div
              ref={listRef}
              onScroll={onScroll}
              className="h-[60vh] min-h-[16rem] overflow-y-auto rounded-md border bg-zinc-50 py-1 font-mono text-xs leading-5 dark:bg-zinc-950"
              role="log"
              aria-live="off"
            >
              {nextBefore != null && entries.length < LOG_CAP + PAGE && (
                <div className="flex justify-center py-1">
                  <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={() => older.mutate()} disabled={older.isPending}>
                    <History />
                    {t("server.logs.loadOlder")}
                  </Button>
                </div>
              )}
              {entries.length === 0 ? (
                <div className="p-4 text-muted-foreground">{filter.source || filter.level || filter.q ? t("server.logs.noMatch") : t("server.logs.empty")}</div>
              ) : (
                entries.map((e) => <LogRow key={e.seq} e={e} />)
              )}
            </div>
            {!follow && entries.length > 0 && (
              <Button size="sm" className="absolute bottom-3 right-4 h-7 shadow" onClick={() => setFollow(true)}>
                <ArrowDown />
                {t("server.logs.jumpLatest")}
              </Button>
            )}
          </div>
        )}
        <div className="flex flex-wrap justify-between gap-2 text-xs text-muted-foreground">
          <span>
            {t("server.logs.shown", { count: entries.length })}
            {stream.dropped > 0 && <span className="text-amber-600"> · {t("server.logs.dropped", { count: stream.dropped })}</span>}
          </span>
          <span>{t("server.logs.redacted")}</span>
        </div>
      </CardContent>
    </Card>
  );
}
