import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import { loginErrorKey } from "@/pages/login-errors";

describe("loginErrorKey", () => {
  it("maps API errors to messages", () => {
    expect(loginErrorKey(new ApiError(401, "unauthorized", "x"))).toBe("login.invalidCredentials");
    expect(loginErrorKey(new ApiError(429, "rate_limited", "x"))).toBe("login.rateLimited");
    expect(loginErrorKey(new ApiError(500, "internal", "x"))).toBe("login.failed");
    expect(loginErrorKey(new Error("network"))).toBe("login.failed");
  });
});
