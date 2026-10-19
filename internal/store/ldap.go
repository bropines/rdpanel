package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// LDAPSettings models the single-row ldap_settings configuration.
type LDAPSettings struct {
	Enabled              bool             `json:"enabled"`
	ServerURL            string           `json:"server_url"`
	StartTLS             bool             `json:"start_tls"`
	InsecureSkipVerify   bool             `json:"insecure_skip_verify"`
	BindDN               string           `json:"bind_dn"`
	BindPasswordEnc      []byte           `json:"-"`
	HasBindPassword      bool             `json:"has_bind_password"`
	BaseDN               string           `json:"base_dn"`
	UserSearchBase       string           `json:"user_search_base"`
	UserFilter           string           `json:"user_filter"`
	UsernameAttribute    string           `json:"username_attribute"`
	DisplayNameAttribute string           `json:"display_name_attribute"`
	EmailAttribute       string           `json:"email_attribute"`
	GroupAttribute       string           `json:"group_attribute"`
	GroupMappings        map[string]int64 `json:"group_mappings"`
	DefaultUserGroupID   *int64           `json:"default_user_group_id"`
	DefaultUserGroupName string           `json:"default_user_group_name,omitempty"`
	AutoRegister         bool             `json:"auto_register"`
	SyncIntervalMins     int              `json:"sync_interval_mins"`
	LastSyncAt           *time.Time       `json:"last_sync_at"`
	LastSyncStatus       string           `json:"last_sync_status"`
	LastSyncCount        int              `json:"last_sync_count"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

// LDAPSettingsRepo is the repository for LDAP configuration.
type LDAPSettingsRepo struct {
	q   db.Querier
	now func() time.Time
}

func scanLDAPSettings(row interface{ Scan(...any) error }) (*LDAPSettings, error) {
	var (
		s                                   LDAPSettings
		en, startTLS, skipVerify, autoReg   int
		bindPwEnc                           []byte
		defaultGroupID, lastSyncAt, updated sql.NullInt64
		lastStatus, groupName               sql.NullString
		mappingsRaw                         string
	)
	err := row.Scan(
		&en, &s.ServerURL, &startTLS, &skipVerify,
		&s.BindDN, &bindPwEnc, &s.BaseDN, &s.UserSearchBase, &s.UserFilter,
		&s.UsernameAttribute, &s.DisplayNameAttribute, &s.EmailAttribute, &s.GroupAttribute,
		&mappingsRaw, &defaultGroupID, &autoReg, &s.SyncIntervalMins,
		&lastSyncAt, &lastStatus, &s.LastSyncCount, &updated,
		&groupName,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	s.Enabled = en == 1
	s.StartTLS = startTLS == 1
	s.InsecureSkipVerify = skipVerify == 1
	s.BindPasswordEnc = bindPwEnc
	s.HasBindPassword = len(bindPwEnc) > 0
	s.AutoRegister = autoReg == 1
	if defaultGroupID.Valid {
		s.DefaultUserGroupID = &defaultGroupID.Int64
	}
	if groupName.Valid {
		s.DefaultUserGroupName = groupName.String
	}
	if lastSyncAt.Valid {
		s.LastSyncAt = ptrMs(lastSyncAt)
	}
	if lastStatus.Valid {
		s.LastSyncStatus = lastStatus.String
	}
	s.UpdatedAt = fromMs(updated.Int64)

	s.GroupMappings = make(map[string]int64)
	if mappingsRaw != "" {
		_ = json.Unmarshal([]byte(mappingsRaw), &s.GroupMappings)
	}
	return &s, nil
}

const ldapSettingsSelect = `
	SELECT s.enabled, s.server_url, s.start_tls, s.insecure_skip_verify,
		s.bind_dn, s.bind_password_enc, s.base_dn, s.user_search_base, s.user_filter,
		s.username_attribute, s.display_name_attribute, s.email_attribute, s.group_attribute,
		s.group_mappings, s.default_user_group_id, s.auto_register, s.sync_interval_mins,
		s.last_sync_at, s.last_sync_status, s.last_sync_count, s.updated_at,
		coalesce(g.name, '')
	FROM ldap_settings s
	LEFT JOIN user_groups g ON g.id = s.default_user_group_id
	WHERE s.id = 1
`

// Get returns the effective LDAP configuration.
func (r *LDAPSettingsRepo) Get(ctx context.Context) (*LDAPSettings, error) {
	return scanLDAPSettings(r.q.QueryRowContext(ctx, ldapSettingsSelect))
}

// Update saves the LDAP configuration. When s.BindPasswordEnc is nil, the existing password is kept.
func (r *LDAPSettingsRepo) Update(ctx context.Context, s *LDAPSettings) error {
	now := r.now()
	s.UpdatedAt = now
	mappingsBytes, _ := json.Marshal(s.GroupMappings)
	if s.GroupMappings == nil {
		mappingsBytes = []byte("{}")
	}

	var defaultGroupID sql.NullInt64
	if s.DefaultUserGroupID != nil {
		defaultGroupID = sql.NullInt64{Int64: *s.DefaultUserGroupID, Valid: true}
	}

	res, err := r.q.ExecContext(ctx, `
		UPDATE ldap_settings SET
			enabled = ?, server_url = ?, start_tls = ?, insecure_skip_verify = ?,
			bind_dn = ?, bind_password_enc = coalesce(?, bind_password_enc),
			base_dn = ?, user_search_base = ?, user_filter = ?,
			username_attribute = ?, display_name_attribute = ?, email_attribute = ?, group_attribute = ?,
			group_mappings = ?, default_user_group_id = ?, auto_register = ?,
			sync_interval_mins = ?, updated_at = ?
		WHERE id = 1
	`,
		boolInt(s.Enabled), s.ServerURL, boolInt(s.StartTLS), boolInt(s.InsecureSkipVerify),
		s.BindDN, s.BindPasswordEnc,
		s.BaseDN, s.UserSearchBase, s.UserFilter,
		s.UsernameAttribute, s.DisplayNameAttribute, s.EmailAttribute, s.GroupAttribute,
		string(mappingsBytes), defaultGroupID, boolInt(s.AutoRegister),
		s.SyncIntervalMins, toMs(now),
	)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res, nil)
}

// UpdateSyncResult records the timestamp, status and user count of a sync run.
func (r *LDAPSettingsRepo) UpdateSyncResult(ctx context.Context, at time.Time, status string, count int) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE ldap_settings SET
			last_sync_at = ?, last_sync_status = ?, last_sync_count = ?
		WHERE id = 1
	`, toMs(at), status, count)
	if err != nil {
		return mapErr(err)
	}
	return expectOne(res, nil)
}
