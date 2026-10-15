package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Well-known settings keys. Values are JSON.
const (
	SettingBrandingTitle   = "branding.title"
	SettingBrandingWelcome = "branding.welcome_markdown"
	SettingRetentionDays   = "audit.retention_days"
	SettingRequireAdmin2FA = "auth.require_admin_2fa"
)

// Settings is the key/value settings repository (JSON values).
type Settings struct {
	q   db.Querier
	now func() time.Time
}

// Get unmarshals the value of key into dst; ErrNotFound when unset.
func (r *Settings) Get(ctx context.Context, key string, dst any) error {
	raw, err := r.GetRaw(ctx, key)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("store: setting %q: %w", key, err)
	}
	return nil
}

// GetRaw returns the raw JSON value of key; ErrNotFound when unset.
func (r *Settings) GetRaw(ctx context.Context, key string) (json.RawMessage, error) {
	var v string
	if err := r.q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return nil, mapErr(err)
	}
	return json.RawMessage(v), nil
}

// Set stores value (JSON-encoded) under key. updatedBy may be nil (CLI/system).
func (r *Settings) Set(ctx context.Context, key string, value any, updatedBy *int64) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("store: setting %q: %w", key, err)
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO settings (key, value, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by,
		updated_at = excluded.updated_at`, key, string(raw), nullID(updatedBy), toMs(r.now()))
	return mapErr(err)
}

// Delete removes key (no error when absent).
func (r *Settings) Delete(ctx context.Context, key string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return mapErr(err)
}

// All returns every setting as raw JSON.
func (r *Settings) All(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, mapErr(err)
		}
		out[k] = json.RawMessage(v)
	}
	return out, mapErr(rows.Err())
}

// GetOr is Get with a default for unset keys.
func GetOr[T any](ctx context.Context, s *Settings, key string, def T) (T, error) {
	var v T
	err := s.Get(ctx, key, &v)
	if errors.Is(err, ErrNotFound) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	return v, nil
}
