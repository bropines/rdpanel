import { API_BASE, api, buildQuery, type QueryOf, type Schemas } from "@/api/client";

/* ── Dashboard / branding ─────────────────────────────────────────────── */

export type Dashboard = Schemas["Dashboard"];
export type Branding = Schemas["Branding"];

export const dashboardApi = {
  get: () => api.request<Dashboard>("/dashboard"),
  branding: () => api.request<Branding>("/branding"),
};

/* ── Web client ───────────────────────────────────────────────────────── */

export type WebClientConfig = Schemas["WebClientConfig"];

export type WebClientCredentials = Schemas["WebClientCredentials"];

export const webClientApi = {
  config: () => api.request<WebClientConfig>("/webclient/config"),
  credentials: (peer: string) => api.request<WebClientCredentials>(`/webclient/credentials/${encodeURIComponent(peer)}`),
  remember: (peer: string, hash: string) =>
    api.request<void>(`/webclient/credentials/${encodeURIComponent(peer)}`, { method: "PUT", body: { hash } }),
  forget: (peer: string) => api.request<void>(`/webclient/credentials/${encodeURIComponent(peer)}`, { method: "DELETE" }),
  /** Terminal sessions the device keeps for the caller (reattach from any browser). */
  shells: (peer: string) => api.request<{ items: WebClientShell[] }>(`/webclient/shells/${encodeURIComponent(peer)}`),
  saveShell: (peer: string, body: Schemas["WebClientShellSave"]) =>
    api.request<void>(`/webclient/shells/${encodeURIComponent(peer)}`, { method: "PUT", body }),
  forgetShell: (peer: string, serviceId: string) =>
    api.request<void>(`/webclient/shells/${encodeURIComponent(peer)}/forget`, { method: "POST", body: { service_id: serviceId } }),
};

export type WebClientShell = Schemas["WebClientShell"];

/* ── Install client ───────────────────────────────────────────────────── */

export type ClientDownloads = Schemas["ClientDownloads"];
export type ClientAsset = Schemas["ClientAsset"];

export const clientsApi = {
  get: () => api.request<ClientDownloads>("/clients"),
  /** Plain link (cookie session): the browser downloads it under the server's file name. */
  windowsUrl: (arch: "x64" | "arm64") => `${API_BASE}/clients/windows/${arch}`,
};

/* ── About ────────────────────────────────────────────────────────────── */

export type About = Schemas["About"];

export const aboutApi = {
  get: () => api.request<About>("/about"),
};

/** Shared by the About page and the sidebar footer (the release check is cached server-side). */
export const aboutQuery = { queryKey: ["about"], queryFn: aboutApi.get, staleTime: 10 * 60_000 } as const;

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
export type ClientOptions = Schemas["ClientOptions"];
export type ClientOptionsInput = Schemas["ClientOptionsInput"];
export type IceServerProbeList = Schemas["IceServerProbeList"];
export type IceServerProbeItem = Schemas["IceServerProbeItem"];
export type PublicStunList = Schemas["PublicStunList"];
export type ClientConfig = Schemas["ClientConfig"];

export const serverApi = {
  health: () => api.request<ServerHealth>("/server/health"),
  settings: () => api.request<ServerSettingsState>("/server/settings"),
  updateSettings: (body: ServerSettingsPatch) =>
    api.request<ServerSettingsState>("/server/settings", { method: "PATCH", body }),
  reapply: () => api.request<ServerSettingsState>("/server/settings/reapply", { method: "POST" }),
  clientOptions: () => api.request<ClientOptions>("/server/client-options"),
  updateClientOptions: (body: ClientOptionsInput) =>
    api.request<ClientOptions>("/server/client-options", { method: "PUT", body }),
  probeIceServers: (body: { ice_servers: string }) =>
    api.request<IceServerProbeList>("/server/client-options/probe", { method: "POST", body }),
  fetchPublicStun: (body?: { source_url?: string; limit?: number }) =>
    api.request<PublicStunList>("/server/client-options/fetch-stun", { method: "POST", body }),
  clientConfig: () => api.request<ClientConfig>("/server/client-config"),
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
  logs: (q: QueryOf<"listServerLogs">) => api.request<Schemas["LogPage"]>("/server/logs", { query: q }),
  downloadLogs: (q: QueryOf<"downloadServerLogs">) => api.download("/server/logs/download", q),
};

/** URL of the live log stream (SSE, same session cookie). */
export function logStreamUrl(q: QueryOf<"streamServerLogs">): string {
  const qs = buildQuery(q);
  return `${API_BASE}/server/logs/stream${qs ? `?${qs}` : ""}`;
}

/* ── Panel settings ───────────────────────────────────────────────────── */

export type PanelSettings = Schemas["PanelSettings"];
export type PanelSettingsPatch = Schemas["PanelSettingsPatch"];
export type EmailSettings = Schemas["EmailSettings"];
export type EmailSettingsInput = Schemas["EmailSettingsInput"];

export const settingsApi = {
  get: () => api.request<PanelSettings>("/settings"),
  update: (body: PanelSettingsPatch) => api.request<PanelSettings>("/settings", { method: "PATCH", body }),
  uploadLogo: (file: File) => api.request<Branding>("/settings/logo", { method: "PUT", rawBody: file }),
  deleteLogo: () => api.request<void>("/settings/logo", { method: "DELETE" }),
  email: () => api.request<EmailSettings>("/settings/email"),
  updateEmail: (body: EmailSettingsInput) => api.request<EmailSettings>("/settings/email", { method: "PUT", body }),
  testEmail: (body: Schemas["TestEmailRequest"]) =>
    api.request<Schemas["TestEmailResult"]>("/settings/email/test", { method: "POST", body }),
};
