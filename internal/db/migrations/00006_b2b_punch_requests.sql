-- hbbs punch requests ("pr"), collected periodically by the panel (hbbs keeps
-- them in an unbounded in-memory list until "pr -").

-- +goose Up
CREATE TABLE punch_requests (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    -- Time hbbs recorded the request.
    at             INTEGER NOT NULL,
    from_ip        TEXT    NOT NULL,
    to_rustdesk_id TEXT    NOT NULL,
    to_ip          TEXT    NULL,
    created_at     INTEGER NOT NULL
);
CREATE INDEX ix_punch_requests_at ON punch_requests (at);
CREATE INDEX ix_punch_requests_to ON punch_requests (to_rustdesk_id);

-- +goose Down
DROP TABLE punch_requests;
