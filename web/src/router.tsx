import type { QueryClient } from "@tanstack/react-query";
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  isRedirect,
  lazyRouteComponent,
  Outlet,
  redirect,
  type RouterHistory,
} from "@tanstack/react-router";

import { AppShell } from "@/components/layout/AppShell";
import { ErrorState } from "@/components/common";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/misc";
import { requireAdmin, requireSession } from "@/lib/guards";
import { isServerPath, safeNext } from "@/lib/redirect";
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
  validateSearch: (s: Record<string, unknown>): { next?: string; error?: string; mfa?: true | "email" } => ({
    next: typeof s.next === "string" ? s.next : undefined,
    error: typeof s.error === "string" ? s.error : undefined,
    // OIDC login of a user with TOTP lands on /login?mfa=1 with a pending session.
    // With an e-mailed code instead (no TOTP, e-mail 2FA): /login?mfa=email.
    mfa: s.mfa === "email" ? "email" : s.mfa === 1 || s.mfa === "1" || s.mfa === true ? true : undefined,
  }),
  beforeLoad: async ({ context, search }) => {
    // Already signed in → go straight to the target.
    try {
      await context.queryClient.ensureQueryData(meQuery);
    } catch {
      return;
    }
    const next = safeNext(search.next);
    throw redirect({ href: next, reloadDocument: isServerPath(next) });
  },
  component: LoginPage,
});

const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/reset-password",
  validateSearch: (s: Record<string, unknown>): { token?: string } => ({ token: typeof s.token === "string" ? s.token : undefined }),
  component: lazyRouteComponent(() => import("@/pages/ResetPasswordPage"), "ResetPasswordPage"),
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
const aboutRoute = createRoute({ getParentRoute: app, path: "/about", component: lazyRouteComponent(() => import("@/pages/AboutPage"), "AboutPage") });
const forbiddenRoute = createRoute({ getParentRoute: app, path: "/forbidden", component: ForbiddenPage });

const devicesRoute = createRoute({ getParentRoute: admin, path: "/devices", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/devices/DevicesPage"), "DevicesPage") });
const usersRoute = createRoute({ getParentRoute: admin, path: "/users", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/users/UsersPage"), "UsersPage") });
const groupsRoute = createRoute({ getParentRoute: admin, path: "/groups", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/GroupsPage"), "GroupsPage") });
const booksRoute = createRoute({ getParentRoute: admin, path: "/address-books", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/address-books/AddressBooksPage"), "AddressBooksPage") });
const aclRoute = createRoute({ getParentRoute: admin, path: "/acl", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/acl/AclPage"), "AclPage") });
const auditRoute = createRoute({ getParentRoute: admin, path: "/audit", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/AuditPage"), "AuditPage") });
const idpRoute = createRoute({ getParentRoute: admin, path: "/identity-providers", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/IdentityProvidersPage"), "IdentityProvidersPage") });
const serverRoute = createRoute({ getParentRoute: admin, path: "/server", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/server/ServerPage"), "ServerPage") });
const settingsRoute = createRoute({ getParentRoute: admin, path: "/settings", validateSearch: S, component: lazyRouteComponent(() => import("@/pages/SettingsPage"), "SettingsPage") });

const connectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/connect",
  beforeLoad: async ({ context, location }) => ({ me: await requireSession(context.queryClient, location) }),
  validateSearch: (s: Record<string, unknown>): { id?: string; pwd?: string } => ({
    id: s.id !== undefined && s.id !== null && String(s.id).trim() !== "" ? String(s.id).trim() : undefined,
    pwd: s.pwd !== undefined && s.pwd !== null && String(s.pwd) !== "" ? String(s.pwd) : undefined,
  }),
  component: lazyRouteComponent(() => import("@/pages/webclient/WebClientPage"), "WebClientPage"),
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  resetPasswordRoute,
  connectRoute,
  appRoute.addChildren([
    dashboardRoute,
    profileRoute,
    myDevicesRoute,
    myBooksRoute,
    mySessionsRoute,
    aboutRoute,
    forbiddenRoute,
    adminRoute.addChildren([devicesRoute, usersRoute, groupsRoute, booksRoute, aclRoute, auditRoute, idpRoute, serverRoute, settingsRoute]),
  ]),
]);

export function createAppRouter(queryClient: QueryClient, history?: RouterHistory) {
  return createRouter({
    routeTree,
    context: { queryClient },
    ...(history ? { history } : {}),
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
