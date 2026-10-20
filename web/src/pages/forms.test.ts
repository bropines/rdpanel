import { describe, expect, it } from "vitest";

import { entrySchema } from "@/pages/address-books/EntryDialog";
import { idpSchema, toInput, type IdpValues } from "@/pages/IdentityProvidersPage";
import { mfaSchema, passwordSchema } from "@/pages/LoginPage";
import { passwordChangeSchema } from "@/pages/my/ProfilePage";
import { formatSecret, TOTP_CODE_RE } from "@/pages/my/TotpSection";
import { cliCommand, windowsExeName } from "@/pages/server/ClientConfigCard";
import { iceEntries, toInput as clientOptionsInput, validateIceServers } from "@/pages/server/ClientOptionsCard";
import { parseConsoleArgs } from "@/pages/server/Console";
import { diffDraft, validateDraft } from "@/pages/server/ServerSettingsForm";
import { settingsSchema, toPatch, type SettingsValues } from "@/pages/SettingsPage";
import { userCreateSchema } from "@/pages/users/UserForm";

const issues = (r: { success: boolean; error?: { issues: { path: PropertyKey[]; message: string }[] } }) =>
  Object.fromEntries((r.error?.issues ?? []).map((i) => [i.path.join("."), i.message]));

describe("login forms", () => {
  it("requires username and password", () => {
    expect(issues(passwordSchema.safeParse({ username: " ", password: "" }))).toEqual({
      username: "login.usernameRequired",
      password: "login.passwordRequired",
    });
  });

  it("accepts a TOTP or a recovery code", () => {
    expect(mfaSchema.safeParse({ code: "123 456" }).success).toBe(true);
    expect(mfaSchema.safeParse({ code: "AAAA-BBBB" }).success).toBe(true);
    expect(issues(mfaSchema.safeParse({ code: "12" }))).toEqual({ code: "login.codeInvalid" });
    expect(TOTP_CODE_RE.test("123456")).toBe(true);
    expect(formatSecret("JBSWY3DPEHPK3PXP")).toBe("JBSW Y3DP EHPK 3PXP");
  });
});

describe("user and password forms", () => {
  const base = {
    username: "new.user",
    display_name: "",
    email: "",
    role: "user" as const,
    status: "active" as const,
    user_group_id: null,
    note: "",
    totp_required: false,
    email_verified: false,
    password: "",
  };

  it("allows an OIDC-only user without password, rejects short passwords", () => {
    expect(userCreateSchema.safeParse(base).success).toBe(true);
    expect(issues(userCreateSchema.safeParse({ ...base, password: "short" }))).toEqual({ password: "validation.passwordMin" });
  });

  it("validates username characters and email", () => {
    expect(issues(userCreateSchema.safeParse({ ...base, username: "bad name", email: "nope" }))).toEqual({
      username: "users.usernameInvalid",
      email: "validation.email",
    });
  });

  it("checks password confirmation and change", () => {
    expect(issues(passwordChangeSchema.safeParse({ current_password: "old-pass", new_password: "new-pass-1", confirm: "x" }))).toEqual({
      confirm: "security.passwordMismatch",
    });
    expect(issues(passwordChangeSchema.safeParse({ current_password: "same-pass", new_password: "same-pass", confirm: "same-pass" }))).toEqual({
      new_password: "security.passwordSame",
    });
  });
});

describe("address book entry form", () => {
  const entry = {
    rustdesk_id: "123456789",
    alias: "",
    hostname: "",
    os_username: "",
    platform: "",
    note: "",
    tags: [],
    password: "",
    clear_password: false,
    login_name: "",
    rdp_port: "",
    rdp_username: "",
    force_always_relay: false,
    same_server: false,
  };
  it("validates the RustDesk id and RDP port", () => {
    expect(entrySchema.safeParse(entry).success).toBe(true);
    expect(issues(entrySchema.safeParse({ ...entry, rustdesk_id: "12 34", rdp_port: "33a" }))).toEqual({
      rustdesk_id: "books.idInvalid",
      rdp_port: "books.portInvalid",
    });
  });
});

describe("identity provider form", () => {
  const v: IdpValues = {
    kind: "oidc",
    slug: "sso",
    display_name: "SSO",
    client_id: "panel",
    client_secret: "",
    issuer: "https://sso.example.com/realms/main",
    scopes: "openid, email  profile",
    pkce_enabled: true,
    pkce_method: "S256",
    auto_register: false,
    link_by_verified_email: true,
    enabled: true,
    sort_order: "2",
    has_secret: true,
  };

  it("requires https issuer for OIDC and a secret for new providers", () => {
    expect(idpSchema.safeParse(v).success).toBe(true);
    expect(issues(idpSchema.safeParse({ ...v, issuer: "http://sso", has_secret: false }))).toEqual({
      issuer: "idp.issuerInvalid",
      client_secret: "validation.required",
    });
    expect(idpSchema.safeParse({ ...v, kind: "github", issuer: "" }).success).toBe(true);
  });

  it("never sends an empty secret (write-only: omitted = keep)", () => {
    const body = toInput(v, true);
    expect(body).not.toHaveProperty("client_secret");
    expect(body).not.toHaveProperty("slug");
    expect(body.scopes).toEqual(["openid", "email", "profile"]);
    expect(toInput({ ...v, client_secret: "s3cret" }, false)).toMatchObject({ client_secret: "s3cret", slug: "sso", kind: "oidc" });
    expect(toInput({ ...v, kind: "github" }, true).issuer).toBeNull();
  });
});

describe("server settings form", () => {
  const desired = {
    relay_servers: ["relay.example.com:21117"],
    always_use_relay: false,
    must_login: true,
    tcp_keepalive: null,
    webrtc: null,
    blacklist: [],
    blocklist: ["198.51.100.0/24"],
    limit_speed_mbps: 32,
    total_bandwidth_mbps: 1024,
    single_bandwidth_mbps: 128,
    downgrade_threshold: 0.66,
    downgrade_start_check_s: 1800,
  };
  const draft = {
    relay_servers: "relay.example.com:21117",
    always_use_relay: false,
    must_login: true,
    tcp_keepalive: false,
    webrtc: false,
    blacklist: "",
    blocklist: "198.51.100.0/24",
    limit_speed_mbps: "32",
    total_bandwidth_mbps: "1024",
    single_bandwidth_mbps: "128",
    downgrade_threshold: "0.66",
    downgrade_start_check_s: "1800",
  };

  it("sends only changed keys", () => {
    expect(diffDraft(desired, draft)).toEqual({});
    expect(diffDraft(desired, { ...draft, always_use_relay: true, total_bandwidth_mbps: "512", blacklist: "1.2.3.4\n1.2.3.4" })).toEqual({
      always_use_relay: true,
      total_bandwidth_mbps: 512,
      blacklist: ["1.2.3.4"],
    });
  });

  it("maps the TCP keepalive toggle (unmanaged = off)", () => {
    expect(diffDraft(desired, { ...draft, tcp_keepalive: true })).toEqual({ tcp_keepalive: true });
    expect(diffDraft({ ...desired, tcp_keepalive: true }, { ...draft, tcp_keepalive: true })).toEqual({});
    expect(diffDraft({ ...desired, tcp_keepalive: true }, draft)).toEqual({ tcp_keepalive: false });
  });

  it("maps the WebRTC toggle (unmanaged = off)", () => {
    expect(diffDraft(desired, { ...draft, webrtc: true })).toEqual({ webrtc: true });
    expect(diffDraft({ ...desired, webrtc: true }, { ...draft, webrtc: true })).toEqual({});
    expect(diffDraft({ ...desired, webrtc: true }, draft)).toEqual({ webrtc: false });
  });

  it("validates hosts, IPs and positive numbers", () => {
    expect(validateDraft(draft)).toEqual({});
    expect(validateDraft({ ...draft, relay_servers: "bad host", blocklist: "999.1.1.1", single_bandwidth_mbps: "-1", downgrade_start_check_s: "1.5" })).toEqual({
      relay_servers: "server.errors.hostPort",
      blocklist: "server.errors.ip",
      single_bandwidth_mbps: "server.errors.positive",
      downgrade_start_check_s: "server.errors.positive",
    });
  });

  it("checks console args against the allow-list rules", () => {
    const cmd = { max_args: 1, arg_pattern: "[YyNn]" };
    expect(parseConsoleArgs("Y", cmd)).toEqual({ args: ["Y"] });
    expect(parseConsoleArgs("Y N", cmd).error).toBe("tooMany");
    expect(parseConsoleArgs("yes", cmd).error).toBe("pattern");
    expect(parseConsoleArgs("", cmd)).toEqual({ args: [] });
  });
});

describe("panel settings form", () => {
  const values: SettingsValues = {
    web_client_url: "",
    title: " Panel ",
    welcome_markdown: "hi",
    password_login_enabled: true,
    require_admin_2fa: true,
    require_email_2fa: false,
    session_idle_hours: "12",
    session_absolute_days: "7",
    client_token_lifetime_hours: "4380",
    login_events_days: "180",
    connection_audits_days: "365",
    file_transfer_audits_days: "365",
    alarm_audits_days: "365",
    admin_audit_days: "0",
    punch_requests_days: "30",
  };

  it("validates URLs, ranges and proxies", () => {
    expect(settingsSchema.safeParse(values).success).toBe(true);
    expect(
      issues(
        settingsSchema.safeParse({ ...values, web_client_url: "ftp://x", session_idle_hours: "0", admin_audit_days: "-1" }),
      ),
    ).toEqual({
      web_client_url: "settings.urlInvalid",
      session_idle_hours: "validation.range",
      admin_audit_days: "validation.integer",
    });
  });

  it("builds a typed PATCH body", () => {
    const p = toPatch(values);
    expect(p).not.toHaveProperty("public_url");
    expect(p).not.toHaveProperty("trusted_proxies");
    expect(p).not.toHaveProperty("rate_limits");
    expect(p.branding).toEqual({ title: "Panel", welcome_markdown: "hi", web_client_url: null });
    expect(p.auth?.client_token_lifetime_hours).toBe(4380);
    expect(p.retention?.admin_audit_days).toBe(0);
  });
});

describe("client options (WebRTC)", () => {
  it("splits and validates ICE servers like the server", () => {
    expect(iceEntries(" stun:a.example.com:3478 ,\n\nturn://u:p@b.example.com:3478\r\n")).toEqual([
      "stun:a.example.com:3478",
      "turn://u:p@b.example.com:3478",
    ]);
    expect(validateIceServers("")).toBeNull();
    expect(validateIceServers("stun:a.example.com:3478\nstuns://b.example.com\nturns://u:p%40ss@c.example.com:5349")).toBeNull();
    expect(validateIceServers("turn:no-creds.example.com:3478")).toBe("server.clientOptions.errors.turn");
    expect(validateIceServers("turn://user@host.example.com")).toBe("server.clientOptions.errors.turn");
    expect(validateIceServers("http://example.com")).toBe("server.clientOptions.errors.url");
    expect(validateIceServers(Array.from({ length: 17 }, (_, i) => `stun:s${i}.example.com`).join("\n"))).toBe(
      "server.clientOptions.errors.tooMany",
    );
  });

  it("maps the draft to the PUT body", () => {
    expect(clientOptionsInput({ ice_servers: " stun:a.example.com \n", congestion: "default" })).toEqual({
      ice_servers: "stun:a.example.com",
      webrtc_congestion_control: null,
    });
    expect(clientOptionsInput({ ice_servers: "", congestion: "on" }).webrtc_congestion_control).toBe(true);
    expect(clientOptionsInput({ ice_servers: "", congestion: "off" }).webrtc_congestion_control).toBe(false);
  });
});

describe("client config helpers", () => {
  it("formats CLI command with quotes", () => {
    expect(cliCommand("abc123xyz")).toBe('rustdesk --config "abc123xyz"');
  });

  it("builds Windows executable name with host and key", () => {
    expect(windowsExeName("=0nI9Q2bu5yZ")).toBe("rustdesk-licensed-=0nI9Q2bu5yZ.exe");
    expect(windowsExeName("")).toBe("rustdesk.exe");
  });
});

