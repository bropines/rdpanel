/**
 * /auth/* endpoints. Owned by stream B1 in api/openapi.yaml — the shapes below are
 * what the UI assumes; reconcile with the final contract by editing only this file.
 *
 *   GET  /auth/providers                  → AuthProvider[] (or {items: AuthProvider[]})
 *   POST /auth/login {username,password}  → Me | {mfa_required: true, methods?}
 *                                           (401 code `totp_required` is treated the same)
 *   POST /auth/mfa {code}                 → Me      (TOTP or recovery code; session cookie
 *                                                    from the password step is mfa_pending)
 *   POST /auth/logout                     → 204
 *   GET  /auth/me                         → Me
 *   GET  /auth/oidc/{slug}/start?next=&action=login|bind → 302 to the IdP (browser navigation)
 */
import { api, API_BASE, ApiError, type ApiClient, type Schemas } from "@/api/client";

export type Me = Schemas["Me"];
export type User = Schemas["User"];

export interface LoginRequest {
  username: string;
  password: string;
}

export type ProviderKind = "github" | "google" | "oidc" | "linuxdo";

export interface AuthProvider {
  slug: string;
  display_name: string;
  kind: ProviderKind | string;
}

export interface MfaChallenge {
  mfa_required: true;
  methods?: ("totp" | "recovery")[];
}

export type LoginResult = { kind: "ok"; me: Me } | { kind: "mfa"; methods: ("totp" | "recovery")[] };

/** True when a login response asks for the second factor. */
export function isMfaChallenge(r: unknown): r is MfaChallenge {
  if (!r || typeof r !== "object") return false;
  const o = r as Record<string, unknown>;
  return o.mfa_required === true || o.status === "mfa_required";
}

/** Typed wrappers for the /auth endpoints. */
export function authApi(client: ApiClient = api) {
  return {
    async login(body: LoginRequest): Promise<LoginResult> {
      try {
        const res = await client.request<Me | MfaChallenge>("/auth/login", { method: "POST", body });
        if (isMfaChallenge(res)) return { kind: "mfa", methods: res.methods ?? ["totp", "recovery"] };
        return { kind: "ok", me: res };
      } catch (err) {
        if (err instanceof ApiError && (err.code === "totp_required" || err.code === "mfa_required")) {
          return { kind: "mfa", methods: ["totp", "recovery"] };
        }
        throw err;
      }
    },
    mfa: (code: string) => client.request<Me>("/auth/mfa", { method: "POST", body: { code } }),
    logout: () => client.request<void>("/auth/logout", { method: "POST" }),
    me: () => client.request<Me>("/auth/me"),
    async providers(): Promise<AuthProvider[]> {
      const res = await client.request<AuthProvider[] | { items: AuthProvider[] }>("/auth/providers");
      return Array.isArray(res) ? res : (res?.items ?? []);
    },
  };
}

export const auth = authApi();

/** Browser URL that starts an OIDC flow (login, or linking to the current account). */
export function oidcStartUrl(slug: string, opts: { next?: string; action?: "login" | "bind" } = {}): string {
  const qs = new URLSearchParams();
  if (opts.next) qs.set("next", opts.next);
  if (opts.action && opts.action !== "login") qs.set("action", opts.action);
  const s = qs.toString();
  return `${API_BASE}/auth/oidc/${encodeURIComponent(slug)}/start${s ? `?${s}` : ""}`;
}

export const meQueryKey = ["auth", "me"] as const;
