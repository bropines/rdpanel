import { createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";

import { AppShell } from "@/components/layout/AppShell";
import { Toaster } from "@/components/ui/sonner";
import { NAV_ITEMS } from "@/nav";
import { LoginPage } from "@/pages/LoginPage";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { PlaceholderPage } from "@/pages/PlaceholderPage";

const rootRoute = createRootRoute({
  component: () => (
    <>
      <Outlet />
      <Toaster richColors closeButton />
    </>
  ),
  notFoundComponent: NotFoundPage,
});

const loginRoute = createRoute({ getParentRoute: () => rootRoute, path: "/login", component: LoginPage });

// Pathless layout for authenticated pages.
// TODO(stream D): beforeLoad → ensure /auth/me succeeds, else redirect to /login;
// guard System pages by role.
const appRoute = createRoute({ getParentRoute: () => rootRoute, id: "app", component: AppShell });

const pageRoutes = NAV_ITEMS.map((item) =>
  createRoute({
    getParentRoute: () => appRoute,
    path: item.path,
    component: () => <PlaceholderPage titleKey={item.labelKey} />,
  }),
);

const routeTree = rootRoute.addChildren([loginRoute, appRoute.addChildren(pageRoutes)]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
