import { api, type QueryOf, type Schemas } from "@/api/client";

/* ── Dashboard / branding ─────────────────────────────────────────────── */

export type Dashboard = Schemas["Dashboard"];
export type Branding = Schemas["Branding"];

export const dashboardApi = {
  get: () => api.request<Dashboard>("/dashboard"),
  branding: () => api.request<Branding>("/branding"),
};

/* ── Audit ────────────────────────────────────────────────────────────── */

export const auditApi = {
  connections: (q: QueryOf<"listConnectionAudits">) =>
    api.request<Schemas["ConnectionAuditPage"]>("/audit/connections", { query: q }),
  files: (q: QueryOf<"listFileTransferAudits">) =>
    api.request<Schemas["FileTransferAuditPage"]>("/audit/files", { query: q }),
  alarms: (q: QueryOf<"listAlarmAudits">) => api.request<Schemas["AlarmAuditPage"]>("/audit/alarms", { query: q }),
  logins: (q: QueryOf<"listLoginEvents">) => api.request<Schemas["LoginEventPage"]>("/audit/logins", { query: q }),
  admin: (q: QueryOf<"listAdminAudit">) => api.request<Schemas["AdminAuditPage"]>("/audit/admin", { query: q }),
};

/* ── Server (hbbs/hbbr) ───────────────────────────────────────────────── */

export type ServerHealth = Schemas["ServerHealth"];
export type ServerSettingsState = Schemas["ServerSettingsState"];
export type ServerSettingsPatch = Schemas["ServerSettingsPatch"];

export const serverApi = {
  health: () => api.request<ServerHealth>("/server/health"),
  settings: () => api.request<ServerSettingsState>("/server/settings"),
  updateSettings: (body: ServerSettingsPatch) =>
    api.request<ServerSettingsState>("/server/settings", { method: "PATCH", body }),
  reapply: () => api.request<ServerSettingsState>("/server/settings/reapply", { method: "POST" }),
  ipBlocker: (q: QueryOf<"listIpBlocker">) => api.request<Schemas["IpBlockerPage"]>("/server/ip-blocker", { query: q }),
  unblockIp: (ip: string) => api.request<void>(`/server/ip-blocker/${encodeURIComponent(ip)}`, { method: "DELETE" }),
  ipChanges: (q: QueryOf<"listIpChanges">) => api.request<Schemas["IpChangesPage"]>("/server/ip-changes", { query: q }),
  clearIpChanges: (id: string) =>
    api.request<void>(`/server/ip-changes/${encodeURIComponent(id)}`, { method: "DELETE" }),
  punchRequests: (q: QueryOf<"listPunchRequests">) =>
    api.request<Schemas["PunchRequestPage"]>("/server/punch-requests", { query: q }),
  relayUsage: () =>
    api.request<{ checked_at: string; items: Schemas["RelayUsage"][] }>("/server/relay-usage"),
  consoleCommands: () => api.request<{ items: Schemas["ConsoleCommand"][] }>("/server/console/commands"),
  runConsole: (body: Schemas["ConsoleRequest"]) =>
    api.request<Schemas["ConsoleResult"]>("/server/console", { method: "POST", body }),
  consoleHistory: (q: QueryOf<"listConsoleHistory">) =>
    api.request<Schemas["ConsoleHistoryPage"]>("/server/console/history", { query: q }),
};

/* ── Panel settings ───────────────────────────────────────────────────── */

export type PanelSettings = Schemas["PanelSettings"];
export type PanelSettingsPatch = Schemas["PanelSettingsPatch"];

export const settingsApi = {
  get: () => api.request<PanelSettings>("/settings"),
  update: (body: PanelSettingsPatch) => api.request<PanelSettings>("/settings", { method: "PATCH", body }),
  uploadLogo: (file: File) => api.request<Branding>("/settings/logo", { method: "PUT", rawBody: file }),
  deleteLogo: () => api.request<void>("/settings/logo", { method: "DELETE" }),
};
