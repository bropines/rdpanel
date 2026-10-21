import { Apple, Check, ClipboardCopy, Download, ExternalLink, Link2Off, Monitor, RotateCw, Smartphone, Terminal } from "lucide-react";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { fetchShareInfo, type ShareInfoResult, type SharePublicInfo } from "@/api/share";
import { copyText, CopyButton, Mono } from "@/components/common";
import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { QrCanvas } from "@/components/QrCanvas";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { detectPlatform, formatSize, PLATFORMS, qrValue, type PlatformId } from "@/pages/my/install";
import { customTitle, GITHUB_RELEASES_URL, platformDownloads, type ShareDownload } from "@/share/page";

type State = ShareInfoResult | { kind: "loading" };

/** The public page of a server share link. */
export function SharePage({ token }: { token: string | null }) {
  const { t } = useTranslation();
  const [state, setState] = useState<State>({ kind: "loading" });

  const load = useCallback(async () => {
    if (!token) {
      setState({ kind: "invalid" });
      return;
    }
    setState({ kind: "loading" });
    setState(await fetchShareInfo(token));
  }, [token]);
  useEffect(() => {
    void load();
  }, [load]);

  const title =
    state.kind === "ok"
      ? (customTitle(state.info) ?? t("share.public.defaultTitle", { host: state.info.id_server }))
      : state.kind === "invalid"
        ? t("share.public.invalidTitle")
        : "RustDesk";
  useEffect(() => {
    document.title = title;
  }, [title]);

  return (
    <div className="min-h-screen bg-muted/30">
      <header className="border-b bg-background/95">
        <div className="mx-auto flex h-14 max-w-3xl items-center justify-between gap-2 px-4">
          <div className="flex min-w-0 items-center gap-2 text-sm font-semibold">
            <Monitor className="h-4 w-4 shrink-0 text-primary" />
            <span className="truncate">RustDesk</span>
          </div>
          <div className="flex items-center">
            <LanguageSwitcher />
            <ThemeToggle />
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-3xl space-y-4 px-4 py-6 sm:py-10">
        {state.kind === "loading" && (
          <div className="space-y-4" aria-busy="true">
            <Skeleton className="h-9 w-2/3" />
            <Skeleton className="h-56" />
            <Skeleton className="h-48" />
          </div>
        )}
        {state.kind === "invalid" && <InvalidLink />}
        {(state.kind === "error" || state.kind === "rate_limited") && (
          <Card>
            <CardContent className="flex flex-col items-center gap-3 p-8 text-center">
              <p className="text-sm text-muted-foreground">
                {state.kind === "rate_limited" ? t("share.public.rateLimited") : t("share.public.loadFailed")}
              </p>
              <Button variant="outline" onClick={() => void load()}>
                <RotateCw />
                {t("share.public.retry")}
              </Button>
            </CardContent>
          </Card>
        )}
        {state.kind === "ok" && token && <ShareContent info={state.info} token={token} title={title} />}
      </main>
    </div>
  );
}

function InvalidLink() {
  const { t } = useTranslation();
  return (
    <Card className="mx-auto max-w-md">
      <CardContent className="flex flex-col items-center gap-3 p-8 text-center">
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
          <Link2Off className="h-6 w-6 text-muted-foreground" />
        </div>
        <h1 className="text-lg font-semibold">{t("share.public.invalidTitle")}</h1>
        <p className="text-sm text-muted-foreground">{t("share.public.invalidText")}</p>
      </CardContent>
    </Card>
  );
}

function ShareContent({ info, token, title }: { info: SharePublicInfo; token: string; title: string }) {
  const { t } = useTranslation();
  const [detected] = useState(() => detectPlatform(navigator.userAgent, navigator.maxTouchPoints));
  const [platform, setPlatform] = useState<PlatformId>(detected);
  // On a phone its own screen cannot be scanned: the QR code goes last there.
  const onPhone = detected === "android" || detected === "ios";

  return (
    <>
      <section className="space-y-2">
        <h1 className="break-words text-2xl font-semibold tracking-tight sm:text-3xl">{title}</h1>
        <p className="whitespace-pre-line break-words text-sm text-muted-foreground sm:text-base">{info.description || t("share.public.intro")}</p>
      </section>

      <Card>
        <CardContent className="space-y-5 p-4 sm:p-6">
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
          <PlatformSection info={info} token={token} platform={platform} />
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-4 md:grid-cols-[auto_minmax(0,1fr)]">
        <Card className={onPhone ? "order-last md:order-first" : undefined}>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <Smartphone className="h-4 w-4" />
              {t("share.public.qrTitle")}
            </CardTitle>
            <CardDescription className="max-w-[16rem]">{t("share.public.qrHint")}</CardDescription>
          </CardHeader>
          <CardContent className="flex justify-center md:justify-start">
            <QrCanvas value={qrValue(info.config_string)} size={184} label={t("share.public.qrTitle")} />
          </CardContent>
        </Card>
        <ManualSettings info={info} />
      </div>

      <p className="text-center text-xs text-muted-foreground sm:text-left">
        {info.version ? t("share.public.official", { version: info.version }) : t("share.public.officialNoVersion")}{" "}
        {info.release_url && (
          <a className="underline-offset-4 hover:underline" href={info.release_url} target="_blank" rel="noreferrer noopener">
            {t("install.releaseNotes")}
          </a>
        )}
      </p>
    </>
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

function DownloadButtons({ downloads }: { downloads: ShareDownload[] }) {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
      {downloads.map((d, i) => {
        const label = d.literal ? d.label : t(d.label);
        return (
          <Button key={d.href} asChild size={i === 0 ? "lg" : "sm"} variant={i === 0 ? "default" : "outline"} className={i === 0 ? "h-12 text-base" : undefined}>
            <a
              href={d.href}
              {...(d.preconfigured ? { download: "" } : { target: "_blank", rel: "noreferrer noopener" })}
            >
              {d.external && !d.preconfigured && i !== 0 ? <ExternalLink className="h-4 w-4" /> : <Download className="h-4 w-4" />}
              {label}
              {d.size ? <span className="text-xs opacity-80">{formatSize(d.size)}</span> : null}
            </a>
          </Button>
        );
      })}
    </div>
  );
}

function GitHubButton({ info }: { info: SharePublicInfo }) {
  const { t } = useTranslation();
  return (
    <Button asChild size="lg" className="h-12 w-full text-base sm:w-auto">
      <a href={info.release_url || GITHUB_RELEASES_URL} target="_blank" rel="noreferrer noopener">
        <ExternalLink className="h-4 w-4" />
        {t("share.public.github")}
      </a>
    </Button>
  );
}

function PlatformSection({ info, token, platform }: { info: SharePublicInfo; token: string; platform: PlatformId }) {
  const { t } = useTranslation();
  const downloads = platformDownloads(info, platform, token);
  const icon =
    platform === "macos" ? <Apple className="h-4 w-4" /> : platform === "linux" ? <Terminal className="h-4 w-4" /> : platform === "windows" ? <Monitor className="h-4 w-4" /> : <Smartphone className="h-4 w-4" />;
  const heading = (
    <h2 className="flex items-center gap-2 text-base font-semibold">
      {icon}
      {t(`install.platforms.${platform}`)}
    </h2>
  );

  if (platform === "windows" && downloads.length > 0) {
    return (
      <div className="space-y-4">
        {heading}
        <DownloadButtons downloads={downloads} />
        <Steps items={[t("share.public.windows.step1"), t("share.public.windows.step2"), t("share.public.windows.step3")]} />
        <p className="text-xs text-muted-foreground">{t("share.public.windows.keepName")}</p>
      </div>
    );
  }
  const mobile = platform === "android" || platform === "ios";
  return (
    <div className="space-y-4">
      {heading}
      {platform === "windows" && <p className="text-sm text-muted-foreground">{t("share.public.windows.unavailable")}</p>}
      {downloads.length > 0 ? <DownloadButtons downloads={downloads} /> : <GitHubButton info={info} />}
      {platform === "macos" && <Steps items={[t("install.macos.step1"), t("install.macos.step2")]} />}
      {platform === "linux" && <p className="text-sm">{t("share.public.linux.step1")}</p>}
      {platform === "android" && <p className="text-sm">{t("share.public.android.step1")}</p>}
      {platform === "ios" && <p className="text-sm">{t("share.public.ios.step1")}</p>}
      {mobile ? <MobileApply /> : <DesktopApply configString={info.config_string} />}
    </div>
  );
}

function CopySettingsButton({ value }: { value: string }) {
  const { t } = useTranslation();
  const [done, setDone] = useState(false);
  return (
    <Button
      type="button"
      size="sm"
      variant="secondary"
      className="ml-1 h-7 align-middle"
      onClick={async () => {
        if (await copyText(value)) {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        }
      }}
    >
      {done ? <Check className="text-emerald-600" /> : <ClipboardCopy />}
      {done ? t("share.public.copied") : t("share.public.copySettings")}
    </Button>
  );
}

function DesktopApply({ configString }: { configString: string }) {
  const { t } = useTranslation();
  return (
    <div className="space-y-2 rounded-md border bg-muted/30 p-3">
      <div className="text-sm font-medium">{t("share.public.apply.title")}</div>
      <Steps
        items={[
          <>
            {t("share.public.apply.step1")}
            <CopySettingsButton value={configString} />
          </>,
          t("share.public.apply.step2"),
          t("share.public.apply.step3"),
        ]}
      />
    </div>
  );
}

function MobileApply() {
  const { t } = useTranslation();
  return (
    <div className="space-y-2 rounded-md border bg-muted/30 p-3">
      <div className="text-sm font-medium">{t("share.public.apply.title")}</div>
      <Steps items={[t("share.public.mobileApply.step1"), t("share.public.mobileApply.step2")]} />
    </div>
  );
}

function ManualSettings({ info }: { info: SharePublicInfo }) {
  const { t } = useTranslation();
  const rows: [string, string][] = [
    [t("server.clientConfig.idServer"), info.id_server],
    [t("server.clientConfig.relayServer"), info.relay_server],
    [t("server.clientConfig.apiServer"), info.api_server],
    [t("server.clientConfig.publicKey"), info.public_key],
  ];
  return (
    <Card className="min-w-0">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("share.public.manualTitle")}</CardTitle>
        <CardDescription>{t("share.public.manualHint")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2 text-xs font-medium">
            {t("share.public.configString")}
            <CopyButton value={info.config_string} label={t("share.public.copySettings")} />
          </div>
          <pre className="max-h-28 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/50 p-2.5 font-mono text-xs">{info.config_string}</pre>
        </div>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          {rows.map(([label, value]) => (
            <div key={label} className="min-w-0 space-y-1 rounded-md border p-2.5">
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
