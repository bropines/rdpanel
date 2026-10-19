import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import { iso } from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const err = (status: number, code: string, message: string) =>
  HttpResponse.json({ error: { code, message } }, { status });
const noContent = () => new HttpResponse(null, { status: 204 });

let nextPeerId = 3;
export const mockFederationPeers: S["FederationPeer"][] = [
  {
    id: 1,
    name: "ams-eu-panel",
    api_url: "https://rd-ams.example.org",
    sync_relays: true,
    status: "active",
    last_sync_at: iso(120_000),
    last_error: null,
    created_at: iso(86_400_000 * 7),
    updated_at: iso(120_000),
  },
  {
    id: 2,
    name: "sgp-asia-panel",
    api_url: "https://rd-sgp.example.org",
    sync_relays: true,
    status: "unreachable",
    last_sync_at: iso(3_600_000),
    last_error: "connection refused: 198.51.100.44:443",
    created_at: iso(86_400_000 * 3),
    updated_at: iso(3_600_000),
  },
];

export const mockFederatedRelays: S["FederatedRelay"][] = [
  {
    id: 101,
    peer_id: 1,
    peer_name: "ams-eu-panel",
    remote_name: "ams-relay-01",
    public_host: "ams-relay.example.org:21117",
    region: "nl",
    health: "active",
    last_sync_at: iso(120_000),
  },
  {
    id: 102,
    peer_id: 1,
    peer_name: "ams-eu-panel",
    remote_name: "fra-relay-01",
    public_host: "fra-relay.example.org:21117",
    region: "de",
    health: "active",
    last_sync_at: iso(120_000),
  },
];

export const federationHandlers = [
  http.get(`${B}/federation/peers`, () => {
    return ok(mockFederationPeers);
  }),

  http.post(`${B}/federation/peers`, async ({ request }) => {
    const body = (await request.json()) as S["CreateFederationPeerInput"];
    if (!body?.name || !body?.api_url || !body?.auth_token) {
      return err(400, "invalid_parameter", "name, api_url and auth_token are required");
    }
    if (mockFederationPeers.some((p) => p.name.toLowerCase() === body.name.toLowerCase())) {
      return err(409, "conflict", "a peer with this name already exists");
    }

    const newPeer: S["FederationPeer"] = {
      id: nextPeerId++,
      name: body.name.trim(),
      api_url: body.api_url.trim(),
      sync_relays: body.sync_relays ?? true,
      status: "active",
      last_sync_at: new Date().toISOString(),
      last_error: null,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockFederationPeers.push(newPeer);
    return ok(newPeer, 201);
  }),

  http.delete(`${B}/federation/peers/:id`, ({ params }) => {
    const id = Number(params.id);
    const idx = mockFederationPeers.findIndex((p) => p.id === id);
    if (idx === -1) {
      return err(404, "not_found", "peer not found");
    }
    mockFederationPeers.splice(idx, 1);
    for (let i = mockFederatedRelays.length - 1; i >= 0; i--) {
      const rel = mockFederatedRelays[i];
      if (rel && rel.peer_id === id) {
        mockFederatedRelays.splice(i, 1);
      }
    }
    return noContent();
  }),

  http.post(`${B}/federation/peers/:id/sync`, ({ params }) => {
    const id = Number(params.id);
    const peer = mockFederationPeers.find((p) => p.id === id);
    if (!peer) {
      return err(404, "not_found", "peer not found");
    }
    peer.status = "active";
    peer.last_sync_at = new Date().toISOString();
    peer.last_error = null;
    return ok(peer);
  }),

  http.get(`${B}/federation/relays`, () => {
    return ok(mockFederatedRelays);
  }),
];
