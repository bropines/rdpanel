-- Hourly traffic samples per relay (bytes, total sessions, and max active concurrent).
-- Mirrors presence_samples; retained for 30 days.

-- +goose Up
CREATE TABLE relay_traffic_samples (
    relay_id   BIGINT  NOT NULL REFERENCES relays (id) ON DELETE CASCADE,
    hour_start BIGINT  NOT NULL,
    bytes      BIGINT  NOT NULL DEFAULT 0 CHECK (bytes >= 0),
    sessions   BIGINT  NOT NULL DEFAULT 0 CHECK (sessions >= 0),
    max_active BIGINT  NOT NULL DEFAULT 0 CHECK (max_active >= 0),
    PRIMARY KEY (relay_id, hour_start)
);

-- +goose Down
DROP TABLE relay_traffic_samples;
