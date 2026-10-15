// Package clientapi implements the RustDesk client wire API mounted at /api
// (login, logout, currentUser, login-options, oidc/*, heartbeat, sysinfo,
// peers, ab/*, audit/*, ...). See docs/research/client-api.md (normative) and
// SPEC section 3.
//
// TODO(stream A): implement handlers, one file per area, plus golden-JSON
// contract tests for every endpoint.
package clientapi

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Deps are the collaborators the client API needs.
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

// Register mounts the client API on r (already scoped to /api). The /api/v1/*
// sub-trees are mounted separately by the app and must not be claimed here.
func Register(r chi.Router, d Deps) {
	_ = r
	_ = d
}
