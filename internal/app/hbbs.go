package app

import (
	"log/slog"

	"github.com/bropines/rustdesk-panel/internal/adminapi"

	"github.com/bropines/rustdesk-panel/internal/events"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
	"github.com/bropines/rustdesk-panel/internal/hbbs/presence"
	"github.com/bropines/rustdesk-panel/internal/hbbs/reconcile"
	"github.com/bropines/rustdesk-panel/internal/hbbs/relaystats"
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
	devs := presence.SQLDevices{DB: a.DB.DB}
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
	r, err := reconcile.New(reconcile.Config{
		Control: ctl, Store: &reconcile.Store{DB: a.DB}, Bus: a.Events, Log: log,
		Registerer: a.Registry, Interval: cfg.ReconcileInterval, ListDir: cfg.ListDir,
	})
	if err != nil {
		return err
	}
	a.HBBS.Reconciler = r
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
