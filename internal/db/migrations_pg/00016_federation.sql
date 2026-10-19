-- +goose Up
CREATE TABLE federation_peers (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
    api_url       TEXT    NOT NULL CHECK (length(api_url) BETWEEN 1 AND 255),
    token_hash    TEXT    NOT NULL,
    sync_relays   INTEGER NOT NULL DEFAULT 1 CHECK (sync_relays IN (0, 1)),
    status        TEXT    NOT NULL DEFAULT 'unknown' CHECK (status IN ('unknown', 'active', 'unreachable')),
    last_sync_at BIGINT NULL,
    last_error    TEXT    NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE TABLE federated_relays (
    id            BIGSERIAL PRIMARY KEY,
    peer_id BIGINT NOT NULL REFERENCES federation_peers (id) ON DELETE CASCADE,
    remote_name   TEXT    NOT NULL,
    public_host   TEXT    NOT NULL,
    region        TEXT    NULL,
    health        TEXT    NOT NULL DEFAULT 'active',
    last_sync_at BIGINT NOT NULL,
    UNIQUE (peer_id, remote_name)
);

CREATE INDEX ix_federated_relays_peer ON federated_relays (peer_id);

-- +goose Down
DROP TABLE IF EXISTS federated_relays;
DROP TABLE IF EXISTS federation_peers;
