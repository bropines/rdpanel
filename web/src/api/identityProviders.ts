/**
 * Identity providers admin (api/openapi.yaml, tag `identity-providers`).
 *
 *   GET    /identity-providers            → {items: IdentityProvider[]}
 *   POST   /identity-providers            → IdentityProvider   (IdentityProviderInput)
 *   PATCH  /identity-providers/{id}       → IdentityProvider   (omitted fields / client_secret kept)
 *   DELETE /identity-providers/{id}       → 204
 *   POST   /identity-providers/{id}/test  → {ok, error?, issuer?, authorization_endpoint?, …, callback_url}
 */
import { api, type ApiClient, type Schemas } from "@/api/client";

export type IdentityProvider = Schemas["IdentityProvider"];
export type IdentityProviderInput = Schemas["IdentityProviderInput"];
export type IdentityProviderPatch = Schemas["IdentityProviderPatch"];
export type IdentityProviderTestResult = Schemas["IdentityProviderTestResult"];
export type IdpKind = Schemas["ProviderKind"];

export function identityProvidersApi(client: ApiClient = api) {
  return {
    list: () => client.request<{ items: IdentityProvider[] }>("/identity-providers"),
    create: (body: IdentityProviderInput) => client.request<IdentityProvider>("/identity-providers", { method: "POST", body }),
    update: (id: number, body: IdentityProviderPatch) =>
      client.request<IdentityProvider>(`/identity-providers/${id}`, { method: "PATCH", body }),
    remove: (id: number) => client.request<void>(`/identity-providers/${id}`, { method: "DELETE" }),
    test: (id: number) => client.request<IdentityProviderTestResult>(`/identity-providers/${id}/test`, { method: "POST" }),
  };
}

export const identityProviders = identityProvidersApi();
