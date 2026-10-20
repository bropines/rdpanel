import { http, HttpResponse } from "msw";

import type { Schemas } from "@/api/client";
import { iso } from "@/mocks/data";

type S = Schemas;
const B = "/api/v1/admin";

const ok = (body: unknown, status = 200) => HttpResponse.json(body as never, { status });

export let mockLDAPSettings: S["LDAPSettings"] = {
  enabled: true,
  server_url: "ldaps://ad.example.corp:636",
  start_tls: false,
  insecure_skip_verify: true,
  bind_dn: "CN=svc-rustdesk,OU=ServiceAccounts,DC=example,DC=corp",
  has_bind_password: true,
  base_dn: "DC=example,DC=corp",
  user_search_base: "OU=Employees,DC=example,DC=corp",
  user_filter: "(&(objectClass=user)(sAMAccountName=%s))",
  username_attribute: "sAMAccountName",
  display_name_attribute: "displayName",
  email_attribute: "mail",
  group_attribute: "memberOf",
  group_mappings: {
    "CN=Domain Admins,CN=Users,DC=example,DC=corp": 1,
    "CN=IT Support,OU=Groups,DC=example,DC=corp": 2,
  },
  default_user_group_id: 2,
  default_user_group_name: "IT Support",
  auto_register: true,
  sync_interval_mins: 30,
  last_sync_at: iso(600_000),
  last_sync_status: "success",
  last_sync_count: 142,
  updated_at: iso(3_600_000),
};

export const ldapHandlers = [
  http.get(`${B}/settings/ldap`, () => {
    return ok(mockLDAPSettings);
  }),

  http.put(`${B}/settings/ldap`, async ({ request }) => {
    const body = (await request.json()) as S["LDAPSettingsInput"];
    if (body.user_filter !== undefined && body.user_filter.split("%s").length > 2) {
      return HttpResponse.json(
        {
          error: {
            code: "validation_failed",
            message: "invalid user_filter",
            fields: { user_filter: "must contain %s (the username) at most once" },
          },
        },
        { status: 400 },
      );
    }
    mockLDAPSettings = {
      ...mockLDAPSettings,
      ...body,
      has_bind_password:
        body.bind_password !== undefined && body.bind_password !== ""
          ? true
          : body.bind_password === ""
            ? false
            : mockLDAPSettings.has_bind_password,
      group_mappings: body.group_mappings ?? mockLDAPSettings.group_mappings,
      updated_at: new Date().toISOString(),
    };
    return ok(mockLDAPSettings);
  }),

  http.post(`${B}/settings/ldap/test`, async ({ request }) => {
    const body = (await request.json()) as S["LDAPTestRequest"];
    // Like the server: the saved bind password only goes to the saved server and bind DN.
    const s = body.settings;
    const targetChanged =
      (s?.server_url !== undefined && s.server_url.trim().toLowerCase() !== mockLDAPSettings.server_url.toLowerCase()) ||
      (s?.bind_dn !== undefined && s.bind_dn.trim() !== mockLDAPSettings.bind_dn) ||
      (s?.start_tls === false && mockLDAPSettings.start_tls) ||
      (s?.insecure_skip_verify === true && !mockLDAPSettings.insecure_skip_verify);
    if (targetChanged && body.bind_password === undefined && s?.bind_password === undefined && mockLDAPSettings.has_bind_password) {
      return HttpResponse.json(
        {
          error: {
            code: "validation_failed",
            message: "bind_password is required when server_url, bind_dn or TLS settings differ from the saved ones",
            fields: { bind_password: "required when testing another server, bind DN or weaker TLS" },
          },
        },
        { status: 400 },
      );
    }
    const username = body.test_username?.trim();
    const password = body.test_password?.trim();

    const res: S["LDAPTestResult"] = {
      ok: true,
      message: "LDAP connection and authentication succeeded",
      connected: true,
      bound: true,
      user_found: !!username,
      user_dn: username ? `CN=${username},OU=Employees,DC=example,DC=corp` : "",
      user_bound: !!password,
      attributes: username
        ? {
            sAMAccountName: username,
            displayName: username.toUpperCase(),
            mail: `${username.toLowerCase()}@example.corp`,
          }
        : {},
      groups: username ? ["CN=IT Support,OU=Groups,DC=example,DC=corp"] : [],
      mapped_group_id: 2,
    };
    return ok(res);
  }),

  http.post(`${B}/settings/ldap/sync`, () => {
    mockLDAPSettings.last_sync_at = new Date().toISOString();
    mockLDAPSettings.last_sync_status = "success";
    mockLDAPSettings.last_sync_count = 145;

    const res: S["LDAPSyncResult"] = {
      synced_count: 145,
      status: "success",
    };
    return ok(res);
  }),
];
