import { api, type ApiClient, type Schemas } from "@/api/client";

export type LoginRequest = Schemas["LoginRequest"];
export type LoginResponse = Schemas["LoginResponse"];
export type Me = Schemas["Me"];

/** Typed wrappers for the /auth endpoints (contract: api/openapi.yaml). */
export function authApi(client: ApiClient = api) {
  return {
    /** `mfa_required: true` means: ask for a code, then call `mfa` (or re-login with `totp_code`). */
    login: (body: LoginRequest) => client.request<LoginResponse>("/auth/login", { method: "POST", body }),
    mfa: (code: string) => client.request<LoginResponse>("/auth/mfa", { method: "POST", body: { code } }),
    logout: () => client.request<void>("/auth/logout", { method: "POST" }),
    me: () => client.request<Me>("/auth/me"),
  };
}

export const auth = authApi();
