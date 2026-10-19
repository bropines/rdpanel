-- E-mail verification, e-mail second factor and password reset by e-mail.
--
--   users.email_verified_at   set when the user confirmed a code sent to the
--                             current address; cleared whenever the address
--                             changes (store.Users.Update).
--   users.email_2fa_enabled   per-user opt-in: a code sent to the verified
--                             address is the second factor (panel and client
--                             /api/login) when the user has no TOTP.
--   users.locale              language of e-mails (NULL = panel default).
--   email_codes               short-lived one-time codes / reset tokens; only
--                             sha256 digests are stored.

-- +goose Up
ALTER TABLE users ADD COLUMN email_verified_at BIGINT NULL;
ALTER TABLE users ADD COLUMN email_2fa_enabled INTEGER NOT NULL DEFAULT 0 CHECK (email_2fa_enabled IN (0, 1));
ALTER TABLE users ADD COLUMN locale TEXT NULL CHECK (locale IS NULL OR locale IN ('en', 'ru'));

CREATE TABLE email_codes (
    id          BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose     TEXT    NOT NULL CHECK (purpose IN ('verify_email', 'login', 'password_reset')),
    -- Address the code was sent to: verification applies only while the
    -- user's address is unchanged.
    email       CITEXT NOT NULL,
    -- sha256 of the code (6 digits, bound to user and purpose) or of the
    -- reset token (256 bits).
    code_sha256 BYTEA    NOT NULL CHECK (length(code_sha256) = 32),
    attempts    INTEGER NOT NULL DEFAULT 0,
    ip          TEXT    NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    consumed_at BIGINT NULL
);
CREATE INDEX ix_email_codes_user_purpose ON email_codes (user_id, purpose, created_at);
CREATE INDEX ix_email_codes_code_sha256 ON email_codes (code_sha256);
CREATE INDEX ix_email_codes_expires_at ON email_codes (expires_at);

-- +goose Down
DROP TABLE email_codes;
ALTER TABLE users DROP COLUMN locale;
ALTER TABLE users DROP COLUMN email_2fa_enabled;
ALTER TABLE users DROP COLUMN email_verified_at;
