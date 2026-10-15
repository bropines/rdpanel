import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import { loginErrorKey, oidcErrorKey } from "@/pages/login-errors";

describe("loginErrorKey", () => {
  it("maps API errors to messages", () => {
    expect(loginErrorKey(new ApiError(401, "invalid_credentials", "x"))).toBe("login.invalidCredentials");
    expect(loginErrorKey(new ApiError(401, "totp_invalid", "x"))).toBe("login.codeWrong");
    expect(loginErrorKey(new ApiError(403, "password_login_disabled", "x"))).toBe("login.passwordDisabled");
    expect(loginErrorKey(new ApiError(429, "rate_limited", "x"))).toBe("login.rateLimited");
    expect(loginErrorKey(new ApiError(500, "internal", "x"))).toBe("login.failed");
    expect(loginErrorKey(new Error("network"))).toBe("login.failed");
  });
});

describe("oidcErrorKey", () => {
  it("maps redirect codes", () => {
    expect(oidcErrorKey("oidc_no_account")).toBe("login.oidcErrors.no_account");
    expect(oidcErrorKey("already_linked")).toBe("login.oidcErrors.already_linked");
    expect(oidcErrorKey("oidc_weird")).toBe("login.oidcFailed");
    expect(oidcErrorKey(undefined)).toBe("login.oidcFailed");
  });
});
