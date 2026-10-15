import { queryOptions, useQuery } from "@tanstack/react-query";

import { auth, meQueryKey, type Me } from "@/api/auth";

export const meQuery = queryOptions({
  queryKey: meQueryKey,
  queryFn: () => auth.me(),
  staleTime: 60_000,
  retry: false,
});

/** The authenticated user. Only call below the auth guard (the query is pre-loaded). */
export function useMe(): Me {
  const { data } = useQuery(meQuery);
  if (!data) throw new Error("useMe used outside the authenticated area");
  return data;
}

export function useIsAdmin(): boolean {
  return useMe().user.role === "admin";
}

/** Display name used in greetings. */
export function displayName(me: Me): string {
  return me.user.display_name || me.user.username;
}
