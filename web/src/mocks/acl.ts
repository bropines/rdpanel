import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import { iso } from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });
const err = (status: number, code: string, message: string, fields?: Record<string, string>) =>
  HttpResponse.json({ error: { code, message, ...(fields ? { fields } : {}) } }, { status });
const noContent = () => new HttpResponse(null, { status: 204 });

let nextRoleId = 4;
export const mockControlRoles: S["ControlRole"][] = [
  {
    id: 1,
    name: "Full Control",
    note: "All capabilities granted (keyboard, clipboard, file transfer, audio, tunnels, terminal, restart).",
    allow_keyboard_mouse: true,
    allow_clipboard: true,
    allow_file_transfer: true,
    allow_audio: true,
    allow_tcp_tunneling: true,
    allow_terminal: true,
    allow_restart: true,
    is_default: true,
    rule_count: 2,
    created_at: iso(86_400_000 * 30),
    updated_at: iso(86_400_000 * 30),
  },
  {
    id: 2,
    name: "View Only",
    note: "Observer mode with no remote input, no file transfer, and no remote terminal.",
    allow_keyboard_mouse: false,
    allow_clipboard: false,
    allow_file_transfer: false,
    allow_audio: true,
    allow_tcp_tunneling: false,
    allow_terminal: false,
    allow_restart: false,
    is_default: false,
    rule_count: 1,
    created_at: iso(86_400_000 * 25),
    updated_at: iso(86_400_000 * 25),
  },
  {
    id: 3,
    name: "Support (No Files)",
    note: "Interactive control for troubleshooting without data exfiltration capabilities.",
    allow_keyboard_mouse: true,
    allow_clipboard: true,
    allow_file_transfer: false,
    allow_audio: true,
    allow_tcp_tunneling: false,
    allow_terminal: true,
    allow_restart: true,
    is_default: false,
    rule_count: 0,
    created_at: iso(86_400_000 * 20),
    updated_at: iso(86_400_000 * 20),
  },
];

let nextAclId = 3;
export const mockConnectionACLs: S["ConnectionACL"][] = [
  {
    id: 1,
    name: "IT Support to Workstations",
    note: "Allows members of IT Support full remote control over all office workstations",
    user_id: null,
    user_name: "",
    user_group_id: 1,
    user_group_name: "IT Support",
    device_id: null,
    device_name: "",
    device_group_id: 1,
    device_group_name: "Workstations",
    control_role_id: 1,
    control_role_name: "Full Control",
    action: "allow",
    priority: 10,
    enabled: true,
    created_at: iso(86_400_000 * 15),
    updated_at: iso(86_400_000 * 15),
  },
  {
    id: 2,
    name: "Block External Contractors from Finance",
    note: "Prevents any unauthorized remote sessions into finance servers",
    user_id: null,
    user_name: "",
    user_group_id: 2,
    user_group_name: "Contractors",
    device_id: null,
    device_name: "",
    device_group_id: 2,
    device_group_name: "Finance",
    control_role_id: null,
    control_role_name: "",
    action: "deny",
    priority: 5,
    enabled: true,
    created_at: iso(86_400_000 * 10),
    updated_at: iso(86_400_000 * 10),
  },
];

export const aclHandlers = [
  // --- Control Roles ---
  http.get(`${B}/control-roles`, () => {
    return ok({ items: mockControlRoles });
  }),

  http.post(`${B}/control-roles`, async ({ request }) => {
    const body = (await request.json()) as S["ControlRoleInput"];
    if (!body?.name?.trim()) {
      return err(400, "validation_failed", "invalid control role", { name: "required" });
    }
    if (mockControlRoles.some((r) => r.name.toLowerCase() === body.name.trim().toLowerCase())) {
      return err(409, "conflict", "a control role with this name already exists");
    }

    if (body.is_default) {
      mockControlRoles.forEach((r) => (r.is_default = false));
    }

    const created: S["ControlRole"] = {
      id: nextRoleId++,
      name: body.name.trim(),
      note: body.note ?? "",
      allow_keyboard_mouse: body.allow_keyboard_mouse ?? true,
      allow_clipboard: body.allow_clipboard ?? true,
      allow_file_transfer: body.allow_file_transfer ?? true,
      allow_audio: body.allow_audio ?? true,
      allow_tcp_tunneling: body.allow_tcp_tunneling ?? true,
      allow_terminal: body.allow_terminal ?? true,
      allow_restart: body.allow_restart ?? true,
      is_default: body.is_default ?? false,
      rule_count: 0,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockControlRoles.push(created);
    return ok(created, 201);
  }),

  http.get(`${B}/control-roles/:id`, ({ params }) => {
    const id = Number(params.id);
    const role = mockControlRoles.find((r) => r.id === id);
    if (!role) return err(404, "not_found", "control role not found");
    return ok(role);
  }),

  http.patch(`${B}/control-roles/:id`, async ({ params, request }) => {
    const id = Number(params.id);
    const role = mockControlRoles.find((r) => r.id === id);
    if (!role) return err(404, "not_found", "control role not found");

    const body = (await request.json()) as S["ControlRolePatch"];
    if (body.name !== undefined) {
      const trimmed = body.name.trim();
      if (!trimmed) {
        return err(400, "validation_failed", "invalid control role", { name: "must be 1-64 characters" });
      }
      if (mockControlRoles.some((r) => r.id !== id && r.name.toLowerCase() === trimmed.toLowerCase())) {
        return err(409, "conflict", "a control role with this name already exists");
      }
      role.name = trimmed;
    }
    if (body.note !== undefined) role.note = body.note;
    if (body.allow_keyboard_mouse !== undefined) role.allow_keyboard_mouse = body.allow_keyboard_mouse;
    if (body.allow_clipboard !== undefined) role.allow_clipboard = body.allow_clipboard;
    if (body.allow_file_transfer !== undefined) role.allow_file_transfer = body.allow_file_transfer;
    if (body.allow_audio !== undefined) role.allow_audio = body.allow_audio;
    if (body.allow_tcp_tunneling !== undefined) role.allow_tcp_tunneling = body.allow_tcp_tunneling;
    if (body.allow_terminal !== undefined) role.allow_terminal = body.allow_terminal;
    if (body.allow_restart !== undefined) role.allow_restart = body.allow_restart;
    if (body.is_default !== undefined) {
      if (body.is_default) {
        mockControlRoles.forEach((r) => (r.is_default = false));
      }
      role.is_default = body.is_default;
    }
    role.updated_at = new Date().toISOString();
    return ok(role);
  }),

  http.delete(`${B}/control-roles/:id`, ({ params }) => {
    const id = Number(params.id);
    const role = mockControlRoles.find((r) => r.id === id);
    if (!role) return err(404, "not_found", "control role not found");
    if (role.is_default) {
      return err(400, "validation_failed", "cannot delete default control role");
    }
    const idx = mockControlRoles.findIndex((r) => r.id === id);
    mockControlRoles.splice(idx, 1);
    return noContent();
  }),

  http.post(`${B}/control-roles/:id/default`, ({ params }) => {
    const id = Number(params.id);
    const role = mockControlRoles.find((r) => r.id === id);
    if (!role) return err(404, "not_found", "control role not found");
    mockControlRoles.forEach((r) => (r.is_default = false));
    role.is_default = true;
    role.updated_at = new Date().toISOString();
    return ok(role);
  }),

  // --- Connection ACL Rules ---
  http.get(`${B}/acls`, () => {
    return ok({ items: mockConnectionACLs });
  }),

  http.post(`${B}/acls`, async ({ request }) => {
    const body = (await request.json()) as S["ConnectionACLInput"];
    if (!body?.name?.trim()) {
      return err(400, "validation_failed", "invalid acl rule", { name: "required" });
    }
    if (!body?.action || (body.action !== "allow" && body.action !== "deny")) {
      return err(400, "validation_failed", "invalid acl rule", { action: "must be allow or deny" });
    }

    const created: S["ConnectionACL"] = {
      id: nextAclId++,
      name: body.name.trim(),
      note: body.note ?? "",
      user_id: body.user_id ?? null,
      user_name: body.user_id ? "User " + body.user_id : "",
      user_group_id: body.user_group_id ?? null,
      user_group_name: body.user_group_id ? "Group " + body.user_group_id : "",
      device_id: body.device_id ?? null,
      device_name: body.device_id ? "Device " + body.device_id : "",
      device_group_id: body.device_group_id ?? null,
      device_group_name: body.device_group_id ? "Group " + body.device_group_id : "",
      control_role_id: body.control_role_id ?? null,
      control_role_name: body.control_role_id ? mockControlRoles.find((r) => r.id === body.control_role_id)?.name ?? "" : "",
      action: body.action,
      priority: body.priority ?? 100,
      enabled: body.enabled ?? true,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    mockConnectionACLs.push(created);
    return ok(created, 201);
  }),

  http.get(`${B}/acls/:id`, ({ params }) => {
    const id = Number(params.id);
    const rule = mockConnectionACLs.find((r) => r.id === id);
    if (!rule) return err(404, "not_found", "acl rule not found");
    return ok(rule);
  }),

  http.patch(`${B}/acls/:id`, async ({ params, request }) => {
    const id = Number(params.id);
    const rule = mockConnectionACLs.find((r) => r.id === id);
    if (!rule) return err(404, "not_found", "acl rule not found");

    const body = (await request.json()) as S["ConnectionACLPatch"];
    if (body.name !== undefined) rule.name = body.name.trim();
    if (body.note !== undefined) rule.note = body.note;
    if (body.user_id !== undefined) {
      rule.user_id = body.user_id;
      rule.user_name = body.user_id ? "User " + body.user_id : "";
      if (body.user_id) {
        rule.user_group_id = null;
        rule.user_group_name = "";
      }
    }
    if (body.user_group_id !== undefined) {
      rule.user_group_id = body.user_group_id;
      rule.user_group_name = body.user_group_id ? "Group " + body.user_group_id : "";
      if (body.user_group_id) {
        rule.user_id = null;
        rule.user_name = "";
      }
    }
    if (body.device_id !== undefined) {
      rule.device_id = body.device_id;
      rule.device_name = body.device_id ? "Device " + body.device_id : "";
      if (body.device_id) {
        rule.device_group_id = null;
        rule.device_group_name = "";
      }
    }
    if (body.device_group_id !== undefined) {
      rule.device_group_id = body.device_group_id;
      rule.device_group_name = body.device_group_id ? "Group " + body.device_group_id : "";
      if (body.device_group_id) {
        rule.device_id = null;
        rule.device_name = "";
      }
    }
    if (body.control_role_id !== undefined) {
      rule.control_role_id = body.control_role_id;
      rule.control_role_name = body.control_role_id ? mockControlRoles.find((r) => r.id === body.control_role_id)?.name ?? "" : "";
    }
    if (body.action !== undefined) rule.action = body.action;
    if (body.priority !== undefined) rule.priority = body.priority;
    if (body.enabled !== undefined) rule.enabled = body.enabled;
    rule.updated_at = new Date().toISOString();
    return ok(rule);
  }),

  http.delete(`${B}/acls/:id`, ({ params }) => {
    const id = Number(params.id);
    const idx = mockConnectionACLs.findIndex((r) => r.id === id);
    if (idx === -1) return err(404, "not_found", "acl rule not found");
    mockConnectionACLs.splice(idx, 1);
    return noContent();
  }),

  // --- ACL Test Access Simulation ---
  http.post(`${B}/acls/test`, async ({ request }) => {
    const body = (await request.json()) as S["ACLTestRequest"];
    if (!body?.user_id || !body?.device_id) {
      return err(400, "validation_failed", "user_id and device_id are required");
    }

    // Find first enabled rule matching user or device
    const matched = mockConnectionACLs
      .filter((r) => r.enabled)
      .sort((a, b) => a.priority - b.priority)[0] ?? null;

    const defaultRole = mockControlRoles.find((r) => r.is_default) ?? mockControlRoles[0];
    const effectiveRole = matched?.control_role_id
      ? mockControlRoles.find((r) => r.id === matched.control_role_id) ?? defaultRole
      : defaultRole;

    const res: S["ACLTestResult"] = {
      decision: matched ? (matched.action === "allow" ? "allow" : "deny") : "no_rules",
      matched_rule: matched,
      effective_role: matched?.action === "allow" || !matched ? effectiveRole : null,
    };
    return ok(res);
  }),
];
