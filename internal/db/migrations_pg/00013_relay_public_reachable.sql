-- Public host reachability for relays with separate mgmt_host:
-- stores whether the lightweight TCP connect to public_host succeeded during the last probe.
-- NULL when mgmt_host is not configured or relay has never been probed.
-- See docs/design/relay-agents.md §17.

-- +goose Up
ALTER TABLE relays ADD COLUMN public_reachable BIGINT NULL CHECK (
    public_reachable IS NULL OR public_reachable IN (0, 1)
);

-- +goose Down
ALTER TABLE relays DROP COLUMN public_reachable;
