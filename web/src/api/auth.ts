/**
 * /auth/* endpoints (api/openapi.yaml, tag `auth`).
 *
 *   GET  /auth/providers                  → {items: LoginProvider[]}
 *   POST /auth/login {username,password}  → LoginResponse {mfa_required, me?}
 *   POST /auth/mfa {code}                 → LoginResponse (pending session from the password step)
 *   POST /auth/logout                     → 204
 *   GET  /auth/me                         → Me
 *   POST /auth/oidc/{slug}/start {next}   → {url}: the SPA navigates there
 *   POST /auth/passkey/{begin,finish}     → usernameless passkey sign-in (LoginResponse)
 *   POST /auth/mfa/passkey/{begin,finish} → passkey as the second factor (LoginResponse)
 */
import { api, type ApiClient, type Schemas } from "@/api/client";
import { getPasskey, type JsonObject } from "@/lib/webauthn";

export type Me = Schemas["Me"];
export type User = Schemas["User"];
export type LoginRequest = Schemas["LoginRequest"];
export type LoginResponse = Schemas["LoginResponse"];
export type AuthProvider = Schemas["LoginProvider"];

export type MfaMethod = NonNullable<LoginResponse["mfa_methods"]>[number];
export type LoginResult = { kind: "ok"; me: Me } | { kind: "mfa"; methods?: MfaMethod[] };
export type LoginOptions = { providers: AuthProvider[]; passkey: boolean };
export type PasskeyCeremony = Schemas["PasskeyCeremony"];

/** Maps a login/MFA response to the next UI step. */
export function loginResult(res: LoginResponse): LoginResult {
  if (res.mfa_required || !res.me) return { kind: "mfa", methods: res.mfa_methods };
  return { kind: "ok", me: res.me };
}

function okMe(res: LoginResponse): Me {
  const r = loginResult(res);
  if (r.kind !== "ok") throw new Error("unexpected login response");
  return r.me;
}

/** Typed wrappers for the /auth endpoints. */
export function authApi(client: ApiClient = api) {
  return {
    login: async (body: LoginRequest): Promise<LoginResult> =>
      loginResult(await client.request<LoginResponse>("/auth/login", { method: "POST", body })),
    async mfa(code: string): Promise<Me> {
      return okMe(await client.request<LoginResponse>("/auth/mfa", { method: "POST", body: { code } }));
    },
    /** Usernameless sign-in: the authenticator picks the account. */
    async passkeyLogin(): Promise<Me> {
      const c = await client.request<PasskeyCeremony>("/auth/passkey/begin", { method: "POST" });
      const credential = await getPasskey(c.options as JsonObject);
      return okMe(await client.request<LoginResponse>("/auth/passkey/finish", { method: "POST", body: { ceremony_id: c.ceremony_id, credential } }));
    },
    /** Completes a pending login (password/OIDC step done) with a passkey. */
    async passkeyMfa(): Promise<Me> {
      const c = await client.request<PasskeyCeremony>("/auth/mfa/passkey/begin", { method: "POST" });
      const credential = await getPasskey(c.options as JsonObject);
      return okMe(await client.request<LoginResponse>("/auth/mfa/passkey/finish", { method: "POST", body: { ceremony_id: c.ceremony_id, credential } }));
    },
    logout: () => client.request<void>("/auth/logout", { method: "POST" }),
    me: () => client.request<Me>("/auth/me"),
    providers: async (): Promise<AuthProvider[]> =>
      (await client.request<{ items: AuthProvider[] }>("/auth/providers")).items ?? [],
    /** Login page options: provider buttons and whether passkey sign-in is offered. */
    loginOptions: async (): Promise<LoginOptions> => {
      const r = await client.request<{ items: AuthProvider[]; passkey?: boolean }>("/auth/providers");
      return { providers: r.items ?? [], passkey: !!r.passkey };
    },
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
