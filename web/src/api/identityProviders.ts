/**
 * Identity providers admin. Owned by stream B1 in api/openapi.yaml — the shapes below
 * are the UI's assumptions; reconcile by editing only this file.
 *
 *   GET    /identity-providers            → {items: IdentityProvider[]}
 *   POST   /identity-providers            → IdentityProvider   (body: IdentityProviderInput)
 *   PATCH  /identity-providers/{id}       → IdentityProvider   (client_secret omitted = keep)
 *   DELETE /identity-providers/{id}       → 204
 *   POST   /identity-providers/{id}/test  → {ok, message, issuer?, authorization_endpoint?}
 */
import { api, type ApiClient } from "@/api/client";

export type IdpKind = "github" | "google" | "oidc";

export interface IdentityProvider {
  id: number;
  slug: string;
  kind: IdpKind | "linuxdo";
  display_name: string;
  client_id: string;
  /** Secrets are write-only: the API only says whether one is stored. */
  has_client_secret: boolean;
  issuer: string | null;
  scopes: string[];
  pkce_enabled: boolean;
  pkce_method: "S256" | "plain";
  auto_register: boolean;
  link_by_verified_email: boolean;
  enabled: boolean;
  sort_order: number;
  /** Redirect URI to register at the IdP. */
  callback_url: string;
  created_at: string;
  updated_at: string;
}

export interface IdentityProviderInput {
  slug?: string;
  kind?: IdpKind;
  display_name?: string;
  client_id?: string;
  client_secret?: string;
  issuer?: string | null;
  scopes?: string[];
  pkce_enabled?: boolean;
  pkce_method?: "S256" | "plain";
  auto_register?: boolean;
  link_by_verified_email?: boolean;
  enabled?: boolean;
  sort_order?: number;
}

export interface IdentityProviderTestResult {
  ok: boolean;
  message: string;
  issuer?: string;
  authorization_endpoint?: string;
}

export function identityProvidersApi(client: ApiClient = api) {
  return {
    list: () => client.request<{ items: IdentityProvider[] }>("/identity-providers"),
    create: (body: IdentityProviderInput) =>
      client.request<IdentityProvider>("/identity-providers", { method: "POST", body }),
    update: (id: number, body: IdentityProviderInput) =>
      client.request<IdentityProvider>(`/identity-providers/${id}`, { method: "PATCH", body }),
    remove: (id: number) => client.request<void>(`/identity-providers/${id}`, { method: "DELETE" }),
    test: (id: number) =>
      client.request<IdentityProviderTestResult>(`/identity-providers/${id}/test`, { method: "POST" }),
  };
}

export const identityProviders = identityProvidersApi();
