-- Panel assets (branding logo) and hourly online-device samples for the
-- dashboard "online" series (stream B2a).

-- +goose Up
CREATE TABLE panel_assets (
    name         TEXT    PRIMARY KEY,
    content_type TEXT    NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp')),
    data         BLOB    NOT NULL CHECK (length(data) <= 262144),
    sha256       BLOB    NOT NULL CHECK (length(sha256) = 32),
    updated_by   INTEGER NULL REFERENCES users (id) ON DELETE SET NULL,
    updated_at   INTEGER NOT NULL
);

-- One row per hour (bucket start, unix ms): the maximum number of devices
-- observed online during that hour.
CREATE TABLE presence_samples (
    hour_start INTEGER PRIMARY KEY,
    max_online INTEGER NOT NULL CHECK (max_online >= 0),
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE presence_samples;
DROP TABLE panel_assets;
