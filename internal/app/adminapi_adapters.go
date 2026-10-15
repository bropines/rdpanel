package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/bropines/rustdesk-panel/internal/adminapi"
	"github.com/bropines/rustdesk-panel/internal/hbbs/presence"
	"github.com/bropines/rustdesk-panel/internal/hbbs/reconcile"
)

// presenceAdapter exposes *presence.Poller as adminapi.PresenceReader.
type presenceAdapter struct{ p *presence.Poller }

func (a presenceAdapter) OnlineIDs() []string {
	snap := a.p.Snapshot()
	out := make([]string, 0, len(snap))
	for _, s := range snap {
		if s.Online {
			out = append(out, s.ID)
		}
	}
	return out
}

func (a presenceAdapter) DevicePresence(id string) (bool, time.Time, bool) {
	s, ok := a.p.Get(id)
	return s.Online, s.Since, ok
}

func (a presenceAdapter) PresenceHealth() adminapi.PresenceHealth {
	h := a.p.Health()
	return adminapi.PresenceHealth{Polls: h.Polls, Reachable: h.Reachable, Since: h.Since, LastOK: h.LastOK,
		RTT: h.RTT, LastError: h.LastError}
}

// reconcilerAdapter exposes *reconcile.Reconciler as adminapi.ServerStatusReader.
type reconcilerAdapter struct{ r *reconcile.Reconciler }

func channel(t reconcile.TargetStatus) adminapi.ChannelStatus {
	return adminapi.ChannelStatus{Known: t.Reachable || !t.Since.IsZero() || t.LastError != "",
		Reachable: t.Reachable, Since: t.Since, RTT: t.RTT, LastError: t.LastError}
}

func (a reconcilerAdapter) ChannelStatus() (adminapi.ChannelStatus, adminapi.ChannelStatus) {
	s := a.r.Status()
	return channel(s.HBBS), channel(s.HBBR)
}

// SettingsInSync: every stored desired key was applied after its last
// update without error.
func (a reconcilerAdapter) SettingsInSync(ctx context.Context) (bool, error) {
	keys, err := a.r.KeyStatus(ctx)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if k.LastError != "" || k.AppliedAt == nil || k.AppliedAt.Before(k.UpdatedAt) {
			return false, nil
		}
	}
	return true, nil
}

// wireAdminDeps fills the hbbs-related collaborators of the admin API.
func (a *App) wireAdminDeps(d *adminapi.Deps) {
	if a.HBBS.Presence != nil {
		d.Presence = presenceAdapter{a.HBBS.Presence}
	}
	if a.HBBS.Reconciler != nil {
		d.Servers = reconcilerAdapter{a.HBBS.Reconciler}
	}
	if a.Kicks != nil {
		d.Kicks = a.Kicks
	}
	if a.Events != nil {
		d.Events = a.Events
	}
}

// presenceSampleInterval is how often the dashboard online series is sampled.
const presenceSampleInterval = time.Minute

// presenceSampleKeep bounds the stored hourly samples.
const presenceSampleKeep = 7 * 24 * time.Hour

// runPresenceSampler records the hourly maximum of online devices.
func (a *App) runPresenceSampler(ctx context.Context) {
	reader := presenceAdapter{a.HBBS.Presence}
	t := time.NewTicker(presenceSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := adminapi.SampleOnline(ctx, a.Store, reader); err != nil && ctx.Err() == nil {
			a.Log.Warn("presence sampler", slog.Any("err", err))
		}
		if _, err := a.Store.PresenceSamples().PurgeBefore(ctx, time.Now().Add(-presenceSampleKeep)); err != nil && ctx.Err() == nil {
			a.Log.Warn("presence sampler: purge", slog.Any("err", err))
		}
	}
}
