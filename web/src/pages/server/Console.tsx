import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, History, Play, TerminalSquare } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { Schemas } from "@/api/client";
import { serverApi } from "@/api/system";
import { EmptyState, ErrorState, Field, Mono, RelativeTime, useErrorMessage } from "@/components";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/misc";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";

/** Splits the args field and checks it against the command's allow-list rules. Exported for tests. */
export function parseConsoleArgs(
  text: string,
  cmd: Pick<Schemas["ConsoleCommand"], "max_args" | "arg_pattern"> | undefined,
): { args: string[]; error?: "tooMany" | "pattern" } {
  const args = text.trim() ? text.trim().split(/\s+/) : [];
  if (!cmd) return { args };
  if (args.length > cmd.max_args) return { args, error: "tooMany" };
  if (cmd.arg_pattern) {
    let re: RegExp;
    try {
      re = new RegExp(`^(?:${cmd.arg_pattern})$`);
    } catch {
      return { args };
    }
    if (args.some((a) => !re.test(a))) return { args, error: "pattern" };
  }
  return { args };
}

interface OutputEntry {
  id: number;
  target: string;
  line: string;
  output: string;
  error?: string;
  at: string;
  ms?: number;
}

export function Console() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const errMsg = useErrorMessage();
  const commands = useQuery({ queryKey: ["server", "console", "commands"], queryFn: serverApi.consoleCommands, staleTime: Infinity });
  const history = useQuery({ queryKey: ["server", "console", "history"], queryFn: () => serverApi.consoleHistory({ page_size: 25 }) });
  const [key, setKey] = useState<string>("");
  const [argText, setArgText] = useState("");
  const [log, setLog] = useState<OutputEntry[]>([]);

  const cmd = useMemo(() => commands.data?.items.find((c) => `${c.target}:${c.cmd}` === key), [commands.data, key]);
  const parsed = parseConsoleArgs(argText, cmd);

  const run = useMutation({
    mutationFn: () => serverApi.runConsole({ target: cmd!.target, cmd: cmd!.cmd, args: parsed.args }),
    onSuccess: (r) => {
      setLog((l) => [{ id: Date.now(), target: cmd!.target, line: [cmd!.cmd, ...parsed.args].join(" "), output: r.output, at: r.executed_at, ms: r.duration_ms }, ...l].slice(0, 30));
      void qc.invalidateQueries({ queryKey: ["server", "console", "history"] });
    },
    onError: (e) =>
      setLog((l) => [{ id: Date.now(), target: cmd!.target, line: [cmd!.cmd, ...parsed.args].join(" "), output: "", error: errMsg(e), at: new Date().toISOString() }, ...l].slice(0, 30)),
  });

  if (commands.isError) return <ErrorState error={commands.error} onRetry={() => void commands.refetch()} />;
  const byTarget = (target: "hbbs" | "hbbr") => (commands.data?.items ?? []).filter((c) => c.target === target);

  return (
    <div className="grid gap-4 xl:grid-cols-[1fr_22rem]">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <TerminalSquare className="h-4 w-4" />
            {t("server.console.title")}
          </CardTitle>
          <CardDescription className="flex items-start gap-1.5">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500" />
            {t("server.console.warning")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <form
            className="grid gap-2 sm:grid-cols-[16rem_1fr_auto] sm:items-end"
            onSubmit={(e) => {
              e.preventDefault();
              if (cmd && !parsed.error) run.mutate();
            }}
          >
            <Field label={t("server.console.command")} htmlFor="con-cmd">
              {commands.isLoading ? (
                <Skeleton className="h-9" />
              ) : (
                <Select value={key} onValueChange={setKey}>
                  <SelectTrigger id="con-cmd">
                    <SelectValue placeholder={t("server.console.pick")} />
                  </SelectTrigger>
                  <SelectContent>
                    {(["hbbs", "hbbr"] as const).map((target) => (
                      <SelectGroup key={target}>
                        <SelectLabel>{target}</SelectLabel>
                        {byTarget(target).map((c) => (
                          <SelectItem key={c.cmd} value={`${c.target}:${c.cmd}`}>
                            <span className="font-mono">{c.cmd}</span>
                            {c.mutating && <span className="ml-1 text-amber-600">•</span>}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field label={t("server.console.args")} htmlFor="con-args">
              <Input
                id="con-args"
                className="font-mono"
                value={argText}
                onChange={(e) => setArgText(e.target.value)}
                placeholder={cmd?.args_hint ?? ""}
                disabled={!cmd || cmd.max_args === 0}
                autoComplete="off"
                spellCheck={false}
              />
            </Field>
            <Button type="submit" disabled={!cmd || !!parsed.error || run.isPending}>
              <Play />
              {t("server.console.run")}
            </Button>
          </form>
          {cmd && (
            <p className="text-xs text-muted-foreground">
              {cmd.description}
              {cmd.mutating && <Badge variant="outline" className="ml-2 border-amber-500/50 font-normal text-amber-700 dark:text-amber-400">{t("server.console.mutating")}</Badge>}
            </p>
          )}
          {parsed.error && <p className="text-xs text-destructive">{t(`server.console.errors.${parsed.error}`, { max: cmd?.max_args ?? 0 })}</p>}
          <div className="max-h-[28rem] min-h-[12rem] overflow-y-auto rounded-md border bg-zinc-950 p-3 font-mono text-xs text-zinc-100" aria-live="polite">
            {log.length === 0 ? (
              <span className="text-zinc-500">{t("server.console.empty")}</span>
            ) : (
              log.map((e) => (
                <div key={e.id} className="mb-3">
                  <div className="text-zinc-400">
                    <span className="text-emerald-400">{e.target}</span> $ {e.line}
                    {e.ms !== undefined && <span className="text-zinc-600"> · {e.ms} ms</span>}
                  </div>
                  {e.error ? (
                    <div className="text-red-400">{e.error}</div>
                  ) : (
                    <pre className="whitespace-pre-wrap break-all">{e.output || t("server.console.noOutput")}</pre>
                  )}
                </div>
              ))
            )}
          </div>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <History className="h-4 w-4" />
            {t("server.console.history")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {history.isLoading ? (
            <Skeleton className="h-40" />
          ) : !history.data?.items.length ? (
            <EmptyState title={t("server.console.noHistory")} className="py-6" />
          ) : (
            <ul className="divide-y text-sm">
              {history.data.items.map((h) => (
                <li key={h.id} className="py-2">
                  <button
                    type="button"
                    className="w-full text-left hover:text-primary"
                    onClick={() => {
                      setKey(`${h.target}:${h.cmd}`);
                      setArgText(h.args.join(" "));
                    }}
                    title={h.output_excerpt ?? undefined}
                  >
                    <Mono className="text-xs">
                      <span className="text-muted-foreground">{h.target}</span> {h.cmd} {h.args.join(" ")}
                    </Mono>
                  </button>
                  <div className="text-xs text-muted-foreground">
                    {h.actor_name} · <RelativeTime iso={h.created_at} />
                    {h.error && <span className="text-destructive"> · {h.error}</span>}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
