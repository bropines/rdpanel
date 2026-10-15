/**
 * "My security" endpoints. Owned by stream B1 in api/openapi.yaml — shapes below are
 * the UI's assumptions; reconcile by editing only this file.
 *
 *   PATCH  /me {display_name?, email?}                         → User
 *   POST   /me/password {current_password, new_password}       → 204 (other sessions revoked)
 *   POST   /me/totp/enroll                                      → {secret, otpauth_uri}
 *   POST   /me/totp/confirm {code}                              → {recovery_codes: string[]}
 *   POST   /me/totp/disable {password, code}                    → 204
 *   POST   /me/recovery-codes {code}                            → {recovery_codes: string[]}
 *   GET    /me/sessions                                         → {items: Session[]}
 *   DELETE /me/sessions/{id}                                    → 204
 *   POST   /me/sessions/revoke-others                           → {revoked}
 *   GET    /me/identities                                       → {items: UserIdentity[]}
 *   DELETE /me/identities/{id}                                  → 204
 */
import { api, type ApiClient, type Schemas } from "@/api/client";

export type Session = Schemas["Session"];
export type LinkedIdentity = Schemas["UserIdentity"];

export interface TotpEnrollment {
  secret: string;
  otpauth_uri: string;
}

export interface RecoveryCodes {
  recovery_codes: string[];
}

export interface ProfileUpdate {
  display_name?: string;
  email?: string | null;
}

export interface PasswordChange {
  current_password: string;
  new_password: string;
}

export function securityApi(client: ApiClient = api) {
  return {
    updateProfile: (body: ProfileUpdate) => client.request<Schemas["User"]>("/me", { method: "PATCH", body }),
    changePassword: (body: PasswordChange) => client.request<void>("/me/password", { method: "POST", body }),
    enrollTotp: () => client.request<TotpEnrollment>("/me/totp/enroll", { method: "POST" }),
    confirmTotp: (code: string) =>
      client.request<RecoveryCodes>("/me/totp/confirm", { method: "POST", body: { code } }),
    disableTotp: (body: { password: string; code: string }) =>
      client.request<void>("/me/totp/disable", { method: "POST", body }),
    regenerateRecoveryCodes: (code: string) =>
      client.request<RecoveryCodes>("/me/recovery-codes", { method: "POST", body: { code } }),
    sessions: () => client.request<{ items: Session[] }>("/me/sessions"),
    revokeSession: (id: number) => client.request<void>(`/me/sessions/${id}`, { method: "DELETE" }),
    revokeOtherSessions: () =>
      client.request<Schemas["RevokeResult"]>("/me/sessions/revoke-others", { method: "POST" }),
    identities: () => client.request<{ items: LinkedIdentity[] }>("/me/identities"),
    unlinkIdentity: (id: number) => client.request<void>(`/me/identities/${id}`, { method: "DELETE" }),
  };
}

export const security = securityApi();
