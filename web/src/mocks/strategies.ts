import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import { iso } from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const err = (status: number, code: string, message: string, fields?: Record<string, string>) =>
  HttpResponse.json({ error: { code, message, ...(fields ? { fields } : {}) } }, { status });
const noContent = () => new HttpResponse(null, { status: 204 });

let nextStratId = 4;
export const mockStrategies: S["ClientStrategy"][] = [
  {
    id: 1,
    name: "Standard Office",
    note: "Balanced policy for typical workstations: password or click confirmation, clipboard and files enabled, local config changes blocked.",
    config_options: {
      "approve-mode": "both",
      "verification-method": "both",
      "enable-keyboard": "Y",
      "enable-clipboard": "Y",
      "enable-file-transfer": "Y",
      "enable-audio": "Y",
      "enable-tunnel": "Y",
      "enable-remote-restart": "Y",
      "enable-record-session": "N",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "N",
      "direct-server": "N",
    },
    is_default: true,
    device_count: 42,
    created_at: iso(86_400_000 * 30),
    updated_at: iso(86_400_000 * 5),
  },
  {
    id: 2,
    name: "Strict Security",
    note: "Strict policy for sensitive hosts: temporary passwords only, clipboard and file transfers disabled, automatic screen lock on disconnect.",
    config_options: {
      "approve-mode": "password",
      "verification-method": "use-temporary-password",
      "enable-keyboard": "Y",
      "enable-clipboard": "N",
      "enable-file-transfer": "N",
      "enable-audio": "N",
      "enable-tunnel": "N",
      "enable-remote-restart": "N",
      "enable-record-session": "Y",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "Y",
      "direct-server": "N",
    },
    is_default: false,
    device_count: 5,
    created_at: iso(86_400_000 * 20),
    updated_at: iso(86_400_000 * 2),
  },
  {
    id: 3,
    name: "Unattended Server",
    note: "Policy for unattended servers: permanent password only, audio disabled, screen locked on disconnect.",
    config_options: {
      "approve-mode": "password",
      "verification-method": "use-permanent-password",
      "enable-keyboard": "Y",
      "enable-clipboard": "Y",
      "enable-file-transfer": "Y",
      "enable-audio": "N",
      "enable-tunnel": "Y",
      "enable-remote-restart": "Y",
      "enable-record-session": "N",
      "allow-remote-config-modification": "N",
      "lock-after-session-end": "Y",
      "direct-server": "N",
    },
    is_default: false,
    device_count: 8,
    created_at: iso(86_400_000 * 15),
    updated_at: iso(86_400_000 * 1),
  },
];

export const strategyHandlers = [
  http.get(`${B}/strategies`, () => {
    return ok({ items: mockStrategies });
  }),

  http.post(`${B}/strategies`, async ({ request }) => {
    const body = (await request.json()) as S["ClientStrategyInput"];
    if (!body.name?.trim()) return err(400, "validation_failed", "name required", { name: "required" });
    if (mockStrategies.some((s) => s.name.toLowerCase() === body.name.trim().toLowerCase())) {
      return err(409, "conflict", "a strategy with this name already exists");
    }
    if (body.is_default) {
      for (const s of mockStrategies) s.is_default = false;
    }
    const created: S["ClientStrategy"] = {
      id: nextStratId++,
      name: body.name.trim(),
      note: body.note ?? "",
      config_options: body.config_options ?? {},
      is_default: Boolean(body.is_default),
      device_count: 0,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockStrategies.unshift(created);
    return ok(created, 201);
  }),

  http.get(`${B}/strategies/:id`, ({ params }) => {
    const id = Number(params.id);
    const s = mockStrategies.find((x) => x.id === id);
    if (!s) return err(404, "not_found", "strategy not found");
    return ok(s);
  }),

  http.patch(`${B}/strategies/:id`, async ({ params, request }) => {
    const id = Number(params.id);
    const s = mockStrategies.find((x) => x.id === id);
    if (!s) return err(404, "not_found", "strategy not found");
    const body = (await request.json()) as S["ClientStrategyPatch"];
    if (body.name !== undefined) {
      const name = body.name.trim();
      if (!name) return err(400, "validation_failed", "name required", { name: "required" });
      if (mockStrategies.some((x) => x.id !== id && x.name.toLowerCase() === name.toLowerCase())) {
        return err(409, "conflict", "a strategy with this name already exists");
      }
      s.name = name;
    }
    if (body.note !== undefined) s.note = body.note;
    if (body.config_options !== undefined) s.config_options = body.config_options;
    if (body.is_default !== undefined) {
      if (body.is_default) {
        for (const x of mockStrategies) x.is_default = false;
      }
      s.is_default = body.is_default;
    }
    s.updated_at = new Date().toISOString();
    return ok(s);
  }),

  http.delete(`${B}/strategies/:id`, ({ params }) => {
    const id = Number(params.id);
    const idx = mockStrategies.findIndex((x) => x.id === id);
    if (idx === -1) return err(404, "not_found", "strategy not found");
    if (mockStrategies[idx]!.is_default) {
      return err(400, "validation_failed", "cannot delete default strategy", { strategy: "cannot delete default strategy" });
    }
    mockStrategies.splice(idx, 1);
    return noContent();
  }),

  http.post(`${B}/strategies/:id/default`, ({ params }) => {
    const id = Number(params.id);
    const s = mockStrategies.find((x) => x.id === id);
    if (!s) return err(404, "not_found", "strategy not found");
    for (const x of mockStrategies) x.is_default = false;
    s.is_default = true;
    s.updated_at = new Date().toISOString();
    return ok(s);
  }),
];
