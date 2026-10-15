import { api, type QueryOf, type Schemas } from "@/api/client";

export type Device = Schemas["Device"];
export type DeviceDetail = Schemas["DeviceDetail"];
export type DevicePage = Schemas["DevicePage"];
export type DeviceListQuery = QueryOf<"listDevices">;
export type DeviceExportQuery = QueryOf<"exportDevicesCsv">;
export type DeviceBulkRequest = Schemas["DeviceBulkRequest"];

export const devicesApi = {
  list: (query: DeviceListQuery, signal?: AbortSignal) =>
    api.request<DevicePage>("/devices", { query, signal }),
  facets: () => api.request<Schemas["DeviceFacets"]>("/devices/facets"),
  get: (id: number) => api.request<DeviceDetail>(`/devices/${id}`),
  update: (id: number, body: Schemas["DeviceUpdate"]) =>
    api.request<Device>(`/devices/${id}`, { method: "PATCH", body }),
  remove: (id: number) => api.request<void>(`/devices/${id}`, { method: "DELETE" }),
  kick: (id: number) => api.request<void>(`/devices/${id}/kick`, { method: "POST" }),
  bulk: (body: DeviceBulkRequest) =>
    api.request<Schemas["BulkResult"]>("/devices/bulk", { method: "POST", body }),
  exportCsv: (query: DeviceExportQuery) => api.download("/devices/export.csv", query),
};

export const deviceKeys = {
  all: ["devices"] as const,
  list: (q: DeviceListQuery) => ["devices", "list", q] as const,
  detail: (id: number) => ["devices", "detail", id] as const,
  facets: ["devices", "facets"] as const,
};
