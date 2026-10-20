// Package adminapi implements the panel JSON API mounted at /api/v1/admin:
// cookie sessions (auth_tokens kind=web) or personal API tokens (Bearer rdp_...,
// api_tokens.go), CSRF double-submit (X-CSRF-Token) for cookie sessions,
// TOTP, OIDC panel login and CRUD for users, groups, devices, address books,
// audit, identity providers, server control and settings. The contract is
// api/openapi.yaml; errors use httpx.WriteError ({"error":{"code","message"}}).
package adminapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/clientopts"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/federation"
	"github.com/bropines/rdpanel/internal/ldap"
	"github.com/bropines/rdpanel/internal/loginlog"
	"github.com/bropines/rdpanel/internal/logs"
	"github.com/bropines/rdpanel/internal/mailauth"
	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/oidc"
	"github.com/bropines/rdpanel/internal/store"
	"github.com/bropines/rdpanel/internal/updatecheck"
	"github.com/bropines/rdpanel/internal/websessions"
)

// Session cookie names shared with the web client (web/src/api/client.ts).
const (
	// SessionCookie holds the opaque session token (HttpOnly, SameSite=Lax,
	// Secure when the public URL is https).
	SessionCookie = "rdpanel_session"
	// CSRFCookie holds the CSRF token readable by the SPA, echoed back in the
	// CSRFHeader on every non-GET request.
	CSRFCookie = "rdpanel_csrf"
	// CSRFHeader is the double-submit header.
	CSRFHeader = "X-CSRF-Token"
)

// Deps are the collaborators the admin API needs.
type Deps struct {
	Log     *slog.Logger
	Config  *config.Config
	Store   *store.Store
	AEAD    *crypto.AEAD
	JWT     *auth.ClientJWT
	Hasher  *auth.Hasher
	Limiter auth.Limiter
	Metrics prometheus.Registerer

	// LDAP manages directory synchronization and corporate authentication (nil: disabled).
	LDAP *ldap.Service
	// WebSessions issues web client session ids bound to panel users (nil: none).
	WebSessions *websessions.Registry
	// OIDC runs provider flows (nil disables OIDC login/linking).
	OIDC *oidc.Service
	// TOTP verifies second factors (default: NewTOTPService(Store, AEAD)).
	TOTP *TOTPService
	// Mail sends e-mail codes and reset links (default: mailauth.New over
	// Store, AEAD and Config).
	Mail *mailauth.Service
	// LoginEvents records login attempts (default: loginlog.DB on Store).
	LoginEvents loginlog.Recorder
	// Telemetry counts login attempts (nil: no metrics).
	Telemetry *metrics.Panel

	// Presence reads live device presence (nil: hbbs integration disabled,
	// presence falls back to "heartbeat within 45 s").
	Presence PresenceReader
	// Servers reports the hbbs/hbbr admin channels (nil: unknown).
	Servers ServerStatusReader
	// Kicks queues heartbeat "disconnect" requests (clientapi.KickQueue).
	Kicks DeviceKicker
	// Events publishes live events such as device.updated (nil: none).
	Events EventPublisher
	// DeviceRate is the effective per-IP limit of the unauthenticated device
	// endpoints (shown read-only in /settings).
	DeviceRate DeviceRateInfo
	// ---- stream B2b: address books, audit, server (hbbs/hbbr) ----
	// PresenceSrc is the live device presence (nil: devices report offline and
	// /server/health reports the heartbeat fallback).
	PresenceSrc PresenceSource
	// Control is the hbbs/hbbr admin channel (nil: hbbs-backed /server/*
	// endpoints answer 503 server_unavailable).
	Control ServerControl
	// Reconciler owns the desired hbbs/hbbr settings (nil: /server/settings 503).
	Reconciler SettingsReconciler
	// Relays manages relays and relay agents (nil: /relays/* answer 503).
	Relays RelayManager
	// Federation manages peering and federated relays (nil: /federation/* answer 503).
	Federation *federation.Service
	// ClientOptions are the client options pushed in heartbeats (default:
	// clientopts.New over Store and AEAD; the app shares one with clientapi).
	ClientOptions *clientopts.Service
	// ConsoleLimit caps raw console commands per admin per minute (default 30).
	ConsoleLimit int
	// Logs is the recent log lines buffer behind /server/logs* (nil or
	// capacity 0: the endpoints report the buffer as disabled).
	Logs *logs.Buffer
	// LogSources are the sources the Logs page always lists (with 0 lines).
	LogSources []string
	// LogCapture is LogCaptureFiles or LogCapturePanel (default).
	LogCapture string
	// ---- end stream B2b ----

	// ---- About page (GET /about) ----
	// StartedAt is the process start (uptime); zero = when Register ran.
	StartedAt time.Time
	// Schema reports the applied and the newest embedded migration (nil: unknown).
	Schema SchemaInfo
	// Updates looks up the latest release (nil: the update check is disabled).
	Updates UpdateChecker
}

// SchemaInfo reports the database schema version (implemented by *db.DB).
type SchemaInfo interface {
	SchemaVersion(ctx context.Context) (int64, error)
	LatestSchemaVersion() (int64, error)
}

// UpdateChecker returns the cached latest release (*updatecheck.Checker).
type UpdateChecker interface {
	Latest(ctx context.Context) updatecheck.Result
}

// DeviceRateInfo is a token bucket: Rate requests per second, bursts of Burst.
type DeviceRateInfo struct {
	Rate  float64
	Burst int
}

// Register mounts the admin API on r (already scoped to /api/v1/admin).
//
// Every route gets the CSRF double-submit check (unsafe methods) and the
// session loader. Resource routes of other streams must be wrapped in
// RequireUser or RequireAdmin (see session.go).
func Register(r chi.Router, d Deps) {
	c := newAuthCore(d)
	if !c.pk.available() && d.Log != nil {
		d.Log.Info("panel passkeys disabled", "reason", c.pk.disabled.Error())
	}
	r.Use(csrfProtect, c.sess.load)
	c.registerAuth(r)
	c.registerSecurity(r)
	c.registerEmail(r)
	c.registerProviders(r)
	registerResources(r, c)
	registerAPITokens(r, c)
	registerB2b(r, d, c.sess.load)
}
