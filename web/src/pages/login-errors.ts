import { ApiError } from "@/api/client";

/** Maps API error codes to user-facing i18n keys. */
export function loginErrorKey(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "rate_limited") return "login.rateLimited";
    if (err.code === "totp_invalid") return "login.codeWrong";
    if (err.code === "password_login_disabled") return "login.passwordDisabled";
    if (err.code === "oidc_not_configured") return "login.oidcNotConfigured";
    if (err.code === "provider_unavailable") return "login.providerUnavailable";
    if (err.code === "passkey_failed") return "login.passkeyFailed";
    if (err.code === "passkeys_unavailable") return "login.passkeysUnavailable";
    if (err.code === "no_passkeys") return "login.noPasskeys";
    if (err.status === 401) return "login.invalidCredentials";
  }
  return "login.failed";
}

/** OIDC failure codes the server appends as `/login?error=oidc_<code>` or `?link_error=<code>`. */
export const OIDC_ERROR_CODES = [
  "expired",
  "denied",
  "failed",
  "no_account",
  "disabled",
  "ambiguous",
  "already_linked",
  "session_mismatch",
] as const;

/** i18n key for an OIDC redirect error (`oidc_no_account` or `no_account`). */
export function oidcErrorKey(code: string | undefined): string {
  const c = (code ?? "").replace(/^oidc_/, "");
  return (OIDC_ERROR_CODES as readonly string[]).includes(c) ? `login.oidcErrors.${c}` : "login.oidcFailed";
}
