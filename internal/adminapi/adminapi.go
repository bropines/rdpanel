// Package adminapi implements the panel JSON API mounted at /api/v1/admin:
// cookie sessions (auth_tokens kind=web), CSRF double-submit (X-CSRF-Token),
// TOTP, OIDC panel login and CRUD for users, groups, devices, address books,
// audit, identity providers, server control and settings. The contract is
// api/openapi.yaml; errors use httpx.WriteError ({"error":{"code","message"}}).
//
// TODO(stream B): implement handlers and complete api/openapi.yaml.
package adminapi

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
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
}

// Register mounts the admin API on r (already scoped to /api/v1/admin).
func Register(r chi.Router, d Deps) {
	_ = r
	_ = d
}
