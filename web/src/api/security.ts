/**
 * "My profile / security" endpoints (api/openapi.yaml, tag `security`).
 *
 *   PATCH  /me {display_name?, email?}                              → User
 *   GET    /me/security                                             → SecurityOverview
 *   POST   /me/security/password {current_password, new_password}   → 204 (other sessions revoked)
 *   POST   /me/security/totp/enroll                                 → {secret, otpauth_uri}
 *   POST   /me/security/totp/confirm {code}                         → {recovery_codes}
 *   POST   /me/security/totp/disable {password?, code?}             → 204
 *   POST   /me/security/recovery-codes {password?, code?}           → {recovery_codes}
 *   GET    /me/security/sessions                                    → {items: Session[]}
 *   DELETE /me/security/sessions/{id}                               → 204
 *   POST   /me/security/sessions/revoke-others                      → {revoked}
 *   GET    /me/security/identities                                  → {items: UserIdentity[]}
 *   DELETE /me/security/identities/{id}                             → 204
 *   POST   /me/security/identities/link/{slug} {next}               → {url}
 */
import { api, type ApiClient, type Schemas } from "@/api/client";

export type Session = Schemas["Session"];
export type LinkedIdentity = Schemas["UserIdentity"];
export type TotpEnrollment = Schemas["TotpEnrollment"];
export type RecoveryCodes = Schemas["RecoveryCodes"];
export type ProfileUpdate = Schemas["ProfileUpdate"];
export type PasswordChange = Schemas["ChangePasswordRequest"];
export type ReauthRequest = Schemas["ReauthRequest"];

const S = "/me/security";

export function securityApi(client: ApiClient = api) {
  return {
    overview: () => client.request<Schemas["SecurityOverview"]>(S),
    updateProfile: (body: ProfileUpdate) => client.request<Schemas["User"]>("/me", { method: "PATCH", body }),
    changePassword: (body: PasswordChange) => client.request<void>(`${S}/password`, { method: "POST", body }),
    enrollTotp: () => client.request<TotpEnrollment>(`${S}/totp/enroll`, { method: "POST" }),
    confirmTotp: (code: string) => client.request<RecoveryCodes>(`${S}/totp/confirm`, { method: "POST", body: { code } }),
    disableTotp: (body: ReauthRequest) => client.request<void>(`${S}/totp/disable`, { method: "POST", body }),
    regenerateRecoveryCodes: (body: ReauthRequest) => client.request<RecoveryCodes>(`${S}/recovery-codes`, { method: "POST", body }),
    sessions: () => client.request<{ items: Session[] }>(`${S}/sessions`),
    revokeSession: (id: number) => client.request<void>(`${S}/sessions/${id}`, { method: "DELETE" }),
    revokeOtherSessions: () => client.request<Schemas["RevokeResult"]>(`${S}/sessions/revoke-others`, { method: "POST" }),
    identities: () => client.request<{ items: LinkedIdentity[] }>(`${S}/identities`),
    unlinkIdentity: (id: number) => client.request<void>(`${S}/identities/${id}`, { method: "DELETE" }),
    /** Starts linking an identity; resolves to the IdP URL to navigate to. */
    linkIdentity: async (slug: string, next?: string): Promise<string> =>
      (
        await client.request<Schemas["UrlResponse"]>(`${S}/identities/link/${encodeURIComponent(slug)}`, {
          method: "POST",
          body: next ? { next } : {},
        })
      ).url,
  };
}

export const security = securityApi();
