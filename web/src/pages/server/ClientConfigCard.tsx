import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronUp, Copy, KeyRound, Monitor, Terminal } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { serverApi } from "@/api/system";
import { CopyButton, copyText, ErrorState, Mono } from "@/components";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton, Switch } from "@/components/ui/misc";

/**
 * The Windows installer name that makes the official client use this server:
 * RustDesk reads its config string from "rustdesk-licensed-<config>.exe"
 * (works for every key, unlike host=...,key=..., which breaks on "/" in keys).
 */
export function windowsExeName(configString: string): string {
  return configString ? `rustdesk-licensed-${configString}.exe` : "rustdesk.exe";
}

/**
 * Formats the CLI invocation command for automated deployment or scripting.
 */
export function cliCommand(configString: string): string {
  return `rustdesk --config "${configString}"`;
}

export function ClientConfigCard() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ["server", "client-config"], queryFn: serverApi.clientConfig });
  const [showManual, setShowManual] = useState(false);
  const [useIp, setUseIp] = useState(false);

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-48" />;

  const c = q.data;
  const hasIp = !!(c.config_string_ip && (c.id_server_ip || c.relay_server_ip));
  const activeIdServer = useIp && c.id_server_ip ? c.id_server_ip : c.id_server;
  const activeRelayServer = useIp && c.relay_server_ip ? c.relay_server_ip : c.relay_server;
  const activeConfigString = useIp && c.config_string_ip ? c.config_string_ip : c.config_string;

  const cmd = cliCommand(activeConfigString);
  const exeName = windowsExeName(activeConfigString);

  const handleCopyMain = async () => {
    if (await copyText(activeConfigString)) {
      toast.success(t("server.clientConfig.copiedToast"));
    }
  };

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              <Monitor className="h-4 w-4" />
              {t("server.clientConfig.title")}
            </CardTitle>
            <CardDescription className="mt-1">
              {t("server.clientConfig.description")}{" "}
              <Link to="/my/install" className="text-primary underline-offset-4 hover:underline">
                {t("server.clientConfig.installPage")}
              </Link>
            </CardDescription>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {hasIp && (
              <label
                className="flex items-center gap-2 rounded-md border bg-muted/40 px-2.5 py-1.5 text-xs font-medium cursor-pointer hover:bg-muted/70 transition-colors select-none"
                title={t("server.clientConfig.useIpHint")}
              >
                <Switch checked={useIp} onCheckedChange={setUseIp} aria-label={t("server.clientConfig.useIp")} />
                <span>{t("server.clientConfig.useIp")}</span>
                {c.id_server_ip && (
                  <Mono className="text-[11px] text-muted-foreground hidden sm:inline">({c.id_server_ip})</Mono>
                )}
              </label>
            )}
            <Button type="button" onClick={handleCopyMain} className="gap-2">
              <Copy className="h-4 w-4" />
              {t("server.clientConfig.copyButton")}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {!c.public_key && (
          <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-800 dark:text-amber-300">
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
            <span>{t("server.clientConfig.noKeyWarning")}</span>
          </div>
        )}

        {/* Encoded config string */}
        <div className="space-y-1.5">
          <label className="text-xs font-medium text-muted-foreground">{t("server.clientConfig.configStringLabel")}</label>
          <div className="relative">
            <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-2.5 pr-10 font-mono text-xs leading-relaxed">
              {activeConfigString}
            </pre>
            <CopyButton value={activeConfigString} label={t("server.clientConfig.copyString")} className="absolute right-2 top-2" />
          </div>
          <p className="text-xs text-muted-foreground">{t("server.clientConfig.instructions")}</p>
        </div>

        {/* CLI command and Windows exe naming */}
        <div className="grid gap-3 sm:grid-cols-2">
          <div className="rounded-md border p-3 space-y-1.5">
            <div className="flex items-center justify-between gap-2 text-xs font-medium">
              <span className="flex items-center gap-1.5">
                <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
                {t("server.clientConfig.cliLabel")}
              </span>
              <CopyButton value={cmd} label={t("common.copy")} />
            </div>
            <Mono className="block truncate text-xs text-muted-foreground">
              {cmd}
            </Mono>
          </div>

          <div className="rounded-md border p-3 space-y-1.5">
            <div className="flex items-center justify-between gap-2 text-xs font-medium">
              <span className="flex items-center gap-1.5">
                <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                {t("server.clientConfig.exeLabel")}
              </span>
              <CopyButton value={exeName} label={t("common.copy")} />
            </div>
            <Mono className="block truncate text-xs text-muted-foreground">
              {exeName}
            </Mono>
          </div>
        </div>

        {/* Manual parameters accordion */}
        <div className="border-t pt-3">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => setShowManual((v) => !v)}
            className="flex w-full items-center justify-between p-0 h-auto font-medium text-xs text-muted-foreground hover:text-foreground"
          >
            <span>{t("server.clientConfig.manualToggle")}</span>
            {showManual ? <ChevronUp className="h-4 w-4" /> : <ChevronDown className="h-4 w-4" />}
          </Button>

          {showManual && (
            <div className="mt-3 grid gap-2.5 rounded-md border bg-muted/20 p-3 sm:grid-cols-2">
              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium">{t("server.clientConfig.idServer")}</span>
                  <CopyButton value={activeIdServer} />
                </div>
                <Mono className="block truncate text-xs">{activeIdServer || "—"}</Mono>
              </div>

              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium">{t("server.clientConfig.relayServer")}</span>
                  <CopyButton value={activeRelayServer} />
                </div>
                <Mono className="block truncate text-xs">{activeRelayServer || "—"}</Mono>
              </div>

              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium">{t("server.clientConfig.apiServer")}</span>
                  <CopyButton value={c.api_server} />
                </div>
                <Mono className="block truncate text-xs">{c.api_server || "—"}</Mono>
              </div>

              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium">{t("server.clientConfig.publicKey")}</span>
                  <CopyButton value={c.public_key} />
                </div>
                <Mono className="block truncate text-xs">{c.public_key || "—"}</Mono>
              </div>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
