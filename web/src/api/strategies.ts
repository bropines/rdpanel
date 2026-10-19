import { api } from "./client";
import type { components } from "./schema";

export type ClientStrategy = components["schemas"]["ClientStrategy"];
export type ClientStrategyInput = components["schemas"]["ClientStrategyInput"];
export type ClientStrategyPatch = components["schemas"]["ClientStrategyPatch"];

export const strategiesApi = {
  list: () => api.request<{ items: ClientStrategy[] }>("/strategies"),
  get: (id: number) => api.request<ClientStrategy>(`/strategies/${id}`),
  create: (body: ClientStrategyInput) => api.request<ClientStrategy>("/strategies", { method: "POST", body }),
  update: (id: number, body: ClientStrategyPatch) => api.request<ClientStrategy>(`/strategies/${id}`, { method: "PATCH", body }),
  delete: (id: number) => api.request<void>(`/strategies/${id}`, { method: "DELETE" }),
  setDefault: (id: number) => api.request<ClientStrategy>(`/strategies/${id}/default`, { method: "POST" }),
};

export const strategyKeys = {
  all: ["strategies"] as const,
  list: () => [...strategyKeys.all, "list"] as const,
  detail: (id: number) => [...strategyKeys.all, "detail", id] as const,
};
