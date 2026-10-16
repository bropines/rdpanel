// Package app wires configuration, storage, auth primitives and HTTP handlers
// into a runnable server, and implements the rdpanel command line.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/bropines/rdpanel/internal/adminapi"
	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/clientapi"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/mailauth"
	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/store"
	"github.com/bropines/rdpanel/internal/updatecheck"
	"github.com/bropines/rdpanel/internal/web"
	webdist "github.com/bropines/rdpanel/web"
)

// jwtKeyBytes is the size of a generated JWT key when RUSTDESK_API_JWT_KEY is unset.
const jwtKeyBytes = 48

// App holds every long-lived dependency. There are no package-level singletons;
// handlers receive what they need through their package's Deps.
type App struct {
	Config   *config.Config
	Log      *slog.Logger
	DB       *db.DB
	Store    *store.Store
	AEAD     *crypto.AEAD
	JWT      *auth.ClientJWT
	Hasher   *auth.Hasher
	Limiter  *auth.Backoff
	Registry *prometheus.Registry
	SPA      *web.SPA
	// Events is the in-process pub/sub behind /api/v1/events.
	Events *events.Bus
	// EventsAuth authenticates SSE streams (panel sessions, set by the admin
	// API wiring). nil denies every stream.
	EventsAuth events.Authenticator
	// HBBS is the hbbs/hbbr integration (presence, admin channel, reconciler).
	HBBS HBBS
	// Relays manages relays (local, static, agents), the computed hbbs relay
	// list and the agent hub at /agent/v1/*.
	Relays *relays.Service
	// Kicks queues admin "disconnect" requests delivered by client heartbeats;
	// shared by the client API (Take) and the admin API (Kick).
	Kicks *clientapi.MemKickQueue
	// TOTP verifies second factors; it implements clientapi's TOTPVerifier
	// (Enrolled/Verify) for the client /api/login tfa step.
	TOTP *adminapi.TOTPService
	// Mail sends e-mail codes and password reset links (internal/mailauth).
	Mail *mailauth.Service
	// StartedAt is when New ran (About page uptime).
	StartedAt time.Time
	// Updates is the cached GitHub release check (nil when
	// RDPANEL_UPDATE_CHECK=false).
	Updates *updatecheck.Checker
	// Telemetry holds the login / client-token / audit counters.
	Telemetry *metrics.Panel

	httpMetrics *httpx.Metrics
}

// New opens the database (applying migrations), loads keys and builds the App.
// Call Close when done.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	a := &App{Config: cfg, Log: log, Hasher: auth.NewHasher(), StartedAt: time.Now()}
	if cfg.UpdateCheck {
		a.Updates = updatecheck.New("rdpanel/" + buildinfo.Version + " (+https://github.com/bropines/rdpanel)")
	}
	var err error
	if a.DB, err = openDB(ctx, cfg, log); err != nil {
		return nil, err
	}
	if err := a.loadKeys(); err != nil {
		_ = a.DB.Close()
		return nil, err
	}
	a.Store = store.New(a.DB, store.WithSecrets(a.AEAD))
	a.Limiter = auth.NewBackoff(auth.DefaultBackoffConfig, nil)
	a.Kicks = clientapi.NewMemKickQueue(0, nil)
	a.TOTP = adminapi.NewTOTPService(a.Store, a.AEAD)
	a.Mail = mailauth.New(mailauth.Options{Store: a.Store, AEAD: a.AEAD, Config: cfg, Log: log.With("component", "mail")})
	a.Registry = prometheus.NewRegistry()
	a.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	a.httpMetrics = httpx.NewMetrics(a.Registry)
	a.Telemetry = metrics.New(a.Registry)
	bi := buildinfo.Get()
	metrics.RegisterBuildInfo(a.Registry, bi, bi.HBBRef)
	a.initEvents()
	if err := a.initRelays(); err != nil {
		_ = a.DB.Close()
		return nil, err
	}
	if err := a.initHBBS(); err != nil {
		_ = a.DB.Close()
		return nil, err
	}
	a.SPA = web.NewSPA(webdist.Dist())
	if !a.SPA.Built() {
		log.Warn("web UI not embedded; serving placeholder page (run `make web build`)")
	}
	return a, nil
}

// openDB opens and migrates the panel database.
func openDB(ctx context.Context, cfg *config.Config, log *slog.Logger) (*db.DB, error) {
	d, err := db.Open(ctx, cfg.DBPath, db.Options{})
	if err != nil {
		return nil, err
	}
	applied, err := d.Migrate(ctx)
	if err != nil {
		_ = d.Close()
		return nil, err
	}
	for _, m := range applied {
		log.Info("applied migration", slog.Int64("version", m.Version), slog.String("source", m.Source))
	}
	return d, nil
}

func (a *App) loadKeys() error {
	cfg := a.Config
	key, src, err := crypto.LoadAEADKey(cfg.SecretKey.Reveal(), cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	a.Log.Info("secrets-at-rest key loaded", slog.String("source", string(src)), slog.String("file", keyFileAttr(src, cfg.SecretKeyFile)))
	if a.AEAD, err = crypto.NewAEAD(key); err != nil {
		return err
	}

	jwtKey := []byte(cfg.Auth.JWTKey.Reveal())
	if len(jwtKey) == 0 {
		jwtKey, src, err = crypto.LoadOrCreateTextKey(cfg.Auth.JWTKeyFile, jwtKeyBytes)
		if err != nil {
			return fmt.Errorf("jwt key: %w", err)
		}
		a.Log.Warn("RUSTDESK_API_JWT_KEY is not set; using a key file instead. "+
			"Set the same value as RUSTDESK_API_JWT_KEY for hbbs if MUST_LOGIN is used.",
			slog.String("source", string(src)), slog.String("file", cfg.Auth.JWTKeyFile))
	}
	if a.JWT, err = auth.NewClientJWT(jwtKey, nil); err != nil {
		return err
	}
	return nil
}

func keyFileAttr(src crypto.KeySource, path string) string {
	if src == crypto.KeyFromEnv {
		return ""
	}
	return path
}

// Close releases resources.
func (a *App) Close() error {
	if a.DB == nil {
		return nil
	}
	return a.DB.Close()
}

// errUsage marks command-line usage errors (exit code 2).
var errUsage = errors.New("usage error")
