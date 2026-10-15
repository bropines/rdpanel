import { QueryCache, QueryClient } from "@tanstack/react-query";

import { ApiError } from "@/api/client";

export function createQueryClient() {
  return new QueryClient({
    queryCache: new QueryCache(),
    defaultOptions: {
      queries: {
        staleTime: 15_000,
        refetchOnWindowFocus: false,
        // Never retry auth/permission/validation failures.
        retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      },
      mutations: { retry: false },
    },
  });
}

export const queryClient = createQueryClient();
