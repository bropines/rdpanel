import { api, type Schemas } from "@/api/client";

export type FederationPeer = Schemas["FederationPeer"];
export type CreateFederationPeerInput = Schemas["CreateFederationPeerInput"];
export type FederatedRelay = Schemas["FederatedRelay"];

export const federationKeys = {
  all: ["federation"] as const,
  peers: ["federation", "peers"] as const,
  relays: ["federation", "relays"] as const,
};

export const federationApi = {
  listPeers: () => api.request<FederationPeer[]>("/federation/peers"),
  createPeer: (body: CreateFederationPeerInput) =>
    api.request<FederationPeer>("/federation/peers", { method: "POST", body }),
  deletePeer: (id: number) =>
    api.request<void>(`/federation/peers/${id}`, { method: "DELETE" }),
  syncPeer: (id: number) =>
    api.request<FederationPeer>(`/federation/peers/${id}/sync`, { method: "POST" }),
  listRelays: () => api.request<FederatedRelay[]>("/federation/relays"),
};
