// Package reconcile keeps hbbs/hbbr runtime settings in line with the desired
// state stored in the panel database (server_settings).
//
// hbbs and hbbr keep every admin-channel setting in memory only, so a restart
// reverts them to CLI/env defaults. The reconciler re-applies the desired
// state on startup, whenever an admin channel becomes reachable again (restart
// detection) and whenever a periodic read-back shows drift. It also writes
// blacklist.txt / blocklist.txt into hbbr's working directory so the lists
// survive an hbbr restart even before the panel re-applies them.
package reconcile

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/hbbs/control"
)

// Settings is the desired (or observed) hbbs/hbbr runtime state. A nil field
// means "unmanaged": the panel leaves the server's value alone. In JSON, nil
// is null; an empty list is [] (managed, empty).
type Settings struct {
	// hbbs
	RelayServers   []string `json:"relay_servers"`
	AlwaysUseRelay *bool    `json:"always_use_relay"`
	MustLogin      *bool    `json:"must_login"`
	// TCPKeepalive needs bropines/rustdesk-server >= 393f872 (tcp-keepalive).
	TCPKeepalive *bool `json:"tcp_keepalive"`
	// WebRTC needs bropines/rustdesk-server with the webrtc admin command.
	WebRTC *bool `json:"webrtc"`
	// hbbr
	Blacklist           []string `json:"blacklist"`
	Blocklist           []string `json:"blocklist"`
	LimitSpeedMbps      *float64 `json:"limit_speed_mbps"`
	TotalBandwidthMbps  *float64 `json:"total_bandwidth_mbps"`
	SingleBandwidthMbps *float64 `json:"single_bandwidth_mbps"`
	DowngradeThreshold  *float64 `json:"downgrade_threshold"`
	// DowngradeStartCheckSecs is in whole seconds.
	DowngradeStartCheckSecs *int64 `json:"downgrade_start_check_secs"`
}

// Keys in server_settings.
const (
	KeyRelayServers        = "relay_servers"
	KeyAlwaysUseRelay      = "always_use_relay"
	KeyMustLogin           = "must_login"
	KeyTCPKeepalive        = "tcp_keepalive"
	KeyWebRTC              = "webrtc"
	KeyBlacklist           = "blacklist"
	KeyBlocklist           = "blocklist"
	KeyLimitSpeed          = "limit_speed"
	KeyTotalBandwidth      = "total_bandwidth"
	KeySingleBandwidth     = "single_bandwidth"
	KeyDowngradeThreshold  = "downgrade_threshold"
	KeyDowngradeStartCheck = "downgrade_start_check"
)

// MaxListEntries bounds each hbbr IP list.
const MaxListEntries = 10_000

// ErrInvalid wraps validation failures of Settings.
var ErrInvalid = errors.New("reconcile: invalid settings")

// field binds one Settings field to its server_settings row.
type field struct {
	target control.Target
	key    string
	get    func(*Settings) any // nil (untyped) when unmanaged
	set    func(*Settings, json.RawMessage) error
}

func fields() []field {
	return []field{
		{control.HBBS, KeyRelayServers, func(s *Settings) any { return nilIfNil(s.RelayServers) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.RelayServers) }},
		{control.HBBS, KeyAlwaysUseRelay, func(s *Settings) any { return ptr(s.AlwaysUseRelay) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.AlwaysUseRelay) }},
		{control.HBBS, KeyMustLogin, func(s *Settings) any { return ptr(s.MustLogin) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.MustLogin) }},
		{control.HBBS, KeyTCPKeepalive, func(s *Settings) any { return ptr(s.TCPKeepalive) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.TCPKeepalive) }},
		{control.HBBS, KeyWebRTC, func(s *Settings) any { return ptr(s.WebRTC) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.WebRTC) }},
		{control.HBBR, KeyBlacklist, func(s *Settings) any { return nilIfNil(s.Blacklist) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.Blacklist) }},
		{control.HBBR, KeyBlocklist, func(s *Settings) any { return nilIfNil(s.Blocklist) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.Blocklist) }},
		{control.HBBR, KeyLimitSpeed, func(s *Settings) any { return ptr(s.LimitSpeedMbps) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.LimitSpeedMbps) }},
		{control.HBBR, KeyTotalBandwidth, func(s *Settings) any { return ptr(s.TotalBandwidthMbps) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.TotalBandwidthMbps) }},
		{control.HBBR, KeySingleBandwidth, func(s *Settings) any { return ptr(s.SingleBandwidthMbps) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.SingleBandwidthMbps) }},
		{control.HBBR, KeyDowngradeThreshold, func(s *Settings) any { return ptr(s.DowngradeThreshold) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.DowngradeThreshold) }},
		{control.HBBR, KeyDowngradeStartCheck, func(s *Settings) any { return ptr(s.DowngradeStartCheckSecs) },
			func(s *Settings, r json.RawMessage) error { return json.Unmarshal(r, &s.DowngradeStartCheckSecs) }},
	}
}

func nilIfNil(v []string) any {
	if v == nil {
		return nil
	}
	return v
}

func ptr[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// Normalize validates s and returns a canonical copy (IPs canonicalised and
// de-duplicated, relay entries trimmed).
func Normalize(s Settings) (Settings, error) {
	out := s
	var errs []error
	if s.RelayServers != nil {
		rs, err := control.NormalizeRelayServers(s.RelayServers)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("relay_servers: %w", err))
		case len(rs) == 0:
			errs = append(errs, errors.New("relay_servers: hbbs cannot be given an empty list (use null to leave it unmanaged)"))
		}
		out.RelayServers = rs
	}
	for _, l := range []struct {
		name string
		dst  *[]string
	}{{"blacklist", &out.Blacklist}, {"blocklist", &out.Blocklist}} {
		if *l.dst == nil {
			continue
		}
		ips, err := control.NormalizeIPs(*l.dst)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", l.name, err))
			continue
		}
		if len(ips) > MaxListEntries {
			errs = append(errs, fmt.Errorf("%s: more than %d entries", l.name, MaxListEntries))
		}
		*l.dst = ips
	}
	for _, f := range []struct {
		name string
		v    *float64
	}{
		{"limit_speed_mbps", s.LimitSpeedMbps}, {"total_bandwidth_mbps", s.TotalBandwidthMbps},
		{"single_bandwidth_mbps", s.SingleBandwidthMbps}, {"downgrade_threshold", s.DowngradeThreshold},
	} {
		if f.v != nil && (!(*f.v > 0) || math.IsInf(*f.v, 0) || *f.v > 1e9) {
			errs = append(errs, fmt.Errorf("%s: must be a number > 0", f.name))
		}
	}
	if s.DowngradeThreshold != nil && *s.DowngradeThreshold < 0.01 {
		errs = append(errs, errors.New("downgrade_threshold: hbbr keeps two decimals; must be >= 0.01"))
	}
	if s.DowngradeStartCheckSecs != nil && (*s.DowngradeStartCheckSecs <= 0 || *s.DowngradeStartCheckSecs > math.MaxUint32) {
		errs = append(errs, errors.New("downgrade_start_check_secs: must be an integer > 0"))
	}
	if len(errs) > 0 {
		return Settings{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(errs...))
	}
	return out, nil
}

// KeyStatus is the bookkeeping of one desired setting.
type KeyStatus struct {
	Target    control.Target `json:"target"`
	Key       string         `json:"key"`
	UpdatedBy *int64         `json:"updated_by,omitempty"`
	UpdatedAt time.Time      `json:"updated_at"`
	AppliedAt *time.Time     `json:"applied_at,omitempty"`
	LastError string         `json:"last_error,omitempty"`
}

// Store persists desired settings in server_settings.
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Load returns the desired settings (unmanaged keys are nil).
func (s *Store) Load(ctx context.Context) (Settings, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT target, key, value FROM server_settings`)
	if err != nil {
		return Settings{}, fmt.Errorf("reconcile: load: %w", err)
	}
	defer rows.Close()
	byKey := map[string]field{}
	for _, f := range fields() {
		byKey[string(f.target)+"/"+f.key] = f
	}
	var out Settings
	for rows.Next() {
		var target, key, value string
		if err := rows.Scan(&target, &key, &value); err != nil {
			return Settings{}, err
		}
		f, ok := byKey[target+"/"+key]
		if !ok {
			continue // unknown key (newer panel version); ignore
		}
		if err := f.set(&out, json.RawMessage(value)); err != nil {
			return Settings{}, fmt.Errorf("reconcile: setting %s/%s: %w", target, key, err)
		}
	}
	return out, rows.Err()
}

// Save stores s as the complete desired state: nil fields delete their row.
// Rows whose value changes get applied_at reset.
func (s *Store) Save(ctx context.Context, st Settings, updatedBy *int64) error {
	now := s.now().UnixMilli()
	var by any
	if updatedBy != nil {
		by = *updatedBy
	}
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		for _, f := range fields() {
			v := f.get(&st)
			if v == nil {
				if _, err := tx.ExecContext(ctx, `DELETE FROM server_settings WHERE target = ? AND key = ?`, string(f.target), f.key); err != nil {
					return err
				}
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO server_settings (target, key, value, updated_by, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (target, key) DO UPDATE SET
					applied_at = CASE WHEN server_settings.value = excluded.value THEN server_settings.applied_at ELSE NULL END,
					last_error = CASE WHEN server_settings.value = excluded.value THEN server_settings.last_error ELSE NULL END,
					value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
				string(f.target), f.key, string(raw), by, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkApplied records the outcome of applying one key.
func (s *Store) MarkApplied(ctx context.Context, target control.Target, key string, applyErr error) error {
	var err error
	if applyErr == nil {
		_, err = s.DB.ExecContext(ctx, `UPDATE server_settings SET applied_at = ?, last_error = NULL WHERE target = ? AND key = ?`,
			s.now().UnixMilli(), string(target), key)
	} else {
		_, err = s.DB.ExecContext(ctx, `UPDATE server_settings SET last_error = ? WHERE target = ? AND key = ?`,
			applyErr.Error(), string(target), key)
	}
	return err
}

// Status lists the bookkeeping of every managed key.
func (s *Store) Status(ctx context.Context) ([]KeyStatus, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT target, key, updated_by, updated_at, applied_at, last_error FROM server_settings ORDER BY target, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyStatus{}
	for rows.Next() {
		var (
			k         KeyStatus
			target    string
			by        sql.NullInt64
			updated   int64
			applied   sql.NullInt64
			lastError sql.NullString
		)
		if err := rows.Scan(&target, &k.Key, &by, &updated, &applied, &lastError); err != nil {
			return nil, err
		}
		k.Target = control.Target(target)
		if by.Valid {
			v := by.Int64
			k.UpdatedBy = &v
		}
		k.UpdatedAt = time.UnixMilli(updated).UTC()
		if applied.Valid {
			t := time.UnixMilli(applied.Int64).UTC()
			k.AppliedAt = &t
		}
		k.LastError = lastError.String
		out = append(out, k)
	}
	return out, rows.Err()
}
