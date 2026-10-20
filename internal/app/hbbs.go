package app

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/bropines/rdpanel/internal/adminapi"
	"github.com/bropines/rdpanel/internal/authz"
	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/crypto"

	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/presence"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/hbbs/relaystats"
	"github.com/bropines/rdpanel/internal/relays"
)

// HBBS groups the hbbs/hbbr integration. Fields are nil when the integration
// is disabled (RDPANEL_HBBS_ENABLED=false); Control/Reconciler are also nil
// when the configured addresses are not loopback (the admin channel only
// accepts loopback peers).
type HBBS struct {
	Presence   *presence.Poller
	Control    *control.Client
	Reconciler *reconcile.Reconciler
	// RelayStats samples hbbr "usage" for the relay metrics.
	RelayStats *relaystats.Poller
}

// initEvents creates the event bus (always available; publishing is cheap).
func (a *App) initEvents() {
	a.Events = events.NewBus(a.Registry)
}

// initHBBS builds the presence poller, admin-channel client and reconciler.
func (a *App) initHBBS() error {
	cfg := a.Config.HBBS
	if !cfg.Enabled {
		a.Log.Info("hbbs integration disabled (RDPANEL_HBBS_ENABLED=false)")
		return nil
	}
	log := a.Log.With("component", "hbbs")
	devs := presence.SQLDevices{DB: a.DB}
	p, err := presence.New(presence.Config{
		Addr: cfg.IDServer, Devices: devs, LastSeen: devs, Bus: a.Events,
		Log: log, Registerer: a.Registry, Interval: cfg.PollInterval,
	})
	if err != nil {
		return err
	}
	a.HBBS.Presence = p
	if a.Config.HTTP.MetricsPerDevice {
		if err := a.Registry.Register(p.DeviceCollector()); err != nil {
			return err
		}
		log.Info("per-device presence metrics enabled (RDPANEL_METRICS_PER_DEVICE)")
	}

	ctl, err := control.New(control.Config{
		HBBSAddr: cfg.AdminAddr(), HBBRAddr: cfg.RelayAdminAddr(), Log: log,
	})
	if err != nil {
		log.Warn("hbbs/hbbr admin channel disabled", slog.Any("err", err))
		return nil
	}
	a.HBBS.Control = ctl
	// Persist mutating and console admin-channel calls in admin_audit.
	ctl.SetAudit(adminapi.NewCommandAudit(a.Store, log))
	rcfg := reconcile.Config{
		Control: ctl, Store: &reconcile.Store{DB: a.DB}, Bus: a.Events, Log: log,
		Registerer: a.Registry, Interval: cfg.ReconcileInterval, ListDir: cfg.ListDir,
	}
	if a.Relays != nil {
		rcfg.Relays = a.Relays // hbbs' relay list is computed from the relays table
	}
	r, err := reconcile.New(rcfg)
	if err != nil {
		return err
	}
	a.HBBS.Reconciler = r
	if a.Relays != nil {
		a.Relays.SetPusher(r, r)
		a.Relays.SetLocal(ctl, func() bool { return r.Status().HBBR.Reachable })
	}
	rs, err := relaystats.New(relaystats.Config{
		Source: ctl, Registerer: a.Registry, Log: log, Interval: cfg.ReconcileInterval,
		Reachable: func() bool { return r.Status().HBBR.Reachable },
	})
	if err != nil {
		return err
	}
	a.HBBS.RelayStats = rs
	return nil
}

// hbbsAuthzSecretBytes is the size of a generated hbbs authorization secret
// (written as hex).
const hbbsAuthzSecretBytes = 32

// initAuthz loads (or generates) the secret hbbs presents to
// /internal/hbbs/authorize and builds that endpoint (RDPANEL_HBBS_AUTHZ).
// Disabled: no endpoint and no key file.
func (a *App) initAuthz() error {
	cfg := a.Config.HBBS
	if !cfg.Authz {
		a.Log.Info("hbbs connection authorization disabled (RDPANEL_HBBS_AUTHZ=false); " +
			"connection rules are only enforced after the fact and control roles not at all")
		return nil
	}
	secret, src, err := crypto.LoadOrCreateHexSecret(cfg.AuthzSecretFile, hbbsAuthzSecretBytes)
	if err != nil {
		return fmt.Errorf("hbbs authz secret: %w", err)
	}
	a.keySecrets = append(a.keySecrets, secret)
	h, err := authz.NewHandler(&authz.Service{Store: a.Store, JWT: a.JWT}, secret, a.Config.HTTP.RealIPHeader,
		a.Registry, a.Log.With("component", "authz"))
	if err != nil {
		return err
	}
	a.hbbsAuthz = h
	a.Log.Info("hbbs connection authorization enabled", slog.String("path", authz.Path),
		slog.String("secret_file", cfg.AuthzSecretFile), slog.String("source", string(src)))
	return nil
}

// initRelays builds the relay manager / agent hub. The reconciler (built in
// initHBBS) uses it as its relay source and is installed as its pusher.
func (a *App) initRelays() error {
	cfg := a.Config
	image := "ghcr.io/bropines/rdpanel:latest"
	// Release builds carry "0.3.0" (image) or "v0.3.0" (make); pin agents to it.
	if v := strings.TrimPrefix(buildinfo.Version, "v"); v != "" && v[0] >= '0' && v[0] <= '9' && strings.Count(v, ".") == 2 && !strings.ContainsAny(v, "-+") {
		image = "ghcr.io/bropines/rdpanel:" + v
	}
	s, err := relays.New(relays.Config{
		Store: &relays.Store{DB: a.DB}, Panel: a.Store, Bus: a.Events, Log: a.Log.With("component", "relays"),
		Registerer: a.Registry, Limiter: a.Limiter, LocalHost: cfg.HBBS.RelayPublicHost, PublicURL: cfg.PublicURL,
		Image:   image,
		ListDir: cfg.HBBS.ListDir,
		PublicKey: func() (string, error) {
			raw, err := os.ReadFile(cfg.HBBS.PublicKeyFile)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(raw)), nil
		},
	})
	if err != nil {
		return err
	}
	a.Relays = s
	return nil
}
