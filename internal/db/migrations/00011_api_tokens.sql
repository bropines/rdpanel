-- Personal API tokens for the admin API (/api/v1/admin/*, /api/v1/events):
-- "Authorization: Bearer rdp_..." for scripts and automation. A token acts as
-- its owner (role taken from users at request time); scope 'read' only allows
-- GET/HEAD. Only sha256 of the token is stored; prefix is its first
-- characters, kept for display. See docs/api.md.

-- +goose Up
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    prefix       TEXT    NOT NULL CHECK (length(prefix) BETWEEN 1 AND 16),
    token_sha256 BLOB    NOT NULL UNIQUE CHECK (length(token_sha256) = 32),
    scope        TEXT    NOT NULL CHECK (scope IN ('read', 'write')),
    expires_at   INTEGER NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NULL,
    last_used_ip TEXT    NULL,
    revoked_at   INTEGER NULL
);
CREATE INDEX ix_api_tokens_user ON api_tokens (user_id, created_at);

-- +goose Down
DROP TABLE api_tokens;
