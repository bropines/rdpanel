import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { BookOpen, Info, LogOut, Menu, MonitorCog, ShieldCheck, X } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { auth } from "@/api/auth";
import { aboutQuery, dashboardApi } from "@/api/system";
import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { API_DOCS_URL } from "@/lib/api-docs";
import { LiveProvider, useLiveStatus } from "@/lib/live";
import { displayName, useMe } from "@/lib/session";
import { cn } from "@/lib/utils";
import { visibleSections } from "@/nav";

function useBranding() {
  return useQuery({ queryKey: ["branding"], queryFn: () => dashboardApi.branding(), staleTime: 5 * 60_000 });
}

function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  const me = useMe();
  return (
    <nav className="flex flex-col gap-5 p-3" aria-label={t("nav.main")}>
      {visibleSections(me.user.role === "admin").map((section) => (
        <div key={section.titleKey}>
          <div className="px-2 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            {t(section.titleKey)}
          </div>
          <ul className="flex flex-col gap-0.5">
            {section.items.map((item) => (
              <li key={item.path}>
                <Link
                  to={item.path}
                  onClick={onNavigate}
                  activeOptions={{ exact: item.path === "/", includeSearch: false }}
                  className="flex items-center gap-2.5 rounded-md px-2 py-1.5 text-sm text-sidebar-foreground transition-colors hover:bg-sidebar-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  activeProps={{ className: "bg-sidebar-accent font-medium !text-foreground" }}
                >
                  <item.icon className="h-4 w-4 shrink-0" aria-hidden />
                  <span className="truncate">{t(item.labelKey)}</span>
                </Link>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </nav>
  );
}

/** Sidebar footer: running version (links to About), with a dot when a release is newer. */
function VersionFooter({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  const { data } = useQuery(aboutQuery);
  const update = data?.update.status === "update_available";
  return (
    <div className="mt-auto border-t border-sidebar-border px-3 py-2">
      <Link
        to="/about"
        onClick={onNavigate}
        className="flex items-center gap-2 rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-sidebar-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        activeProps={{ className: "bg-sidebar-accent !text-foreground" }}
        title={update ? t("about.updates.update_available", { latest: data?.update.latest ?? "" }) : t("nav.about")}
      >
        <Info className="h-3.5 w-3.5 shrink-0" aria-hidden />
        <span className="truncate" data-testid="sidebar-version">
          {data ? t("about.sidebarVersion", { version: data.version.panel }) : t("nav.about")}
        </span>
        {update && (
          <span className="ml-auto h-2 w-2 shrink-0 rounded-full bg-primary" role="img" aria-label={t("about.updates.badge")} />
        )}
      </Link>
    </div>
  );
}

function Brand() {
  const { t } = useTranslation();
  const { data } = useBranding();
  return (
    <Link to="/" className="flex min-w-0 items-center gap-2 font-semibold">
      {data?.logo_url ? (
        <img src={data.logo_url} alt="" className="h-6 w-6 rounded object-contain" />
      ) : (
        <MonitorCog className="h-5 w-5 shrink-0 text-primary" aria-hidden />
      )}
      <span className="truncate">{data?.title || t("app.title")}</span>
    </Link>
  );
}

function LiveIndicator() {
  const { t } = useTranslation();
  const status = useLiveStatus();
  const label = t(`live.${status}`);
  return (
    <span
      className="hidden items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs text-muted-foreground sm:inline-flex"
      title={label}
      aria-live="polite"
    >
      <span
        className={cn(
          "h-1.5 w-1.5 rounded-full",
          status === "open" ? "bg-emerald-500" : status === "closed" ? "bg-zinc-400" : "animate-pulse bg-amber-500",
        )}
      />
      {label}
    </span>
  );
}

function UserMenu() {
  const { t } = useTranslation();
  const me = useMe();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const logout = useMutation({
    mutationFn: () => auth.logout(),
    onSettled: () => {
      qc.clear();
      void navigate({ to: "/login" });
    },
  });
  const name = displayName(me);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" className="h-9 gap-2 px-2" aria-label={t("user.menu")}>
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary/15 text-xs font-semibold uppercase text-primary">
            {name.slice(0, 2)}
          </span>
          <span className="hidden max-w-[10rem] truncate text-sm lg:inline">{name}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel className="font-normal">
          <div className="truncate text-sm font-medium">{name}</div>
          <div className="truncate text-xs text-muted-foreground">
            {me.user.username} · {t(`roles.${me.user.role}`)}
          </div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem asChild>
          <Link to="/my/profile">
            <ShieldCheck />
            {t("nav.myProfile")}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to="/about">
            <Info />
            {t("nav.about")}
          </Link>
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <a href={API_DOCS_URL}>
            <BookOpen />
            {t("apiDocs.title")}
          </a>
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => logout.mutate()} disabled={logout.isPending}>
          <LogOut />
          {t("user.logout")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** Authenticated layout: sidebar (drawer on mobile), top bar, routed content. */
export function AppShell() {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { data: branding } = useBranding();

  useEffect(() => setMobileOpen(false), [pathname]);
  useEffect(() => {
    document.title = branding?.title || t("app.title");
  }, [branding?.title, t]);

  return (
    <LiveProvider>
      <div className="flex min-h-screen bg-background">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded focus:bg-background focus:p-2"
        >
          {t("app.skipToContent")}
        </a>

        <aside className="sticky top-0 hidden h-screen w-60 shrink-0 flex-col overflow-y-auto border-r border-sidebar-border bg-sidebar md:flex">
          <div className="flex h-14 shrink-0 items-center border-b border-sidebar-border px-4">
            <Brand />
          </div>
          <Sidebar />
          <VersionFooter />
        </aside>

        {mobileOpen && (
          <div className="fixed inset-0 z-40 md:hidden" role="dialog" aria-modal="true" aria-label={t("nav.main")}>
            <div className="absolute inset-0 bg-black/50" onClick={() => setMobileOpen(false)} aria-hidden />
            <aside className="absolute inset-y-0 left-0 flex w-72 flex-col overflow-y-auto bg-sidebar shadow-xl">
              <div className="flex h-14 shrink-0 items-center justify-between border-b border-sidebar-border px-4">
                <Brand />
                <Button variant="ghost" size="icon" onClick={() => setMobileOpen(false)} aria-label={t("nav.closeMenu")}>
                  <X />
                </Button>
              </div>
              <Sidebar onNavigate={() => setMobileOpen(false)} />
              <VersionFooter onNavigate={() => setMobileOpen(false)} />
            </aside>
          </div>
        )}

        <div className="flex min-w-0 flex-1 flex-col">
          <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/95 px-3 backdrop-blur md:px-5">
            <Button
              variant="ghost"
              size="icon"
              className="md:hidden"
              onClick={() => setMobileOpen(true)}
              aria-label={t("nav.openMenu")}
            >
              <Menu />
            </Button>
            <div className="min-w-0 md:hidden">
              <Brand />
            </div>
            <div className="ml-auto flex items-center gap-1">
              <LiveIndicator />
              <LanguageSwitcher />
              <ThemeToggle />
              <UserMenu />
            </div>
          </header>
          <main id="main" className="mx-auto w-full max-w-[1600px] flex-1 p-3 md:p-6">
            <Outlet />
          </main>
        </div>
      </div>
    </LiveProvider>
  );
}
