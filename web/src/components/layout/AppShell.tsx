import { Link, Outlet } from "@tanstack/react-router";
import { LogOut, Menu, MonitorCog, UserRound, X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { LanguageSwitcher } from "@/components/layout/LanguageSwitcher";
import { ThemeToggle } from "@/components/layout/ThemeToggle";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import { visibleSections } from "@/nav";

// TODO(stream D): take the role from the /auth/me query once the admin API exists.
const IS_ADMIN = true;

function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation();
  return (
    <nav className="flex flex-col gap-6 p-4" aria-label={t("app.title")}>
      {visibleSections(IS_ADMIN).map((section) => (
        <div key={section.titleKey}>
          <div className="px-2 pb-2 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            {t(section.titleKey)}
          </div>
          <ul className="flex flex-col gap-0.5">
            {section.items.map((item) => (
              <li key={item.path}>
                <Link
                  to={item.path}
                  onClick={onNavigate}
                  activeOptions={{ exact: item.path === "/" }}
                  className="flex items-center gap-3 rounded-md px-2 py-1.5 text-sm text-sidebar-foreground transition-colors hover:bg-sidebar-accent"
                  activeProps={{ className: "bg-sidebar-accent font-medium text-foreground" }}
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

function Brand() {
  const { t } = useTranslation();
  return (
    <Link to="/" className="flex items-center gap-2 font-semibold">
      <MonitorCog className="h-5 w-5 text-primary" aria-hidden />
      <span>{t("app.title")}</span>
    </Link>
  );
}

/** Authenticated layout: sidebar (drawer on mobile), top bar, routed content. */
export function AppShell() {
  const { t } = useTranslation();
  const [mobileOpen, setMobileOpen] = useState(false);

  return (
    <div className="flex min-h-screen bg-background">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:z-50 focus:rounded focus:bg-background focus:p-2"
      >
        {t("app.skipToContent")}
      </a>

      <aside className="hidden w-64 shrink-0 border-r border-sidebar-border bg-sidebar md:block">
        <div className="flex h-14 items-center border-b border-sidebar-border px-4">
          <Brand />
        </div>
        <Sidebar />
      </aside>

      {mobileOpen && (
        <div className="fixed inset-0 z-40 md:hidden" role="dialog" aria-modal="true">
          <div className="absolute inset-0 bg-black/50" onClick={() => setMobileOpen(false)} aria-hidden />
          <aside className="absolute inset-y-0 left-0 w-72 overflow-y-auto bg-sidebar shadow-xl">
            <div className="flex h-14 items-center justify-between border-b border-sidebar-border px-4">
              <Brand />
              <Button variant="ghost" size="icon" onClick={() => setMobileOpen(false)} aria-label={t("nav.closeMenu")}>
                <X />
              </Button>
            </div>
            <Sidebar onNavigate={() => setMobileOpen(false)} />
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/95 px-4 backdrop-blur">
          <Button
            variant="ghost"
            size="icon"
            className="md:hidden"
            onClick={() => setMobileOpen(true)}
            aria-label={t("nav.openMenu")}
          >
            <Menu />
          </Button>
          <div className="md:hidden">
            <Brand />
          </div>
          <div className="ml-auto flex items-center gap-1">
            <LanguageSwitcher />
            <ThemeToggle />
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="ghost" size="icon" aria-label={t("user.menu")}>
                  <UserRound />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem asChild>
                  <Link to="/login">
                    <LogOut />
                    {t("user.logout")}
                  </Link>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <main id="main" className={cn("flex-1 p-4 md:p-6")}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}
