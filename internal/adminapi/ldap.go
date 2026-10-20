package adminapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/ldap"
	"github.com/bropines/rdpanel/internal/store"
)

type ldapSettingsDTO struct {
	Enabled              bool             `json:"enabled"`
	ServerURL            string           `json:"server_url"`
	StartTLS             bool             `json:"start_tls"`
	InsecureSkipVerify   bool             `json:"insecure_skip_verify"`
	BindDN               string           `json:"bind_dn"`
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
	DefaultUserGroupName string           `json:"default_user_group_name"`
	AutoRegister         bool             `json:"auto_register"`
	SyncIntervalMins     int              `json:"sync_interval_mins"`
	LastSyncAt           *time.Time       `json:"last_sync_at"`
	LastSyncStatus       *string          `json:"last_sync_status"`
	LastSyncCount        int              `json:"last_sync_count"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

func toLDAPSettingsDTO(s *store.LDAPSettings) ldapSettingsDTO {
	var status *string
	if s.LastSyncStatus != "" {
		st := s.LastSyncStatus
		status = &st
	}
	mappings := s.GroupMappings
	if mappings == nil {
		mappings = make(map[string]int64)
	}
	return ldapSettingsDTO{
		Enabled:              s.Enabled,
		ServerURL:            s.ServerURL,
		StartTLS:             s.StartTLS,
		InsecureSkipVerify:   s.InsecureSkipVerify,
		BindDN:               s.BindDN,
		HasBindPassword:      s.HasBindPassword,
		BaseDN:               s.BaseDN,
		UserSearchBase:       s.UserSearchBase,
		UserFilter:           s.UserFilter,
		UsernameAttribute:    s.UsernameAttribute,
		DisplayNameAttribute: s.DisplayNameAttribute,
		EmailAttribute:       s.EmailAttribute,
		GroupAttribute:       s.GroupAttribute,
		GroupMappings:        mappings,
		DefaultUserGroupID:   s.DefaultUserGroupID,
		DefaultUserGroupName: s.DefaultUserGroupName,
		AutoRegister:         s.AutoRegister,
		SyncIntervalMins:     s.SyncIntervalMins,
		LastSyncAt:           s.LastSyncAt,
		LastSyncStatus:       status,
		LastSyncCount:        s.LastSyncCount,
		UpdatedAt:            s.UpdatedAt,
	}
}

type ldapSettingsInput struct {
	Enabled              *bool            `json:"enabled"`
	ServerURL            *string          `json:"server_url"`
	StartTLS             *bool            `json:"start_tls"`
	InsecureSkipVerify   *bool            `json:"insecure_skip_verify"`
	BindDN               *string          `json:"bind_dn"`
	BindPassword         *string          `json:"bind_password"`
	BaseDN               *string          `json:"base_dn"`
	UserSearchBase       *string          `json:"user_search_base"`
	UserFilter           *string          `json:"user_filter"`
	UsernameAttribute    *string          `json:"username_attribute"`
	DisplayNameAttribute *string          `json:"display_name_attribute"`
	EmailAttribute       *string          `json:"email_attribute"`
	GroupAttribute       *string          `json:"group_attribute"`
	GroupMappings        map[string]int64 `json:"group_mappings"`
	DefaultUserGroupID   opt[int64]       `json:"default_user_group_id"`
	AutoRegister         *bool            `json:"auto_register"`
	SyncIntervalMins     *int             `json:"sync_interval_mins"`
}

func (a *resources) handleGetLDAPSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := a.d.Store.LDAPSettings.Get(ctx)
	if err != nil {
		a.fail500(w, r, "get ldap settings", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toLDAPSettingsDTO(cfg))
}

func (a *resources) handleUpdateLDAPSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in ldapSettingsInput
	if !decodeJSON(w, r, &in) {
		return
	}

	cfg, err := a.d.Store.LDAPSettings.Get(ctx)
	if err != nil {
		a.fail500(w, r, "load ldap settings", err)
		return
	}

	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.ServerURL != nil {
		uStr := strings.TrimSpace(*in.ServerURL)
		if uStr != "" {
			u, err := url.Parse(uStr)
			if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Host == "" {
				validationFailed(w, "invalid server_url", map[string]string{"server_url": "must be a valid ldap:// or ldaps:// URL"})
				return
			}
		}
		cfg.ServerURL = uStr
	}
	if in.StartTLS != nil {
		cfg.StartTLS = *in.StartTLS
	}
	if in.InsecureSkipVerify != nil {
		cfg.InsecureSkipVerify = *in.InsecureSkipVerify
	}
	if in.BindDN != nil {
		cfg.BindDN = strings.TrimSpace(*in.BindDN)
	}
	if in.BaseDN != nil {
		cfg.BaseDN = strings.TrimSpace(*in.BaseDN)
	}
	if in.UserSearchBase != nil {
		cfg.UserSearchBase = strings.TrimSpace(*in.UserSearchBase)
	}
	if in.UserFilter != nil {
		f := strings.TrimSpace(*in.UserFilter)
		if f == "" {
			f = "(&(objectClass=user)(sAMAccountName=%s))"
		}
		cfg.UserFilter = f
	}
	if in.UsernameAttribute != nil {
		cfg.UsernameAttribute = strings.TrimSpace(*in.UsernameAttribute)
	}
	if in.DisplayNameAttribute != nil {
		cfg.DisplayNameAttribute = strings.TrimSpace(*in.DisplayNameAttribute)
	}
	if in.EmailAttribute != nil {
		cfg.EmailAttribute = strings.TrimSpace(*in.EmailAttribute)
	}
	if in.GroupAttribute != nil {
		cfg.GroupAttribute = strings.TrimSpace(*in.GroupAttribute)
	}
	if in.GroupMappings != nil {
		cfg.GroupMappings = in.GroupMappings
	}
	if in.DefaultUserGroupID.Set {
		cfg.DefaultUserGroupID = in.DefaultUserGroupID.ptr()
	}
	if in.AutoRegister != nil {
		cfg.AutoRegister = *in.AutoRegister
	}
	if in.SyncIntervalMins != nil {
		if *in.SyncIntervalMins < 0 {
			validationFailed(w, "invalid sync_interval_mins", map[string]string{"sync_interval_mins": "must be >= 0"})
			return
		}
		cfg.SyncIntervalMins = *in.SyncIntervalMins
	}

	if a.d.LDAP != nil {
		if err := a.d.LDAP.UpdateSettings(ctx, cfg, in.BindPassword); err != nil {
			a.fail500(w, r, "update ldap settings", err)
			return
		}
	} else {
		if err := a.d.Store.LDAPSettings.Update(ctx, cfg); err != nil {
			a.fail500(w, r, "update ldap settings", err)
			return
		}
	}

	a.audit(ctx, r, "settings.ldap_update", "settings", "", map[string]any{
		"enabled":            cfg.Enabled,
		"server_url":         cfg.ServerURL,
		"sync_interval_mins": cfg.SyncIntervalMins,
	})

	saved, err := a.d.Store.LDAPSettings.Get(ctx)
	if err != nil {
		a.fail500(w, r, "reload ldap settings", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toLDAPSettingsDTO(saved))
}

type ldapTestRequest struct {
	Settings     *ldapSettingsInput `json:"settings"`
	BindPassword *string            `json:"bind_password"`
	TestUsername string             `json:"test_username"`
	TestPassword string             `json:"test_password"`
}

func (a *resources) handleTestLDAP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req ldapTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if a.d.LDAP == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "LDAP service is not available")
		return
	}

	var testReq ldap.TestRequest
	testReq.RawBindPassword = req.BindPassword
	testReq.TestUsername = req.TestUsername
	testReq.TestPassword = req.TestPassword

	if req.Settings != nil {
		saved, err := a.d.Store.LDAPSettings.Get(ctx)
		if err != nil {
			a.fail500(w, r, "get ldap settings", err)
			return
		}
		c := *saved
		cfg := &c
		in := req.Settings
		if in.Enabled != nil {
			cfg.Enabled = *in.Enabled
		}
		if in.ServerURL != nil {
			cfg.ServerURL = strings.TrimSpace(*in.ServerURL)
		}
		if in.StartTLS != nil {
			cfg.StartTLS = *in.StartTLS
		}
		if in.InsecureSkipVerify != nil {
			cfg.InsecureSkipVerify = *in.InsecureSkipVerify
		}
		if in.BindDN != nil {
			cfg.BindDN = strings.TrimSpace(*in.BindDN)
		}
		if in.BaseDN != nil {
			cfg.BaseDN = strings.TrimSpace(*in.BaseDN)
		}
		if in.UserSearchBase != nil {
			cfg.UserSearchBase = strings.TrimSpace(*in.UserSearchBase)
		}
		if in.UserFilter != nil {
			cfg.UserFilter = strings.TrimSpace(*in.UserFilter)
		}
		if in.UsernameAttribute != nil {
			cfg.UsernameAttribute = strings.TrimSpace(*in.UsernameAttribute)
		}
		if in.DisplayNameAttribute != nil {
			cfg.DisplayNameAttribute = strings.TrimSpace(*in.DisplayNameAttribute)
		}
		if in.EmailAttribute != nil {
			cfg.EmailAttribute = strings.TrimSpace(*in.EmailAttribute)
		}
		if in.GroupAttribute != nil {
			cfg.GroupAttribute = strings.TrimSpace(*in.GroupAttribute)
		}
		if in.GroupMappings != nil {
			cfg.GroupMappings = in.GroupMappings
		}
		if in.DefaultUserGroupID.Set {
			cfg.DefaultUserGroupID = in.DefaultUserGroupID.ptr()
		}
		testReq.Settings = cfg
		if in.BindPassword != nil {
			testReq.RawBindPassword = in.BindPassword
		}
		// The saved bind password is only ever sent to the saved server as
		// the saved bind DN: otherwise an admin session or API token could
		// send it to a host of its choice.
		if testReq.RawBindPassword == nil && len(saved.BindPasswordEnc) > 0 && bindTargetChanged(saved, cfg) {
			validationFailed(w, "bind_password is required when server_url, bind_dn or TLS settings differ from the saved ones",
				map[string]string{"bind_password": "required when testing another server, bind DN or weaker TLS"})
			return
		}
	}

	res, err := a.d.LDAP.Test(ctx, testReq)
	if err != nil {
		a.fail500(w, r, "ldap test", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// bindTargetChanged reports whether test settings would send the bind
// password somewhere the saved settings do not: another scheme or host:port,
// another bind DN, or a weaker TLS setup.
func bindTargetChanged(saved, test *store.LDAPSettings) bool {
	if test.BindDN != saved.BindDN {
		return true
	}
	su, err1 := url.Parse(saved.ServerURL)
	tu, err2 := url.Parse(test.ServerURL)
	if err1 != nil || err2 != nil || !strings.EqualFold(su.Scheme, tu.Scheme) || !strings.EqualFold(su.Host, tu.Host) {
		return true
	}
	return (saved.StartTLS && !test.StartTLS) || (!saved.InsecureSkipVerify && test.InsecureSkipVerify)
}

func (a *resources) handleSyncLDAP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.d.LDAP == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "LDAP service is not available")
		return
	}

	res, err := a.d.LDAP.Sync(ctx)
	if err != nil {
		a.fail500(w, r, "ldap sync", err)
		return
	}

	a.audit(ctx, r, "settings.ldap_sync", "settings", "", map[string]any{
		"synced_count": res.SyncedCount,
		"status":       res.Status,
	})

	httpx.WriteJSON(w, http.StatusOK, res)
}
