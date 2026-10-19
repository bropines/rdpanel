-- Cross-host hand-off of panel OIDC flows: the IdP returns to the public host
-- (<public_url>/api/oidc/callback) while the browser-binding cookie lives on
-- the admin host. The callback stores the IdP result and a single-use ticket;
-- the admin host verifies the binding cookie against binding_sha256 and
-- consumes the ticket (GET /api/v1/admin/auth/oidc/complete).

-- +goose Up
ALTER TABLE oauth_states ADD COLUMN binding_sha256 BYTEA NULL CHECK (binding_sha256 IS NULL OR length(binding_sha256) = 32);
ALTER TABLE oauth_states ADD COLUMN handoff_sha256 BYTEA NULL CHECK (handoff_sha256 IS NULL OR length(handoff_sha256) = 32);
ALTER TABLE oauth_states ADD COLUMN handoff_expires_at BIGINT NULL;
CREATE UNIQUE INDEX ux_oauth_states_handoff_sha256 ON oauth_states (handoff_sha256) WHERE handoff_sha256 IS NOT NULL;

-- +goose Down
DROP INDEX ux_oauth_states_handoff_sha256;
ALTER TABLE oauth_states DROP COLUMN handoff_expires_at;
ALTER TABLE oauth_states DROP COLUMN handoff_sha256;
ALTER TABLE oauth_states DROP COLUMN binding_sha256;
