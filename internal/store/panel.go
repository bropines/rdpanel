package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Panel settings keys managed by PATCH /settings (values are JSON). The
// branding title/welcome and auth.require_admin_2fa keys are in settings.go.
const (
	SettingPublicURL              = "panel.public_url"
	SettingBrandingWebClientURL   = "branding.web_client_url"
	SettingPasswordLoginEnabled   = "auth.password_login_enabled"
	SettingSessionIdleHours       = "auth.session_idle_hours"
	SettingSessionAbsoluteDays    = "auth.session_absolute_days"
	SettingClientTokenLifetimeH   = "auth.client_token_lifetime_hours"
	SettingRetentionLoginEvents   = "retention.login_events_days"
	SettingRetentionConnections   = "retention.connection_audits_days"
	SettingRetentionFileTransfers = "retention.file_transfer_audits_days"
	SettingRetentionAlarms        = "retention.alarm_audits_days"
	SettingRetentionAdminAudit    = "retention.admin_audit_days"
	SettingRetentionPunchRequests = "retention.punch_requests_days"
	SettingRateLoginPerIP         = "rate_limits.login_per_ip_per_minute"
	SettingRateLoginPerUser       = "rate_limits.login_per_user_per_minute"
	SettingRateClientAPIPerIP     = "rate_limits.client_api_per_ip_per_minute"
	SettingTrustedProxies         = "http.trusted_proxies"
)

// AssetLogo is the panel_assets name of the branding logo.
const AssetLogo = "branding.logo"

// MaxAssetBytes bounds a stored asset (CHECK in the schema too).
const MaxAssetBytes = 256 << 10

// Asset is a row of panel_assets.
type Asset struct {
	Name        string
	ContentType string
	Data        []byte
	SHA256      []byte
	UpdatedAt   time.Time
}

// Assets is the panel_assets repository.
type Assets struct {
	q   db.Querier
	now func() time.Time
}

// Assets returns the panel_assets repository bound to s.
func (s *Store) Assets() *Assets { return &Assets{q: s.q, now: s.opts.now} }

// Get returns the asset name.
func (r *Assets) Get(ctx context.Context, name string) (*Asset, error) {
	var (
		a  Asset
		at int64
	)
	err := r.q.QueryRowContext(ctx, `SELECT name, content_type, data, sha256, updated_at FROM panel_assets WHERE name = ?`,
		name).Scan(&a.Name, &a.ContentType, &a.Data, &a.SHA256, &at)
	if err != nil {
		return nil, mapErr(err)
	}
	a.UpdatedAt = fromMs(at)
	return &a, nil
}

// Exists reports whether asset name is stored, with its digest.
func (r *Assets) Exists(ctx context.Context, name string) (bool, []byte, error) {
	var sum []byte
	err := r.q.QueryRowContext(ctx, `SELECT sha256 FROM panel_assets WHERE name = ?`, name).Scan(&sum)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, mapErr(err)
	}
	return true, sum, nil
}

// Put stores (replaces) asset a; SHA256 and UpdatedAt are set.
func (r *Assets) Put(ctx context.Context, a *Asset, updatedBy *int64) error {
	sum := sha256.Sum256(a.Data)
	a.SHA256, a.UpdatedAt = sum[:], r.now()
	_, err := r.q.ExecContext(ctx, `INSERT INTO panel_assets (name, content_type, data, sha256, updated_by, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (name) DO UPDATE SET content_type = excluded.content_type,
		data = excluded.data, sha256 = excluded.sha256, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		a.Name, a.ContentType, a.Data, a.SHA256, nullID(updatedBy), toMs(a.UpdatedAt))
	return mapErr(err)
}

// Delete removes asset name; it reports whether it existed.
func (r *Assets) Delete(ctx context.Context, name string) (bool, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM panel_assets WHERE name = ?`, name)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PresenceSamples records the hourly maximum of online devices.
type PresenceSamples struct {
	q   db.Querier
	now func() time.Time
}

// PresenceSamples returns the presence_samples repository bound to s.
func (s *Store) PresenceSamples() *PresenceSamples { return &PresenceSamples{q: s.q, now: s.opts.now} }

// Record raises the sample of the hour containing at to online if higher.
func (r *PresenceSamples) Record(ctx context.Context, at time.Time, online int) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO presence_samples (hour_start, max_online, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (hour_start) DO UPDATE SET max_online = max(max_online, excluded.max_online),
		updated_at = excluded.updated_at`, toMs(HourStart(at)), max(online, 0), toMs(r.now()))
	return mapErr(err)
}

// Since returns the samples of hours starting at or after since (keyed by hour start, unix ms).
func (r *PresenceSamples) Since(ctx context.Context, since time.Time) (map[int64]int, error) {
	return hourlyCounts(ctx, r.q, `SELECT hour_start, max_online FROM presence_samples WHERE hour_start >= ?`,
		[]any{toMs(since)})
}

// PurgeBefore deletes samples of hours before cutoff.
func (r *PresenceSamples) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM presence_samples WHERE hour_start < ?`, toMs(cutoff))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
