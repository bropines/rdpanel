// Package clientapi implements the RustDesk client wire API mounted at /api
// (login, logout, currentUser, address books, accessible users/peers,
// heartbeat, sysinfo, audit, device provisioning). See
// docs/research/client-api.md (normative) and SPEC section 3. The OIDC
// endpoints (/api/login-options, /api/oidc/*) live elsewhere (stream B) and
// reuse TokenIssuer and NewUserPayload from this package.
//
// Files: account.go (login/logout/currentUser), ab.go + ab_legacy.go
// (address books), accessible.go (users/peers/device groups), device.go
// (heartbeat/sysinfo), audit.go, provisioning.go (deploy/cli/switch-grant/
// record), kick.go (KickQueue), respond.go (wire helpers).
package clientapi

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/metrics"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Settings keys (JSON values in the settings table) read by the client API.
const (
	// SettingABLegacyOnly (bool, default false): answer /api/ab/personal with
	// 404 so clients use the legacy single-blob address book.
	SettingABLegacyOnly = "clientapi.ab_legacy_only"
	// SettingABMaxPeers (int, default 0 = unlimited): max_peer_one_ab, also
	// enforced on peer/add.
	SettingABMaxPeers = "clientapi.ab_max_peers"
	// SettingDeployEnabled (bool, default true): POST /api/devices/deploy.
	SettingDeployEnabled = "clientapi.deploy_enabled"
)

// TOTPVerifier is the second factor of POST /api/login. Stream B implements it
// over totp_secrets/recovery_codes; the default (NoTOTP) reports that no user
// is enrolled, so logins are single-factor.
type TOTPVerifier interface {
	// Enrolled reports whether userID has a confirmed second factor.
	Enrolled(ctx context.Context, userID int64) (bool, error)
	// Verify checks a TOTP or recovery code for userID, enforcing replay
	// protection, and reports whether it was accepted.
	Verify(ctx context.Context, userID int64, code string) (bool, error)
}

// NoTOTP is the TOTPVerifier used when 2FA is not wired: nobody is enrolled.
type NoTOTP struct{}

// Enrolled implements TOTPVerifier.
func (NoTOTP) Enrolled(context.Context, int64) (bool, error) { return false, nil }

// Verify implements TOTPVerifier.
func (NoTOTP) Verify(context.Context, int64, string) (bool, error) { return false, nil }

// Strategy is a device strategy pushed in heartbeat responses: config_options
// are merged into the client's options ("" removes a key).
type Strategy struct {
	// ModifiedAt identifies the strategy version (the client echoes it back).
	ModifiedAt    int64
	ConfigOptions map[string]string
	Extra         map[string]string
}

// StrategyProvider returns the strategy that applies to a device, or nil.
// Not implemented in v1; the heartbeat sends no strategy when unset.
type StrategyProvider interface {
	StrategyFor(ctx context.Context, d *store.Device) (*Strategy, error)
}

// RateConfig is a token-bucket rate limit: Rate requests per second with
// bursts of Burst. A zero Rate disables limiting.
type RateConfig struct {
	Rate  float64
	Burst int
}

// DefaultDeviceRate limits the unauthenticated device endpoints per client IP.
// It is generous because many devices may share one NAT address (each device
// sends a heartbeat every 3-15 s).
var DefaultDeviceRate = RateConfig{Rate: 50, Burst: 300}

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
	// Telemetry counts logins, issued tokens and audit records (nil: none).
	Telemetry *metrics.Panel

	// TOTP verifies the second factor (default NoTOTP).
	TOTP TOTPVerifier
	// Kicks delivers admin "disconnect" requests through heartbeats (default:
	// a private MemKickQueue; share one with the admin API).
	Kicks KickQueue
	// Strategies supplies heartbeat strategies (optional).
	Strategies StrategyProvider
	// DeviceRate limits unauthenticated device calls per IP (zero value:
	// DefaultDeviceRate; Rate < 0 disables).
	DeviceRate RateConfig
	// Now is the clock (default time.Now).
	Now func() time.Time
}

type handler struct {
	Deps
	st        *store.Store
	log       *slog.Logger
	startedAt time.Time
	devRate   *rateLimiter
	tokens    *TokenIssuer
}

// Register mounts the client API on r (already scoped to /api). The /api/v1/*
// sub-trees are mounted separately by the app and must not be claimed here.
func Register(r chi.Router, d Deps) {
	h := newHandler(d)

	// Account.
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.Post("/currentUser", h.requireUser(h.currentUser))

	// Address book: legacy single blob.
	r.Get("/ab", h.requireUser(h.legacyGet))
	r.Post("/ab", h.requireUser(h.legacyPush))
	r.Post("/ab/get", h.requireUser(h.legacyGet))
	// Address book: new API, read side.
	r.Post("/ab/personal", h.requireUser(h.abPersonal))
	r.Post("/ab/settings", h.requireUser(h.abSettings))
	r.Post("/ab/shared/profiles", h.requireUser(h.abSharedProfiles))
	r.Post("/ab/peers", h.requireUser(h.abPeers))
	r.Post("/ab/tags/{guid}", h.requireUser(h.abTags))
	// Address book: new API, write side (200 + empty body on success).
	r.Post("/ab/peer/add/{guid}", h.requireUser(h.abPeerAdd))
	r.Put("/ab/peer/update/{guid}", h.requireUser(h.abPeerUpdate))
	r.Delete("/ab/peer/{guid}", h.requireUser(h.abPeerDelete))
	r.Post("/ab/tag/add/{guid}", h.requireUser(h.abTagAdd))
	r.Put("/ab/tag/rename/{guid}", h.requireUser(h.abTagRename))
	r.Put("/ab/tag/update/{guid}", h.requireUser(h.abTagUpdate))
	r.Delete("/ab/tag/{guid}", h.requireUser(h.abTagDelete))

	// Accessible devices ("Group" tab).
	r.Get("/users", h.requireUser(h.users))
	r.Get("/peers", h.requireUser(h.peers))
	r.Get("/device-group/accessible", h.requireUser(h.deviceGroups))

	// Device side (no auth; id + uuid).
	r.Post("/heartbeat", h.deviceGate(h.heartbeat))
	r.Post("/sysinfo", h.deviceGate(h.sysinfo))
	r.Post("/sysinfo_ver", h.deviceGate(h.sysinfoVer))
	r.Post("/audit/conn", h.deviceGate(h.auditConn))
	r.Post("/audit/file", h.deviceGate(h.auditFile))
	r.Post("/audit/alarm", h.deviceGate(h.auditAlarm))
	r.Post("/record", h.deviceGate(h.record))
	r.Post("/switch-grant", h.deviceGate(h.switchGrant))

	// Controller side audit (Bearer).
	r.Get("/audit/conn/active", h.requireUser(h.auditConnActive))
	r.Put("/audit", h.requireUser(h.auditNote))

	// Provisioning (Bearer admin token).
	r.Post("/devices/deploy", h.deploy)
	r.Post("/devices/cli", h.deviceAssign)
}

func newHandler(d Deps) *handler {
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.TOTP == nil {
		d.TOTP = NoTOTP{}
	}
	if d.Kicks == nil {
		d.Kicks = NewMemKickQueue(0, d.Now)
	}
	if d.Limiter == nil {
		d.Limiter = auth.NewBackoff(auth.DefaultBackoffConfig, d.Now)
	}
	if d.DeviceRate == (RateConfig{}) {
		d.DeviceRate = DefaultDeviceRate
	}
	h := &handler{Deps: d, st: d.Store, log: d.Log, startedAt: d.Now()}
	h.devRate = newRateLimiter(d.DeviceRate, d.Now)
	h.tokens = NewTokenIssuer(d)
	return h
}
