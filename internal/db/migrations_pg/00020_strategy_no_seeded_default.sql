-- +goose Up

-- 00017 seeded "Standard Office" as the default strategy, so every device
-- without a strategy of its own got its options pushed on upgrade. A seeded
-- strategy the admin never touched (created_at = updated_at = the seed's
-- timestamp; editing or setting the default bumps updated_at) stops being
-- the default: devices without a strategy get only the admin-saved client
-- options again. A default chosen by an admin stays.
UPDATE client_strategies SET is_default = 0
WHERE is_default = 1 AND created_at = 1729000000000 AND updated_at = 1729000000000;

-- +goose Down
UPDATE client_strategies SET is_default = 1
WHERE name = 'Standard Office' AND created_at = 1729000000000 AND updated_at = 1729000000000
  AND NOT EXISTS (SELECT 1 FROM client_strategies d WHERE d.is_default = 1);
