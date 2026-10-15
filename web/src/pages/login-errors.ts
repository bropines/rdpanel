import { ApiError } from "@/api/client";

/** Maps API error codes to user-facing i18n keys. */
export function loginErrorKey(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "rate_limited") return "login.rateLimited";
    if (err.code === "totp_invalid" || err.code === "mfa_invalid") return "login.codeWrong";
    if (err.code === "account_disabled") return "login.disabled";
    if (err.status === 401) return "login.invalidCredentials";
  }
  return "login.failed";
}
