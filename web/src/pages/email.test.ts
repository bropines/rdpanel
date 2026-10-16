import { describe, expect, it } from "vitest";

import { loginResult } from "@/api/auth";
import { ApiError } from "@/api/client";
import type { EmailSettings } from "@/api/system";
import { emailDraft, emailInput } from "@/pages/EmailSettingsCard";
import { loginErrorKey, oidcErrorKey } from "@/pages/login-errors";
import { EMAIL_CODE_RE } from "@/pages/my/EmailSection";
import { resetSchema } from "@/pages/ResetPasswordPage";

const settings: EmailSettings = {
  source: "settings",
  enabled: true,
  host: "smtp.example.com",
  port: 587,
  username: "u",
  password_set: true,
  from: "Panel <p@example.com>",
  encryption: "starttls",
  insecure_skip_verify: false,
  default_locale: "en",
  password_reset_available: true,
};

describe("e-mail settings form", () => {
  it("keeps the stored password unless it was typed", () => {
    const d = emailDraft(settings);
    expect(emailInput(d, false)).not.toHaveProperty("password");
    expect(emailInput({ ...d, password: "", passwordTouched: true }, false).password).toBe("");
    expect(emailInput({ ...d, port: "" }, false).port).toBe(0);
  });

  it("sends only the language when SMTP comes from the configuration", () => {
    expect(emailInput({ ...emailDraft(settings), default_locale: "ru" }, true)).toEqual({ default_locale: "ru" });
  });
});

describe("e-mail login and reset", () => {
  it("maps the e-mail MFA step", () => {
    expect(loginResult({ mfa_required: true, mfa_methods: ["email"], email_hint: "a***e@x.io" })).toEqual({
      kind: "mfa",
      methods: ["email"],
      emailHint: "a***e@x.io",
    });
  });

  it("maps e-mail error codes", () => {
    expect(loginErrorKey(new ApiError(401, "code_invalid", "x"))).toBe("login.codeWrong");
    expect(loginErrorKey(new ApiError(502, "email_send_failed", "x"))).toBe("login.emailSendFailed");
    expect(loginErrorKey(new ApiError(400, "invalid_token", "x"))).toBe("login.resetTokenInvalid");
    expect(oidcErrorKey("email_failed")).toBe("login.emailSendFailed");
  });

  it("validates the new password and e-mail codes", () => {
    expect(resetSchema.safeParse({ password: "long-enough", confirm: "long-enough" }).success).toBe(true);
    expect(resetSchema.safeParse({ password: "short", confirm: "short" }).success).toBe(false);
    expect(resetSchema.safeParse({ password: "long-enough", confirm: "different!" }).success).toBe(false);
    expect(EMAIL_CODE_RE.test("123456")).toBe(true);
    expect(EMAIL_CODE_RE.test("12345")).toBe(false);
  });
});
