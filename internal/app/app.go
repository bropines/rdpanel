// Package app wires configuration, storage, auth primitives and HTTP handlers
// into a runnable server, and implements the rdpanel command line.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/clientapi"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
	"github.com/bropines/rustdesk-panel/internal/web"
	webdist "github.com/bropines/rustdesk-panel/web"
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
	// Kicks queues admin "disconnect" requests delivered by client heartbeats;
	// shared by the client API (Take) and the admin API (Kick).
	Kicks *clientapi.MemKickQueue

	httpMetrics *httpx.Metrics
}

// New opens the database (applying migrations), loads keys and builds the App.
// Call Close when done.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	a := &App{Config: cfg, Log: log, Hasher: auth.NewHasher()}
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
	a.Registry = prometheus.NewRegistry()
	a.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	a.httpMetrics = httpx.NewMetrics(a.Registry)
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
