package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bropines/rdpanel/internal/adminapi"
	"github.com/bropines/rdpanel/internal/clientapi"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/oidc"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/web"
)

// shutdownTimeout bounds graceful shutdown of in-flight requests.
const shutdownTimeout = 15 * time.Second

// Handler builds the root HTTP handler:
//
//	/healthz, /metrics
//	/api/v1/admin/*  panel API (stream B)
//	/api/v1/events   SSE (stream C)
//	/api/v1/openapi.{yaml,json}, /api/v1/docs  API contract + Swagger UI
//	/api/*           RustDesk client API (stream A)
//	/agent/v1/*      relay agent hub (enroll, WebSocket session)
//	/agent/install.sh relay agent installer (static script, no auth)
//	/*               embedded SPA
func (a *App) Handler() http.Handler {
	cfg := a.Config

	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		httpx.RealIP(cfg.HTTP.TrustedProxyPrefixes(), cfg.HTTP.RealIPHeader),
		httpx.Logger(a.Log),
		httpx.Recoverer(a.Log),
		a.httpMetrics.Middleware,
		web.SecurityHeaders(cfg.HTTPS()),
	)

	r.Get("/healthz", a.handleHealthz)
	r.With(httpx.AllowIPs(cfg.HTTP.MetricsAllowPrefixes())).
		Handle("/metrics", promhttp.HandlerFor(a.Registry, promhttp.HandlerOpts{}))

	r.Route("/api", func(api chi.Router) {
		api.NotFound(httpx.NotFound)
		api.MethodNotAllowed(httpx.MethodNotAllowed)
		adminDeps := adminapi.Deps{
			Log: a.Log.With("component", "adminapi"), Config: cfg, Store: a.Store, AEAD: a.AEAD,
			JWT: a.JWT, Hasher: a.Hasher, Limiter: a.Limiter, Metrics: a.Registry, TOTP: a.TOTP, Mail: a.Mail,
			Telemetry:  a.Telemetry,
			DeviceRate: adminapi.DeviceRateInfo{Rate: clientapi.DefaultDeviceRate.Rate, Burst: clientapi.DefaultDeviceRate.Burst},
			StartedAt:  a.StartedAt, Schema: a.DB,
			ClientOptions: a.ClientOptions,
			Federation:    a.Federation,
			Logs:          a.Logs, LogSources: a.logSources(), LogCapture: a.logCapture(),
		}
		if a.Updates != nil {
			adminDeps.Updates = a.Updates
		}
		a.wireAdminDeps(&adminDeps)
		a.b2bDeps(&adminDeps)
		oidcSvc := oidc.NewService(oidc.Options{
			Store: a.Store, AEAD: a.AEAD, JWT: a.JWT, Log: a.Log.With("component", "oidc"),
			PublicURL: cfg.PublicURL, AdminURL: cfg.AdminURL, TokenLifetime: cfg.Auth.TokenLifetime, JWTLifetime: cfg.Auth.JWTLifetime,
			SecureCookies: cfg.AdminHTTPS(), WebLogin: adminapi.OIDCWebLogin(adminDeps),
			Telemetry: a.Telemetry,
		})
		adminDeps.OIDC = oidcSvc
		api.Route("/v1/admin", func(r chi.Router) {
			r.NotFound(httpx.NotFound)
			adminapi.Register(r, adminDeps)
		})
		eventsAuth := a.EventsAuth
		if eventsAuth == nil {
			eventsAuth = panelSessionAuth(adminDeps)
		}
		api.Route("/v1/events", func(r chi.Router) {
			r.NotFound(httpx.NotFound)
			events.Register(r, events.Deps{
				Log: a.Log.With("component", "events"), Store: a.Store, Bus: a.Events, Auth: eventsAuth,
			})
		})
		api.Route("/v1/federation", func(r chi.Router) {
			r.NotFound(httpx.NotFound)
			r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
				if a.Federation == nil {
					httpx.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "federation disabled")
					return
				}
				httpx.WriteJSON(w, http.StatusOK, a.Federation.PublicHealth(req.Context()))
			})
			r.Get("/relays", func(w http.ResponseWriter, req *http.Request) {
				if a.Federation == nil {
					httpx.WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "federation disabled")
					return
				}
				res, err := a.Federation.PublicRelays(req.Context())
				if err != nil {
					httpx.WriteError(w, http.StatusInternalServerError, "internal_error", err.Error())
					return
				}
				httpx.WriteJSON(w, http.StatusOK, res)
			})
		})
		// Admin API contract and its interactive docs (RDPANEL_API_DOCS).
		newAPIDocs(cfg, panelSessionAuth(adminDeps), a.WebFS).register(api)
		// Client OIDC endpoints (/api/login-options, /api/oidc/*) are owned by
		// internal/oidc; clientapi must not register these paths.
		oidc.Register(api, oidc.RouteDeps{Service: oidcSvc, Log: a.Log.With("component", "oidc")})
		clientapi.Register(api, clientapi.Deps{
			Log: a.Log.With("component", "clientapi"), Config: cfg, Store: a.Store, AEAD: a.AEAD,
			JWT: a.JWT, Hasher: a.Hasher, Limiter: a.Limiter, Metrics: a.Registry, Kicks: a.Kicks,
			TOTP: clientTOTP{a.TOTP}, Email: a.Mail, Telemetry: a.Telemetry,
			Strategies: a.ClientOptions,
		})
	})

	// Relay agents (`rdpanel agent`): enrollment and the WebSocket session.
	// Served next to /api/* on the public client-API host (docs/relay-agents.md).
	if a.Relays != nil {
		r.Route("/agent/v1", a.Relays.Register)
		r.Method(http.MethodGet, relays.InstallScriptPath, http.HandlerFunc(a.Relays.HandleInstallScript))
		r.Method(http.MethodHead, relays.InstallScriptPath, http.HandlerFunc(a.Relays.HandleInstallScript))
	}

	r.Handle("/*", a.SPA)
	return r
}

func (a *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.DB.PingContext(ctx); err != nil {
		a.Log.ErrorContext(ctx, "health check failed", slog.Any("err", err))
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "db": "down"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "ok"})
}

// Serve listens on the configured address and blocks until ctx is cancelled,
// then shuts down gracefully. Background workers run for the server's lifetime.
func (a *App) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.Config.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", a.Config.Listen, err)
	}
	return a.ServeListener(ctx, ln)
}

// ServeListener is Serve on an existing listener (tests use 127.0.0.1:0).
func (a *App) ServeListener(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// No WriteTimeout: /api/v1/events streams (SSE). Handlers bound their own work.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 64 << 10,
		ErrorLog:       slog.NewLogLogger(a.Log.Handler(), slog.LevelWarn),
		BaseContext:    func(net.Listener) context.Context { return ctx },
	}

	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	var wg sync.WaitGroup
	a.startWorkers(workerCtx, &wg)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	a.Log.Info("http server listening", slog.String("addr", ln.Addr().String()),
		slog.String("public_url", a.Config.PublicURL), slog.String("admin_url", a.Config.AdminURL))

	select {
	case err := <-errCh:
		stopWorkers()
		wg.Wait()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	a.Log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	stopWorkers()
	wg.Wait()
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	a.Log.Info("server stopped")
	return nil
}
