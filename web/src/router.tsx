import type { QueryClient } from "@tanstack/react-query";
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  isRedirect,
  lazyRouteComponent,
  Outlet,
  redirect,
} from "@tanstack/react-router";

import { AppShell } from "@/components/layout/AppShell";
import { ErrorState } from "@/components/common";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/misc";
import { requireAdmin, requireSession } from "@/lib/guards";
import { safeNext } from "@/lib/redirect";
import { meQuery } from "@/lib/session";
import { ForbiddenPage } from "@/pages/ForbiddenPage";
import { LoginPage } from "@/pages/LoginPage";
import { NotFoundPage } from "@/pages/NotFoundPage";

export interface RouterContext {
  queryClient: QueryClient;
}

/** Keeps every search param (tables validate their own keys). */
const passthrough = (s: Record<string, unknown>) => s;

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => (
    <TooltipProvider delayDuration={300}>
      <Outlet />
      <Toaster richColors closeButton />
    </TooltipProvider>
  ),
  notFoundComponent: NotFoundPage,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): { next?: string; error?: string } => ({
    next: typeof s.next === "string" ? s.next : undefined,
    error: typeof s.error === "string" ? s.error : undefined,
  }),
  beforeLoad: async ({ context, search }) => {
    // Already signed in → go straight to the target.
    try {
      await context.queryClient.ensureQueryData(meQuery);
    } catch {
      return;
    }
    throw redirect({ href: safeNext(search.next) });
  },
  component: LoginPage,
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async ({ context, location }) => ({ me: await requireSession(context.queryClient, location) }),
  component: AppShell,
  errorComponent: ({ error, reset }) => (isRedirect(error) ? null : <ErrorState error={error} onRetry={reset} className="min-h-screen" />),
});

const adminRoute = createRoute({
  getParentRoute: () => appRoute,
  id: "admin",
  beforeLoad: ({ context }) => requireAdmin(context.me),
});

const app = () => appRoute;
const admin = () => adminRoute;
const S = passthrough;

const dashboardRoute = createRoute({ getParentRoute: app, path: "/", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/DashboardPage"), "DashboardPage") });
const profileRoute = createRoute({ getParentRoute: app, path: "/my/profile", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/my/ProfilePage"), "ProfilePage") });
const myDevicesRoute = createRoute({ getParentRoute: app, path: "/my/devices", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/devices/DevicesPage"), "MyDevicesPage") });
const myBooksRoute = createRoute({ getParentRoute: app, path: "/my/address-books", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/address-books/AddressBooksPage"), "MyAddressBooksPage") });
const mySessionsRoute = createRoute({ getParentRoute: app, path: "/my/sessions", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/my/SessionsPage"), "MySessionsPage") });
const forbiddenRoute = createRoute({ getParentRoute: app, path: "/forbidden", component: ForbiddenPage });

const devicesRoute = createRoute({ getParentRoute: admin, path: "/devices", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/devices/DevicesPage"), "DevicesPage") });
const usersRoute = createRoute({ getParentRoute: admin, path: "/users", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/users/UsersPage"), "UsersPage") });
const groupsRoute = createRoute({ getParentRoute: admin, path: "/groups", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/GroupsPage"), "GroupsPage") });
const booksRoute = createRoute({ getParentRoute: admin, path: "/address-books", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/address-books/AddressBooksPage"), "AddressBooksPage") });
const auditRoute = createRoute({ getParentRoute: admin, path: "/audit", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/AuditPage"), "AuditPage") });
const idpRoute = createRoute({ getParentRoute: admin, path: "/identity-providers", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/IdentityProvidersPage"), "IdentityProvidersPage") });
const serverRoute = createRoute({ getParentRoute: admin, path: "/server", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/server/ServerPage"), "ServerPage") });
const settingsRoute = createRoute({ getParentRoute: admin, path: "/settings", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/SettingsPage"), "SettingsPage") });

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    dashboardRoute,
    profileRoute,
    myDevicesRoute,
    myBooksRoute,
    mySessionsRoute,
    forbiddenRoute,
    adminRoute.addChildren([devicesRoute, usersRoute, groupsRoute, booksRoute, auditRoute, idpRoute, serverRoute, settingsRoute]),
  ]),
]);

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
  });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module "@tanstack/react-router" {
  interface Register {
    router: AppRouter;
  }
}
