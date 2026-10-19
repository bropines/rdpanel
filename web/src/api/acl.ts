import { api } from "./client";
import type { components } from "./schema";

export type ControlRole = components["schemas"]["ControlRole"];
export type ControlRoleInput = components["schemas"]["ControlRoleInput"];
export type ControlRolePatch = components["schemas"]["ControlRolePatch"];

export type ConnectionACL = components["schemas"]["ConnectionACL"];
export type ConnectionACLInput = components["schemas"]["ConnectionACLInput"];
export type ConnectionACLPatch = components["schemas"]["ConnectionACLPatch"];

export type ACLTestRequest = components["schemas"]["ACLTestRequest"];
export type ACLTestResult = components["schemas"]["ACLTestResult"];

export const aclApi = {
  // Control Roles
  listRoles: () => api.request<{ items: ControlRole[] }>("/control-roles"),
  getRole: (id: number) => api.request<ControlRole>(`/control-roles/${id}`),
  createRole: (body: ControlRoleInput) => api.request<ControlRole>("/control-roles", { method: "POST", body }),
  updateRole: (id: number, body: ControlRolePatch) => api.request<ControlRole>(`/control-roles/${id}`, { method: "PATCH", body }),
  deleteRole: (id: number) => api.request<void>(`/control-roles/${id}`, { method: "DELETE" }),
  setDefaultRole: (id: number) => api.request<ControlRole>(`/control-roles/${id}/default`, { method: "POST" }),

  // Connection ACL Rules
  listRules: () => api.request<{ items: ConnectionACL[] }>("/acls"),
  getRule: (id: number) => api.request<ConnectionACL>(`/acls/${id}`),
  createRule: (body: ConnectionACLInput) => api.request<ConnectionACL>("/acls", { method: "POST", body }),
  updateRule: (id: number, body: ConnectionACLPatch) => api.request<ConnectionACL>(`/acls/${id}`, { method: "PATCH", body }),
  deleteRule: (id: number) => api.request<void>(`/acls/${id}`, { method: "DELETE" }),

  // Test access simulation
  testAccess: (body: ACLTestRequest) => api.request<ACLTestResult>("/acls/test", { method: "POST", body }),
};

export const aclKeys = {
  all: ["acl"] as const,
  roles: () => [...aclKeys.all, "roles"] as const,
  rules: () => [...aclKeys.all, "rules"] as const,
};
