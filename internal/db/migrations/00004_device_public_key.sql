-- Device Ed25519 public key (32 raw bytes), recorded by POST /api/devices/deploy
-- (or synced from hbbs) and used to verify POST /api/switch-grant signatures.

-- +goose Up
ALTER TABLE devices ADD COLUMN pk BLOB NULL CHECK (pk IS NULL OR length(pk) = 32);

-- +goose Down
ALTER TABLE devices DROP COLUMN pk;
