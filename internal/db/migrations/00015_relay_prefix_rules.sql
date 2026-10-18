-- Prefix routing rules (CIDR -> relay hosts) for hbbs geo/subnet selection.

-- +goose Up
CREATE TABLE relay_prefix_rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    cidr       TEXT NOT NULL,
    relay_host TEXT NOT NULL,
    comment    TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_relay_prefix_rules_cidr ON relay_prefix_rules (cidr);

-- +goose Down
DROP TABLE relay_prefix_rules;
