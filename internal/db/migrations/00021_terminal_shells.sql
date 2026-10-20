-- +goose Up

-- Web client terminal sessions kept running on a device (RustDesk terminal
-- service), remembered per panel user so they can be reattached from any
-- browser. The service id is a capability: sealed with the AEAD key; its
-- sha256 is the lookup key.
CREATE TABLE terminal_shells (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    rustdesk_id  TEXT    NOT NULL CHECK (length(rustdesk_id) BETWEEN 1 AND 100),
    service_hash BLOB    NOT NULL,
    service_enc  BLOB    NOT NULL,
    hostname     TEXT    NOT NULL DEFAULT '',
    tabs         INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX ux_terminal_shells ON terminal_shells (user_id, rustdesk_id, service_hash);

-- +goose Down
DROP TABLE terminal_shells;
