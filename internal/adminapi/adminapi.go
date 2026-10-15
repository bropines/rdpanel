// Package adminapi implements the panel JSON API mounted at /api/v1/admin:
// cookie sessions (auth_tokens kind=web), CSRF double-submit (X-CSRF-Token),
// TOTP, OIDC panel login and CRUD for users, groups, devices, address books,
// audit, identity providers, server control and settings. The contract is
// api/openapi.yaml; errors use httpx.WriteError ({"error":{"code","message"}}).
package adminapi

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/loginlog"
	"github.com/bropines/rustdesk-panel/internal/oidc"
	"github.com/bropines/rustdesk-panel/internal/store"
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

	// OIDC runs provider flows (nil disables OIDC login/linking).
	OIDC *oidc.Service
	// TOTP verifies second factors (default: NewTOTPService(Store, AEAD)).
	TOTP *TOTPService
	// LoginEvents records login attempts (default: loginlog.DB on Store).
	LoginEvents loginlog.Recorder

	// ---- stream B2b: address books, audit, server (hbbs/hbbr) ----
	// Presence is the live device presence (nil: devices report offline and
	// /server/health reports the heartbeat fallback).
	Presence PresenceSource
	// Control is the hbbs/hbbr admin channel (nil: hbbs-backed /server/*
	// endpoints answer 503 server_unavailable).
	Control ServerControl
	// Reconciler owns the desired hbbs/hbbr settings (nil: /server/settings 503).
	Reconciler SettingsReconciler
	// ConsoleLimit caps raw console commands per admin per minute (default 30).
	ConsoleLimit int
	// ---- end stream B2b ----
}

// Register mounts the admin API on r (already scoped to /api/v1/admin).
//
// Every route gets the CSRF double-submit check (unsafe methods) and the
// session loader. Resource routes of other streams must be wrapped in
// RequireUser or RequireAdmin (see session.go).
func Register(r chi.Router, d Deps) {
	c := newAuthCore(d)
	r.Use(csrfProtect, c.sess.load)
	c.registerAuth(r)
	c.registerSecurity(r)
	c.registerProviders(r)
	registerB2b(r, d)
}
