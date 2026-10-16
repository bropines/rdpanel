-- Passkeys (WebAuthn) for panel login: credentials, per-user WebAuthn user
-- handles, short-lived ceremony challenges, and the 'passkey' login method.

-- +goose Up

-- Random, opaque WebAuthn user handle (user.id in the WebAuthn sense) per
-- panel user; returned by authenticators in discoverable logins.
CREATE TABLE webauthn_user_handles (
    user_id    INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    handle     BLOB    NOT NULL UNIQUE CHECK (length(handle) BETWEEN 16 AND 64),
    created_at INTEGER NOT NULL
);

CREATE TABLE webauthn_credentials (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id    BLOB    NOT NULL UNIQUE CHECK (length(credential_id) BETWEEN 1 AND 1023),
    -- COSE-encoded credential public key.
    public_key       BLOB    NOT NULL,
    attestation_type TEXT    NOT NULL DEFAULT '',
    aaguid           BLOB    NULL,
    -- Last signature counter seen; clone_warning is set when an assertion
    -- did not advance it (the login is refused).
    sign_count       INTEGER NOT NULL DEFAULT 0 CHECK (sign_count >= 0),
    clone_warning    INTEGER NOT NULL DEFAULT 0 CHECK (clone_warning IN (0, 1)),
    -- Comma-separated AuthenticatorTransport values.
    transports       TEXT    NOT NULL DEFAULT '',
    attachment       TEXT    NOT NULL DEFAULT '',
    user_verified    INTEGER NOT NULL DEFAULT 0 CHECK (user_verified IN (0, 1)),
    backup_eligible  INTEGER NOT NULL DEFAULT 0 CHECK (backup_eligible IN (0, 1)),
    backup_state     INTEGER NOT NULL DEFAULT 0 CHECK (backup_state IN (0, 1)),
    name             TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    last_used_at     INTEGER NULL
);
CREATE INDEX ix_webauthn_credentials_user ON webauthn_credentials (user_id);

-- Pending registration/login ceremonies. The plain ceremony id is only held by
-- the browser; session_data is the go-webauthn SessionData (JSON).
CREATE TABLE webauthn_challenges (
    id_sha256    BLOB    PRIMARY KEY CHECK (length(id_sha256) = 32),
    kind         TEXT    NOT NULL CHECK (kind IN ('register', 'login', 'mfa')),
    user_id      INTEGER NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Panel session the ceremony is bound to (register, mfa).
    token_id     INTEGER NULL REFERENCES auth_tokens (id) ON DELETE CASCADE,
    session_data BLOB    NOT NULL,
    expires_at   INTEGER NOT NULL,
    created_at   INTEGER NOT NULL
);
CREATE INDEX ix_webauthn_challenges_expires ON webauthn_challenges (expires_at);

-- login_events.method gains 'passkey' (SQLite cannot alter a CHECK: rebuild).
CREATE TABLE login_events_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NULL REFERENCES users (id) ON DELETE SET NULL,
    username_attempted TEXT    NULL,
    token_id           INTEGER NULL REFERENCES auth_tokens (id) ON DELETE SET NULL,
    method             TEXT    NOT NULL CHECK (method IN ('password', 'oidc', 'webauth', 'passkey')),
    result             TEXT    NOT NULL DEFAULT 'success' CHECK (result IN ('success', 'failure')),
    failure_reason     TEXT    NULL,
    client_kind        TEXT    NULL,
    device_rustdesk_id TEXT    NULL,
    device_uuid        TEXT    NULL,
    platform           TEXT    NULL,
    ip                 TEXT    NULL,
    user_agent         TEXT    NULL,
    hidden_by_user     INTEGER NOT NULL DEFAULT 0 CHECK (hidden_by_user IN (0, 1)),
    created_at         INTEGER NOT NULL
);
INSERT INTO login_events_new SELECT id, user_id, username_attempted, token_id, method, result, failure_reason,
    client_kind, device_rustdesk_id, device_uuid, platform, ip, user_agent, hidden_by_user, created_at
    FROM login_events;
DROP TABLE login_events;
ALTER TABLE login_events_new RENAME TO login_events;
CREATE INDEX ix_login_events_created_at ON login_events (created_at);
CREATE INDEX ix_login_events_user_created ON login_events (user_id, created_at);

-- +goose Down
CREATE TABLE login_events_old (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id            INTEGER NULL REFERENCES users (id) ON DELETE SET NULL,
    username_attempted TEXT    NULL,
    token_id           INTEGER NULL REFERENCES auth_tokens (id) ON DELETE SET NULL,
    method             TEXT    NOT NULL CHECK (method IN ('password', 'oidc', 'webauth')),
    result             TEXT    NOT NULL DEFAULT 'success' CHECK (result IN ('success', 'failure')),
    failure_reason     TEXT    NULL,
    client_kind        TEXT    NULL,
    device_rustdesk_id TEXT    NULL,
    device_uuid        TEXT    NULL,
    platform           TEXT    NULL,
    ip                 TEXT    NULL,
    user_agent         TEXT    NULL,
    hidden_by_user     INTEGER NOT NULL DEFAULT 0 CHECK (hidden_by_user IN (0, 1)),
    created_at         INTEGER NOT NULL
);
INSERT INTO login_events_old SELECT id, user_id, username_attempted, token_id,
    CASE method WHEN 'passkey' THEN 'webauth' ELSE method END, result, failure_reason,
    client_kind, device_rustdesk_id, device_uuid, platform, ip, user_agent, hidden_by_user, created_at
    FROM login_events;
DROP TABLE login_events;
ALTER TABLE login_events_old RENAME TO login_events;
CREATE INDEX ix_login_events_created_at ON login_events (created_at);
CREATE INDEX ix_login_events_user_created ON login_events (user_id, created_at);

DROP TABLE webauthn_challenges;
DROP TABLE webauthn_credentials;
DROP TABLE webauthn_user_handles;
