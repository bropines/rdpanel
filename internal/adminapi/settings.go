package adminapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg" // logo validation
	_ "image/png"  // logo validation
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// DefaultBrandingTitle is the panel title when none is configured.
const DefaultBrandingTitle = "RustDesk Panel"

// LogoPath is the public URL of the branding logo (relative to the site root).
const LogoPath = "/api/v1/admin/branding/logo"

// maxLogoPixels bounds the decoded logo dimensions (PNG/JPEG).
const maxLogoPixels = 4096

// brandingDTO is components.schemas.Branding.
type brandingDTO struct {
	Title           string  `json:"title"`
	LogoURL         *string `json:"logo_url"`
	WelcomeMarkdown string  `json:"welcome_markdown"`
	WebClientURL    *string `json:"web_client_url"`
}

type brandingSettings struct {
	Title           string  `json:"title"`
	WelcomeMarkdown string  `json:"welcome_markdown"`
	HasLogo         bool    `json:"has_logo"`
	WebClientURL    *string `json:"web_client_url"`
}

type authSettings struct {
	PasswordLoginEnabled     bool `json:"password_login_enabled"`
	RequireAdmin2FA          bool `json:"require_admin_2fa"`
	SessionIdleHours         int  `json:"session_idle_hours"`
	SessionAbsoluteDays      int  `json:"session_absolute_days"`
	ClientTokenLifetimeHours int  `json:"client_token_lifetime_hours"`
}

type retentionSettings struct {
	LoginEventsDays        int `json:"login_events_days"`
	ConnectionAuditsDays   int `json:"connection_audits_days"`
	FileTransferAuditsDays int `json:"file_transfer_audits_days"`
	AlarmAuditsDays        int `json:"alarm_audits_days"`
	AdminAuditDays         int `json:"admin_audit_days"`
	PunchRequestsDays      int `json:"punch_requests_days"`
}

// rateLimitInfo is the effective (read-only) rate limiting: exponential
// backoff of failed logins (panel and client, per IP and per username) and the
// token bucket of the unauthenticated device endpoints (per IP).
type rateLimitInfo struct {
	LoginFreeAttempts       int     `json:"login_free_attempts"`
	LoginBaseDelaySeconds   int     `json:"login_base_delay_seconds"`
	LoginMaxDelaySeconds    int     `json:"login_max_delay_seconds"`
	DeviceRequestsPerSecond float64 `json:"device_requests_per_second"`
	DeviceBurst             int     `json:"device_burst"`
}

// panelSettingsDTO is components.schemas.PanelSettings. public_url,
// admin_url, trusted_proxies and rate_limits are read-only effective values
// of the process configuration (environment / config file).
type panelSettingsDTO struct {
	PublicURL      string            `json:"public_url"`
	AdminURL       string            `json:"admin_url"`
	Branding       brandingSettings  `json:"branding"`
	Auth           authSettings      `json:"auth"`
	Retention      retentionSettings `json:"retention"`
	RateLimits     rateLimitInfo     `json:"rate_limits"`
	TrustedProxies []string          `json:"trusted_proxies"`
}

// intSetting describes one integer setting: key, JSON path, bounds and the
// field it maps to.
type intSetting struct {
	key, path string
	min, max  int
	field     func(*panelSettingsDTO) *int
}

func intSettings(cfg *config.Config) ([]intSetting, map[string]int) {
	defs := map[string]int{
		store.SettingSessionIdleHours: 12, store.SettingSessionAbsoluteDays: 7, store.SettingClientTokenLifetimeH: 4380,
	}
	for c, d := range store.RetentionDefaults {
		defs[store.RetentionSettingKey(c)] = d
	}
	if cfg != nil {
		if h := int(cfg.Auth.SessionIdle / time.Hour); h >= 1 {
			defs[store.SettingSessionIdleHours] = min(h, 720)
		}
		if d := int(cfg.Auth.SessionAbsolute / (24 * time.Hour)); d >= 1 {
			defs[store.SettingSessionAbsoluteDays] = min(d, 365)
		}
		if h := int(cfg.Auth.TokenLifetime / time.Hour); h >= 1 {
			defs[store.SettingClientTokenLifetimeH] = min(h, 87600)
		}
	}
	const forever = 36500
	return []intSetting{
		{store.SettingSessionIdleHours, "auth.session_idle_hours", 1, 720, func(s *panelSettingsDTO) *int { return &s.Auth.SessionIdleHours }},
		{store.SettingSessionAbsoluteDays, "auth.session_absolute_days", 1, 365, func(s *panelSettingsDTO) *int { return &s.Auth.SessionAbsoluteDays }},
		{store.SettingClientTokenLifetimeH, "auth.client_token_lifetime_hours", 1, 87600, func(s *panelSettingsDTO) *int { return &s.Auth.ClientTokenLifetimeHours }},
		{store.SettingRetentionLoginEvents, "retention.login_events_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.LoginEventsDays }},
		{store.SettingRetentionConnections, "retention.connection_audits_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.ConnectionAuditsDays }},
		{store.SettingRetentionFileTransfers, "retention.file_transfer_audits_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.FileTransferAuditsDays }},
		{store.SettingRetentionAlarms, "retention.alarm_audits_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.AlarmAuditsDays }},
		{store.SettingRetentionAdminAudit, "retention.admin_audit_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.AdminAuditDays }},
		{store.SettingRetentionPunchRequests, "retention.punch_requests_days", 0, forever, func(s *panelSettingsDTO) *int { return &s.Retention.PunchRequestsDays }},
	}, defs
}

// loadSettings reads the panel settings, falling back to the configuration
// and built-in defaults for unset keys.
func (a *resources) loadSettings(ctx context.Context) (panelSettingsDTO, error) {
	st := a.d.Store.Settings
	var (
		s   panelSettingsDTO
		err error
	)
	errs := func(e error) {
		if err == nil {
			err = e
		}
	}
	if a.d.Config != nil {
		s.PublicURL, s.AdminURL = a.d.Config.PublicURL, a.d.Config.AdminURL
		if s.AdminURL == "" {
			s.AdminURL = s.PublicURL
		}
		s.TrustedProxies = append([]string{}, a.d.Config.HTTP.TrustedProxies...)
	}
	bo := auth.DefaultBackoffConfig
	s.RateLimits = rateLimitInfo{LoginFreeAttempts: bo.Free, LoginBaseDelaySeconds: int(bo.Base / time.Second),
		LoginMaxDelaySeconds: int(bo.Max / time.Second), DeviceRequestsPerSecond: a.d.DeviceRate.Rate,
		DeviceBurst: a.d.DeviceRate.Burst}
	var e error
	s.Branding.Title, e = store.GetOr(ctx, st, store.SettingBrandingTitle, DefaultBrandingTitle)
	errs(e)
	s.Branding.WelcomeMarkdown, e = store.GetOr(ctx, st, store.SettingBrandingWelcome, "")
	errs(e)
	s.Branding.WebClientURL, e = store.GetOr[*string](ctx, st, store.SettingBrandingWebClientURL, nil)
	errs(e)
	s.Branding.HasLogo, _, e = a.d.Store.Assets().Exists(ctx, store.AssetLogo)
	errs(e)
	s.Auth.PasswordLoginEnabled, e = store.GetOr(ctx, st, store.SettingPasswordLoginEnabled, true)
	errs(e)
	s.Auth.RequireAdmin2FA, e = store.GetOr(ctx, st, store.SettingRequireAdmin2FA, false)
	errs(e)
	ints, defs := intSettings(a.d.Config)
	for _, is := range ints {
		*is.field(&s), e = store.GetOr(ctx, st, is.key, defs[is.key])
		errs(e)
	}
	if s.TrustedProxies == nil {
		s.TrustedProxies = []string{}
	}
	return s, err
}

func (a *resources) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	s, err := a.loadSettings(r.Context())
	if err != nil {
		a.fail500(w, r, "load settings", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

// panelSettingsPatch is components.schemas.PanelSettingsPatch.
type panelSettingsPatch struct {
	// Read-only (configuration): present = 400 with a pointer to the env var.
	PublicURL      json.RawMessage `json:"public_url"`
	AdminURL       json.RawMessage `json:"admin_url"`
	RateLimits     json.RawMessage `json:"rate_limits"`
	TrustedProxies json.RawMessage `json:"trusted_proxies"`
	Branding       *struct {
		Title           *string     `json:"title"`
		WelcomeMarkdown *string     `json:"welcome_markdown"`
		WebClientURL    opt[string] `json:"web_client_url"`
	} `json:"branding"`
	Auth *struct {
		PasswordLoginEnabled     *bool `json:"password_login_enabled"`
		RequireAdmin2FA          *bool `json:"require_admin_2fa"`
		SessionIdleHours         *int  `json:"session_idle_hours"`
		SessionAbsoluteDays      *int  `json:"session_absolute_days"`
		ClientTokenLifetimeHours *int  `json:"client_token_lifetime_hours"`
	} `json:"auth"`
	Retention *struct {
		LoginEventsDays        *int `json:"login_events_days"`
		ConnectionAuditsDays   *int `json:"connection_audits_days"`
		FileTransferAuditsDays *int `json:"file_transfer_audits_days"`
		AlarmAuditsDays        *int `json:"alarm_audits_days"`
		AdminAuditDays         *int `json:"admin_audit_days"`
		PunchRequestsDays      *int `json:"punch_requests_days"`
	} `json:"retention"`
}

// intPatches maps the setting keys to the patched values (nil = unchanged).
func (p *panelSettingsPatch) intPatches() map[string]*int {
	m := map[string]*int{}
	if a := p.Auth; a != nil {
		m[store.SettingSessionIdleHours], m[store.SettingSessionAbsoluteDays] = a.SessionIdleHours, a.SessionAbsoluteDays
		m[store.SettingClientTokenLifetimeH] = a.ClientTokenLifetimeHours
	}
	if r := p.Retention; r != nil {
		m[store.SettingRetentionLoginEvents], m[store.SettingRetentionConnections] = r.LoginEventsDays, r.ConnectionAuditsDays
		m[store.SettingRetentionFileTransfers], m[store.SettingRetentionAlarms] = r.FileTransferAuditsDays, r.AlarmAuditsDays
		m[store.SettingRetentionAdminAudit], m[store.SettingRetentionPunchRequests] = r.AdminAuditDays, r.PunchRequestsDays
	}
	return m
}

// validHTTPURL accepts absolute http(s) URLs without credentials or fragment
// (and, when noQuery, without a query).
func validHTTPURL(s string, noQuery bool) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		u.Fragment == "" && (!noQuery || u.RawQuery == "") && len(s) <= 2048
}

func (a *resources) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in panelSettingsPatch
	if !decodeJSON(w, r, &in) {
		return
	}
	f := map[string]string{}
	set := map[string]any{} // key -> new value
	var del []string        // keys to delete (reset to default)
	for _, ro := range []struct {
		field string
		raw   json.RawMessage
		env   string
	}{{"public_url", in.PublicURL, "RDPANEL_PUBLIC_URL"}, {"admin_url", in.AdminURL, "RDPANEL_ADMIN_URL"},
		{"trusted_proxies", in.TrustedProxies, "RDPANEL_TRUSTED_PROXIES"},
		{"rate_limits", in.RateLimits, "the server configuration"}} {
		if ro.raw != nil {
			f[ro.field] = "read-only: set via " + ro.env
		}
	}
	if b := in.Branding; b != nil {
		if b.Title != nil {
			t := strings.TrimSpace(*b.Title)
			if runes(t) > 64 {
				f["branding.title"] = "must be at most 64 characters"
			}
			set[store.SettingBrandingTitle] = t
		}
		if b.WelcomeMarkdown != nil {
			if runes(*b.WelcomeMarkdown) > 20000 {
				f["branding.welcome_markdown"] = "must be at most 20000 characters"
			}
			set[store.SettingBrandingWelcome] = *b.WelcomeMarkdown // stored raw; the UI sanitizes
		}
		if b.WebClientURL.Set {
			if b.WebClientURL.Null || strings.TrimSpace(b.WebClientURL.V) == "" {
				del = append(del, store.SettingBrandingWebClientURL)
			} else {
				u := strings.TrimSpace(b.WebClientURL.V)
				if !validHTTPURL(u, false) {
					f["branding.web_client_url"] = "must be an absolute http(s) URL"
				}
				set[store.SettingBrandingWebClientURL] = u
			}
		}
	}
	if au := in.Auth; au != nil {
		if au.PasswordLoginEnabled != nil {
			set[store.SettingPasswordLoginEnabled] = *au.PasswordLoginEnabled
		}
		if au.RequireAdmin2FA != nil {
			set[store.SettingRequireAdmin2FA] = *au.RequireAdmin2FA
		}
	}
	ints, _ := intSettings(a.d.Config)
	patches := in.intPatches()
	for _, is := range ints {
		v := patches[is.key]
		if v == nil {
			continue
		}
		if *v < is.min || *v > is.max {
			f[is.path] = "must be between " + strconv.Itoa(is.min) + " and " + strconv.Itoa(is.max)
		}
		set[is.key] = *v
	}
	if idle, abs := patches[store.SettingSessionIdleHours], patches[store.SettingSessionAbsoluteDays]; idle != nil || abs != nil {
		cur, err := a.loadSettings(ctx)
		if err != nil {
			a.fail500(w, r, "update settings", err)
			return
		}
		i, d := cur.Auth.SessionIdleHours, cur.Auth.SessionAbsoluteDays
		if idle != nil {
			i = *idle
		}
		if abs != nil {
			d = *abs
		}
		if i > d*24 {
			f["auth.session_idle_hours"] = "must not exceed the absolute session lifetime"
		}
	}
	if len(f) > 0 {
		validationFailed(w, "invalid settings", f)
		return
	}
	actor := CurrentUser(ctx).ID
	changes := diff{}
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		for k, v := range set {
			old, err := tx.Settings.GetRaw(ctx, k)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if err := tx.Settings.Set(ctx, k, v, &actor); err != nil {
				return err
			}
			if k == store.SettingBrandingWelcome {
				changes[k] = "changed"
			} else {
				changes[k] = []any{rawOrNil(old), v}
			}
		}
		for _, k := range del {
			if err := tx.Settings.Delete(ctx, k); err != nil {
				return err
			}
			changes[k] = "cleared"
		}
		return nil
	})
	if err != nil {
		a.fail500(w, r, "update settings", err)
		return
	}
	if len(changes) > 0 {
		a.audit(ctx, r, "settings.update", "settings", "", map[string]any{"changes": changes})
	}
	a.handleGetSettings(w, r)
}

func rawOrNil(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// ---- branding & logo ----

func (a *resources) branding(ctx context.Context) (brandingDTO, error) {
	st := a.d.Store.Settings
	title, err := store.GetOr(ctx, st, store.SettingBrandingTitle, DefaultBrandingTitle)
	if err != nil {
		return brandingDTO{}, err
	}
	if strings.TrimSpace(title) == "" {
		title = DefaultBrandingTitle
	}
	welcome, err := store.GetOr(ctx, st, store.SettingBrandingWelcome, "")
	if err != nil {
		return brandingDTO{}, err
	}
	web, err := store.GetOr[*string](ctx, st, store.SettingBrandingWebClientURL, nil)
	if err != nil {
		return brandingDTO{}, err
	}
	out := brandingDTO{Title: title, WelcomeMarkdown: welcome, WebClientURL: web}
	ok, sum, err := a.d.Store.Assets().Exists(ctx, store.AssetLogo)
	if err != nil {
		return brandingDTO{}, err
	}
	if ok {
		u := LogoPath + "?v=" + hex.EncodeToString(sum[:6])
		out.LogoURL = &u
	}
	return out, nil
}

func (a *resources) handleBranding(w http.ResponseWriter, r *http.Request) {
	b, err := a.branding(r.Context())
	if err != nil {
		a.fail500(w, r, "branding", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}

func (a *resources) handleBrandingLogo(w http.ResponseWriter, r *http.Request) {
	logo, err := a.d.Store.Assets().Get(r.Context(), store.AssetLogo)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "branding logo", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", logo.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(logo.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Disposition", "inline")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("ETag", `"`+hex.EncodeToString(logo.SHA256[:12])+`"`)
	if match := r.Header.Get("If-None-Match"); match != "" && match == h.Get("ETag") {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(logo.Data)
}

// sniffLogo returns the image type of data when it is a PNG, JPEG or WebP.
func sniffLogo(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

func (a *resources) handleUploadLogo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	declared, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		declared = ""
	}
	switch declared {
	case "image/png", "image/jpeg", "image/webp":
	case "image/svg+xml":
		validationFailed(w, "SVG logos are not accepted", map[string]string{"content_type": "use PNG, JPEG or WebP"})
		return
	default:
		validationFailed(w, "unsupported logo type", map[string]string{"content_type": "must be image/png, image/jpeg or image/webp"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, store.MaxAssetBytes+1))
	if err != nil {
		badRequest(w, "could not read the upload")
		return
	}
	if len(data) == 0 || len(data) > store.MaxAssetBytes {
		validationFailed(w, "logo must be 1 byte to 256 KiB", map[string]string{"body": "must be at most 256 KiB"})
		return
	}
	if got := sniffLogo(data); got != declared {
		validationFailed(w, "file content does not match the declared image type", map[string]string{"body": "not a valid " + declared})
		return
	}
	if declared != "image/webp" { // stdlib has no WebP decoder; the signature check above applies
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxLogoPixels || cfg.Height > maxLogoPixels {
			validationFailed(w, "invalid image", map[string]string{"body": "image is corrupt or larger than 4096x4096"})
			return
		}
	}
	actor := CurrentUser(ctx).ID
	asset := &store.Asset{Name: store.AssetLogo, ContentType: declared, Data: data}
	if err := a.d.Store.Assets().Put(ctx, asset, &actor); err != nil {
		a.fail500(w, r, "upload logo", err)
		return
	}
	a.audit(ctx, r, "settings.logo_upload", "settings", "", map[string]any{"content_type": declared, "bytes": len(data),
		"sha256": hex.EncodeToString(asset.SHA256)})
	a.handleBranding(w, r)
}

func (a *resources) handleDeleteLogo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	existed, err := a.d.Store.Assets().Delete(ctx, store.AssetLogo)
	if err != nil {
		a.fail500(w, r, "delete logo", err)
		return
	}
	if existed {
		a.audit(ctx, r, "settings.logo_delete", "settings", "", nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
