import { api, type ApiClient, type Schemas } from "@/api/client";

export type LoginRequest = Schemas["LoginRequest"];
export type Me = Schemas["Me"];

/** Typed wrappers for the /auth endpoints (contract: api/openapi.yaml). */
export function authApi(client: ApiClient = api) {
  return {
    login: (body: LoginRequest) => client.request<Me>("/auth/login", { method: "POST", body }),
    logout: () => client.request<void>("/auth/logout", { method: "POST" }),
    me: () => client.request<Me>("/auth/me"),
  };
}

export const auth = authApi();
