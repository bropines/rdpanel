import { api } from "./client";
import type { components } from "./schema";

export type LDAPSettings = components["schemas"]["LDAPSettings"];
export type LDAPSettingsInput = components["schemas"]["LDAPSettingsInput"];
export type LDAPTestRequest = components["schemas"]["LDAPTestRequest"];
export type LDAPTestResult = components["schemas"]["LDAPTestResult"];
export type LDAPSyncResult = components["schemas"]["LDAPSyncResult"];

export const ldapApi = {
  get: () => api.request<LDAPSettings>("/settings/ldap"),
  update: (body: LDAPSettingsInput) =>
    api.request<LDAPSettings>("/settings/ldap", { method: "PUT", body }),
  test: (body: LDAPTestRequest) =>
    api.request<LDAPTestResult>("/settings/ldap/test", { method: "POST", body }),
  sync: () =>
    api.request<LDAPSyncResult>("/settings/ldap/sync", { method: "POST" }),
};

export const ldapKeys = {
  all: ["settings", "ldap"] as const,
  settings: () => [...ldapKeys.all, "detail"] as const,
};
