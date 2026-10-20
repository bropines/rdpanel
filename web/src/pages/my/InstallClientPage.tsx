import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Apple, Download, ExternalLink, Monitor, Smartphone, Terminal } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { clientsApi, type ClientDownloads } from "@/api/system";
import { CopyButton, ErrorState, Mono, PageHeader } from "@/components";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { cn } from "@/lib/utils";
import { ShareLinksCard } from "./ShareLinksCard";
import { QrCanvas } from "./TotpSection";
import {
  APP_STORE_URL,
  detectPlatform,
  findAsset,
  formatSize,
  linuxCommand,
  macosCommand,
  PLATFORMS,
  qrValue,
  windowsCommand,
  type ClientAsset,
  type PlatformId,
} from "./install";

export function InstallClientPage() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ["clients"], queryFn: clientsApi.get, staleTime: 5 * 60_000 });
  const [detected] = useState(() => detectPlatform(navigator.userAgent, navigator.maxTouchPoints));
  const [platform, setPlatform] = useState<PlatformId>(detected);

  if (q.isError) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return <Skeleton className="h-96 max-w-4xl" />;
  const d = q.data;

  return (
    <div className="max-w-4xl space-y-4">
      <PageHeader title={t("install.title")} description={t("install.description", { host: d.id_server })} />

      {!d.public_key && <Warning>{t("install.noKey")}</Warning>}
      {d.enabled && d.error && d.assets.length === 0 && <Warning>{t("install.lookupFailed")}</Warning>}

      <div className="flex flex-wrap gap-1.5" role="tablist" aria-label={t("install.platform")}>
        {PLATFORMS.map((p) => (
          <Button
            key={p}
            type="button"
            role="tab"
            aria-selected={platform === p}
            variant={platform === p ? "default" : "outline"}
            size="sm"
            onClick={() => setPlatform(p)}
          >
            {t(`install.platforms.${p}`)}
            {p === detected && <span className="ml-1 text-[10px] opacity-70">· {t("install.thisDevice")}</span>}
          </Button>
        ))}
      </div>

      {platform === "windows" && <WindowsCard d={d} />}
      {platform === "macos" && <MacCard d={d} />}
      {platform === "linux" && <LinuxCard d={d} />}
      {(platform === "android" || platform === "ios") && <MobileCard d={d} platform={platform} />}

      <ManualCard d={d} />

      <ShareLinksCard host={d.id_server} />

      {d.version && (
        <p className="text-xs text-muted-foreground">
          {t("install.versionNote", { version: d.version })}{" "}
          {d.release_url && (
            <a className="underline-offset-4 hover:underline" href={d.release_url} target="_blank" rel="noreferrer noopener">
              {t("install.releaseNotes")}
            </a>
          )}
        </p>
      )}
    </div>
  );
}

function Warning({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm text-amber-800 dark:text-amber-300">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{children}</span>
    </div>
  );
}

function Steps({ items }: { items: ReactNode[] }) {
  return (
    <ol className="list-decimal space-y-1.5 pl-5 text-sm">
      {items.map((s, i) => (
        <li key={i}>{s}</li>
      ))}
    </ol>
  );
}

function CommandBlock({ title, command, hint }: { title: string; command: string; hint?: string }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2 text-xs font-medium">
        <span className="flex items-center gap-1.5">
          <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
          {title}
        </span>
        <CopyButton value={command} />
      </div>
      <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-2.5 font-mono text-[11px] leading-relaxed">{command}</pre>
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}

function AssetLink({ asset, label }: { asset: ClientAsset | undefined; label: string }) {
  if (!asset) return null;
  return (
    <Button asChild variant="outline" size="sm">
      <a href={asset.url} rel="noreferrer noopener">
        <Download className="h-4 w-4" />
        {label}
        <span className="text-xs text-muted-foreground">{formatSize(asset.size)}</span>
      </a>
    </Button>
  );
}

/** SHA-256 of an asset (the same file as on GitHub, so it matches the release's digest). */
function Checksum({ asset, releaseUrl, verify }: { asset: ClientAsset | undefined; releaseUrl?: string; verify: string }) {
  const { t } = useTranslation();
  if (!asset?.sha256) return null;
  return (
    <details className="rounded-md border p-2.5 text-xs">
      <summary className="cursor-pointer">
        <span className="font-medium">SHA-256</span> <Mono className="break-all text-muted-foreground">{asset.name}</Mono>
      </summary>
      <div className="mt-2 space-y-2">
        <div className="flex items-start gap-1">
          <Mono className="min-w-0 flex-1 break-all">{asset.sha256}</Mono>
          <CopyButton value={asset.sha256} />
        </div>
        <p className="text-muted-foreground">
          {t("install.checksum.hint")}{" "}
          {releaseUrl && (
            <a className="text-primary underline-offset-4 hover:underline" href={releaseUrl} target="_blank" rel="noreferrer noopener">
              {t("install.checksum.github")}
            </a>
          )}
        </p>
        <CommandBlock title={t("install.checksum.verify")} command={verify} />
      </div>
    </details>
  );
}

function PlatformCard({ icon, title, description, children }: { icon: ReactNode; title: string; description?: string; children: ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-base">
          {icon}
          {title}
        </CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  );
}

function WindowsCard({ d }: { d: ClientDownloads }) {
  const { t } = useTranslation();
  const x64 = findAsset(d.assets, "windows-x64");
  const arm = findAsset(d.assets, "windows-arm64");
  const ready = d.enabled && !!d.windows_file_name && !!d.public_key;
  return (
    <PlatformCard icon={<Monitor className="h-4 w-4" />} title={t("install.platforms.windows")} description={ready ? t("install.windows.desc") : undefined}>
      {ready && x64 ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Button asChild size="lg">
              <a href={clientsApi.windowsUrl("x64")} download={d.windows_file_name}>
                <Download className="h-4 w-4" />
                {t("install.windows.download")}
                <span className="text-xs opacity-80">{formatSize(x64.size)}</span>
              </a>
            </Button>
            {arm && (
              <Button asChild variant="outline" size="sm">
                <a href={clientsApi.windowsUrl("arm64")} download={d.windows_file_name}>
                  {t("install.windows.arm")}
                </a>
              </Button>
            )}
          </div>
          <Steps items={[t("install.windows.step1"), t("install.windows.step2"), t("install.windows.step3")]} />
          <p className="text-xs text-muted-foreground">{t("install.windows.keepName")}</p>
          <Checksum asset={x64} releaseUrl={d.release_url} verify={'Get-FileHash "$env:USERPROFILE\\Downloads\\rustdesk-licensed-*.exe" -Algorithm SHA256'} />
        </>
      ) : (
        <ImportSteps platform="windows" />
      )}
      {x64 && (
        <details className="rounded-md border p-3">
          <summary className="cursor-pointer text-sm font-medium">{t("install.windows.silentTitle")}</summary>
          <div className="mt-3">
            <CommandBlock title="PowerShell" command={windowsCommand(x64, d.config_string)} hint={t("install.windows.silentHint")} />
          </div>
        </details>
      )}
    </PlatformCard>
  );
}

function MacCard({ d }: { d: ClientDownloads }) {
  const { t } = useTranslation();
  return (
    <PlatformCard icon={<Apple className="h-4 w-4" />} title={t("install.platforms.macos")}>
      <div className="flex flex-wrap gap-2">
        <AssetLink asset={findAsset(d.assets, "macos-arm64")} label={t("install.macos.arm")} />
        <AssetLink asset={findAsset(d.assets, "macos-x64")} label={t("install.macos.intel")} />
      </div>
      <Checksum asset={findAsset(d.assets, "macos-arm64")} releaseUrl={d.release_url} verify="shasum -a 256 ~/Downloads/rustdesk-*.dmg" />
      <Checksum asset={findAsset(d.assets, "macos-x64")} releaseUrl={d.release_url} verify="shasum -a 256 ~/Downloads/rustdesk-*.dmg" />
      <Steps items={[t("install.macos.step1"), t("install.macos.step2")]} />
      <ImportSteps platform="desktop" />
      <CommandBlock title={t("install.orTerminal")} command={macosCommand(d.config_string)} />
    </PlatformCard>
  );
}

function LinuxCard({ d }: { d: ClientDownloads }) {
  const { t } = useTranslation();
  const [arch, setArch] = useState<"x64" | "arm64">("x64");
  const deb = findAsset(d.assets, `linux-deb-${arch}`);
  const rpm = findAsset(d.assets, `linux-rpm-${arch}`);
  return (
    <PlatformCard icon={<Terminal className="h-4 w-4" />} title={t("install.platforms.linux")}>
      <div className="flex flex-wrap gap-1.5">
        {(["x64", "arm64"] as const).map((a) => (
          <Button key={a} type="button" size="sm" variant={arch === a ? "secondary" : "ghost"} onClick={() => setArch(a)}>
            {a === "x64" ? "x86_64" : "ARM64"}
          </Button>
        ))}
      </div>
      {deb && <CommandBlock title={t("install.linux.deb")} command={linuxCommand(deb, d.config_string)} />}
      {rpm && <CommandBlock title={t("install.linux.rpm")} command={linuxCommand(rpm, d.config_string)} />}
      <div className="flex flex-wrap gap-2">
        <AssetLink asset={deb} label=".deb" />
        <AssetLink asset={rpm} label=".rpm" />
        <AssetLink asset={findAsset(d.assets, `linux-appimage-${arch}`)} label="AppImage" />
      </div>
      <p className="text-xs text-muted-foreground">{t("install.linux.manual")}</p>
      <p className="text-xs text-muted-foreground">{t("install.checksum.linux")}</p>
    </PlatformCard>
  );
}

function MobileCard({ d, platform }: { d: ClientDownloads; platform: "android" | "ios" }) {
  const { t } = useTranslation();
  return (
    <PlatformCard icon={<Smartphone className="h-4 w-4" />} title={t(`install.platforms.${platform}`)} description={platform === "ios" ? t("install.mobile.iosNote") : undefined}>
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start">
        <div className="shrink-0 self-center sm:self-start">
          <QrCanvas value={qrValue(d.config_string)} size={208} />
        </div>
        <div className="min-w-0 space-y-3">
          <div className="flex flex-wrap gap-2">
            {platform === "android" ? (
              <AssetLink asset={findAsset(d.assets, "android-universal")} label="APK" />
            ) : (
              <Button asChild variant="outline" size="sm">
                <a href={APP_STORE_URL} target="_blank" rel="noreferrer noopener">
                  <ExternalLink className="h-4 w-4" />
                  App Store
                </a>
              </Button>
            )}
          </div>
          <Steps items={[t(`install.mobile.step1.${platform}`), t("install.mobile.step2"), t("install.mobile.step3")]} />
          {platform === "android" && (
            <Checksum asset={findAsset(d.assets, "android-universal")} releaseUrl={d.release_url} verify="sha256sum rustdesk-*-universal-signed.apk" />
          )}
          <p className="text-xs text-muted-foreground">{t("install.mobile.noCamera")}</p>
        </div>
      </div>
    </PlatformCard>
  );
}

/** How to paste the copied settings into an installed client. */
function ImportSteps({ platform }: { platform: "windows" | "desktop" }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-1.5 rounded-md border bg-muted/20 p-3">
      <div className="text-sm font-medium">{t(platform === "windows" ? "install.import.titleWindows" : "install.import.title")}</div>
      <Steps items={[t("install.import.step1"), t("install.import.step2"), t("install.import.step3")]} />
    </div>
  );
}

function ManualCard({ d }: { d: ClientDownloads }) {
  const { t } = useTranslation();
  const rows: [string, string][] = [
    [t("server.clientConfig.idServer"), d.id_server],
    [t("server.clientConfig.relayServer"), d.relay_server],
    [t("server.clientConfig.apiServer"), d.api_server],
    [t("server.clientConfig.publicKey"), d.public_key],
  ];
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("install.manual.title")}</CardTitle>
        <CardDescription>{t("install.manual.desc")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="relative">
          <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-2.5 pr-10 font-mono text-xs">{d.config_string}</pre>
          <CopyButton value={d.config_string} label={t("install.manual.copyConfig")} className="absolute right-2 top-2" />
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          {rows.map(([label, value]) => (
            <div key={label} className={cn("min-w-0 space-y-1 rounded-md border p-2.5")}>
              <div className="flex items-center justify-between gap-2 text-xs font-medium">
                {label}
                {value && <CopyButton value={value} />}
              </div>
              <Mono className="block truncate text-xs">{value || "—"}</Mono>
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
