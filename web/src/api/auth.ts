/**
 * /auth/* endpoints (api/openapi.yaml, tag `auth`).
 *
 *   GET  /auth/providers                  → {items: LoginProvider[]}
 *   POST /auth/login {username,password}  → LoginResponse {mfa_required, me?}
 *   POST /auth/mfa {code}                 → LoginResponse (pending session from the password step)
 *   POST /auth/logout                     → 204
 *   GET  /auth/me                         → Me
 *   POST /auth/oidc/{slug}/start {next}   → {url}: the SPA navigates there
 */
import { api, type ApiClient, type Schemas } from "@/api/client";

export type Me = Schemas["Me"];
export type User = Schemas["User"];
export type LoginRequest = Schemas["LoginRequest"];
export type LoginResponse = Schemas["LoginResponse"];
export type AuthProvider = Schemas["LoginProvider"];

export type LoginResult = { kind: "ok"; me: Me } | { kind: "mfa" };

/** Maps a login/MFA response to the next UI step. */
export function loginResult(res: LoginResponse): LoginResult {
  if (res.mfa_required || !res.me) return { kind: "mfa" };
  return { kind: "ok", me: res.me };
}

/** Typed wrappers for the /auth endpoints. */
export function authApi(client: ApiClient = api) {
  return {
    login: async (body: LoginRequest): Promise<LoginResult> =>
      loginResult(await client.request<LoginResponse>("/auth/login", { method: "POST", body })),
    async mfa(code: string): Promise<Me> {
      const res = loginResult(await client.request<LoginResponse>("/auth/mfa", { method: "POST", body: { code } }));
      if (res.kind !== "ok") throw new Error("unexpected MFA response");
      return res.me;
    },
    logout: () => client.request<void>("/auth/logout", { method: "POST" }),
    me: () => client.request<Me>("/auth/me"),
    providers: async (): Promise<AuthProvider[]> =>
      (await client.request<{ items: AuthProvider[] }>("/auth/providers")).items ?? [],
    /** Starts a panel OIDC login; resolves to the IdP URL to navigate to. */
    oidcStart: async (slug: string, next?: string): Promise<string> =>
      (
        await client.request<Schemas["UrlResponse"]>(`/auth/oidc/${encodeURIComponent(slug)}/start`, {
          method: "POST",
          body: next ? { next } : {},
        })
      ).url,
  };
}

export const auth = authApi();

/** Leaves the SPA for an IdP URL (separate function so tests can stub it). */
export function navigateExternal(url: string) {
  window.location.assign(url);
}

export const meQueryKey = ["auth", "me"] as const;
