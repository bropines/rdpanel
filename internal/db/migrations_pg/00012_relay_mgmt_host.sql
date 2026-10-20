-- Separate public and management addresses for relays:
-- public_host is handed to clients via hbbs rs, while mgmt_host (optional)
-- is probed by RD Panel for end-to-end reachability checks.
-- See docs/design/relay-agents.md §17.

-- +goose Up
ALTER TABLE relays ADD COLUMN mgmt_host TEXT NULL CHECK (
    mgmt_host IS NULL OR (
        length(mgmt_host) BETWEEN 1 AND 255
        AND strpos(mgmt_host, ',') = 0
        AND strpos(mgmt_host, ' ') = 0
    )
);

-- +goose Down
ALTER TABLE relays DROP COLUMN mgmt_host;
