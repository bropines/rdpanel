import { api, type QueryOf, type Schemas } from "@/api/client";

export type UserListItem = Schemas["UserListItem"];
export type UserDetail = Schemas["UserDetail"];
export type UserListQuery = QueryOf<"listUsers">;
export type UserGroup = Schemas["UserGroup"];
export type DeviceGroup = Schemas["DeviceGroup"];
export type UserRef = Schemas["UserRef"];
export type GroupRef = Schemas["GroupRef"];

export const usersApi = {
  list: (query: UserListQuery) => api.request<Schemas["UserPage"]>("/users", { query }),
  get: (id: number) => api.request<UserDetail>(`/users/${id}`),
  create: (body: Schemas["UserCreate"]) => api.request<UserListItem>("/users", { method: "POST", body }),
  update: (id: number, body: Schemas["UserUpdate"]) =>
    api.request<UserListItem>(`/users/${id}`, { method: "PATCH", body }),
  remove: (id: number) => api.request<void>(`/users/${id}`, { method: "DELETE" }),
  resetPassword: (id: number, body: Schemas["PasswordReset"]) =>
    api.request<void>(`/users/${id}/password`, { method: "POST", body }),
  resetTotp: (id: number) => api.request<void>(`/users/${id}/totp/reset`, { method: "POST" }),
  sessions: (id: number) => api.request<{ items: Schemas["Session"][] }>(`/users/${id}/sessions`),
  revokeSession: (id: number, sessionId: number) =>
    api.request<void>(`/users/${id}/sessions/${sessionId}`, { method: "DELETE" }),
  revokeAll: (id: number, kind: "all" | "web" | "client" = "all") =>
    api.request<Schemas["RevokeResult"]>(`/users/${id}/sessions/revoke`, { method: "POST", body: { kind } }),
  unlinkIdentity: (id: number, identityId: number) =>
    api.request<void>(`/users/${id}/identities/${identityId}`, { method: "DELETE" }),
  passkeys: (id: number) => api.request<{ items: Schemas["Passkey"][] }>(`/users/${id}/passkeys`),
  revokePasskey: (id: number, passkeyId: number) =>
    api.request<void>(`/users/${id}/passkeys/${passkeyId}`, { method: "DELETE" }),
  apiTokens: (id: number) => api.request<{ items: Schemas["ApiToken"][] }>(`/users/${id}/api-tokens`),
  revokeApiToken: (id: number, tokenId: number) =>
    api.request<void>(`/users/${id}/api-tokens/${tokenId}`, { method: "DELETE" }),
};

export const groupsApi = {
  listUserGroups: (query: QueryOf<"listUserGroups">) =>
    api.request<Schemas["UserGroupPage"]>("/user-groups", { query }),
  createUserGroup: (body: Schemas["UserGroupInput"]) =>
    api.request<UserGroup>("/user-groups", { method: "POST", body }),
  updateUserGroup: (id: number, body: Schemas["UserGroupPatch"]) =>
    api.request<UserGroup>(`/user-groups/${id}`, { method: "PATCH", body }),
  deleteUserGroup: (id: number) => api.request<void>(`/user-groups/${id}`, { method: "DELETE" }),
  listDeviceGroups: (query: QueryOf<"listDeviceGroups">) =>
    api.request<Schemas["DeviceGroupPage"]>("/device-groups", { query }),
  createDeviceGroup: (body: Schemas["DeviceGroupInput"]) =>
    api.request<DeviceGroup>("/device-groups", { method: "POST", body }),
  updateDeviceGroup: (id: number, body: Schemas["DeviceGroupPatch"]) =>
    api.request<DeviceGroup>(`/device-groups/${id}`, { method: "PATCH", body }),
  deleteDeviceGroup: (id: number) => api.request<void>(`/device-groups/${id}`, { method: "DELETE" }),
};

export const lookupApi = {
  users: (q?: string) => api.request<{ items: UserRef[] }>("/lookup/users", { query: { q } }),
  userGroups: (q?: string) => api.request<{ items: GroupRef[] }>("/lookup/user-groups", { query: { q } }),
};

export const userKeys = {
  all: ["users"] as const,
  list: (q: UserListQuery) => ["users", "list", q] as const,
  detail: (id: number) => ["users", "detail", id] as const,
  sessions: (id: number) => ["users", "sessions", id] as const,
  passkeys: (id: number) => ["users", "passkeys", id] as const,
  apiTokens: (id: number) => ["users", "api-tokens", id] as const,
};

export const groupKeys = {
  user: ["user-groups"] as const,
  device: ["device-groups"] as const,
};
