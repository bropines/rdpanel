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
 *   POST   /me/security/email/verify                                 → 204 (code e-mailed)
 *   POST   /me/security/email/confirm {code}                         → SecurityOverview
 *   PUT    /me/security/email-2fa {enabled, password?, code?}        → SecurityOverview
 *   GET    /me/security/sessions                                    → {items: Session[]}
 *   DELETE /me/security/sessions/{id}                               → 204
 *   POST   /me/security/sessions/revoke-others                      → {revoked}
 *   GET    /me/security/identities                                  → {items: UserIdentity[]}
 *   DELETE /me/security/identities/{id}                             → 204
 *   POST   /me/security/identities/link/{slug} {next}               → {url}
 *   GET    /me/security/passkeys                                    → {items: Passkey[]}
 *   POST   /me/security/passkeys/register/{begin,finish}            → Passkey (begin needs re-auth)
 *   PATCH  /me/security/passkeys/{id} {name}                        → Passkey
 *   DELETE /me/security/passkeys/{id}                               → 204
 *   GET    /me/api-tokens                                           → {items: ApiToken[]}
 *   POST   /me/api-tokens {name, scope, expires_in_days?}           → ApiTokenCreated (secret shown once)
 *   DELETE /me/api-tokens/{id}                                      → 204
 */
import { api, type ApiClient, type Schemas } from "@/api/client";
import { createPasskey, type JsonObject } from "@/lib/webauthn";

export type Session = Schemas["Session"];
export type LinkedIdentity = Schemas["UserIdentity"];
export type TotpEnrollment = Schemas["TotpEnrollment"];
export type RecoveryCodes = Schemas["RecoveryCodes"];
export type ProfileUpdate = Schemas["ProfileUpdate"];
export type PasswordChange = Schemas["ChangePasswordRequest"];
export type ReauthRequest = Schemas["ReauthRequest"];
export type SecurityOverview = Schemas["SecurityOverview"];
export type Passkey = Schemas["Passkey"];
export type ApiToken = Schemas["ApiToken"];
export type ApiTokenCreated = Schemas["ApiTokenCreated"];
export type CreateApiToken = Schemas["CreateApiTokenRequest"];

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
    /** E-mails a 6-digit code to verify my address. */
    sendEmailVerification: () => client.request<void>(`${S}/email/verify`, { method: "POST" }),
    confirmEmail: (code: string) => client.request<SecurityOverview>(`${S}/email/confirm`, { method: "POST", body: { code } }),
    setEmail2fa: (body: Schemas["Email2faRequest"]) => client.request<SecurityOverview>(`${S}/email-2fa`, { method: "PUT", body }),
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
    passkeys: () => client.request<{ items: Passkey[] }>(`${S}/passkeys`),
    /** Re-authenticates, runs navigator.credentials.create() and stores the passkey. */
    async addPasskey(reauth: ReauthRequest, name: string): Promise<Passkey> {
      const c = await client.request<Schemas["PasskeyCeremony"]>(`${S}/passkeys/register/begin`, { method: "POST", body: reauth });
      const credential = await createPasskey(c.options as JsonObject);
      return client.request<Passkey>(`${S}/passkeys/register/finish`, { method: "POST", body: { ceremony_id: c.ceremony_id, name, credential } });
    },
    renamePasskey: (id: number, name: string) => client.request<Passkey>(`${S}/passkeys/${id}`, { method: "PATCH", body: { name } }),
    deletePasskey: (id: number) => client.request<void>(`${S}/passkeys/${id}`, { method: "DELETE" }),
    apiTokens: () => client.request<{ items: ApiToken[] }>("/me/api-tokens"),
    createApiToken: (body: CreateApiToken) => client.request<ApiTokenCreated>("/me/api-tokens", { method: "POST", body }),
    revokeApiToken: (id: number) => client.request<void>(`/me/api-tokens/${id}`, { method: "DELETE" }),
  };
}

export const security = securityApi();
