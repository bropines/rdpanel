import type { QueryClient } from "@tanstack/react-query";
import { redirect } from "@tanstack/react-router";

import type { Me } from "@/api/auth";
import { ApiError } from "@/api/client";
import { safeNext } from "@/lib/redirect";
import { meQuery } from "@/lib/session";

/** Where a session-less visitor is sent: /login with a relative `next`. */
export function loginRedirect(href: string) {
  const next = safeNext(href, "/");
  return redirect({ to: "/login", search: next === "/" ? {} : { next } });
}

/**
 * Route guard for the authenticated area: loads /auth/me (cached), redirects to
 * /login on 401, and to the profile page while 2FA enrolment is mandatory.
 */
export async function requireSession(queryClient: QueryClient, location: { href: string; pathname: string }): Promise<Me> {
  let me: Me;
  try {
    me = await queryClient.ensureQueryData(meQuery);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) throw loginRedirect(location.href);
    throw err;
  }
  if (me.must_enroll_totp && location.pathname !== "/my/profile") {
    throw redirect({ to: "/my/profile", search: { enroll: true } });
  }
  return me;
}

/** Route guard for System pages: non-admins get the 403 page. */
export function requireAdmin(me: Me) {
  if (me.user.role !== "admin") throw redirect({ to: "/forbidden" });
}
