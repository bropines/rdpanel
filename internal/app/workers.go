package app

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Janitor tuning.
const (
	janitorInterval = 10 * time.Minute
	// tokenGrace keeps expired/revoked token rows briefly for the sessions UI.
	tokenGrace = 24 * time.Hour
	// auditNonceTTL exceeds the 5 minutes clients assume for retry dedupe.
	auditNonceTTL = 15 * time.Minute
)

// startWorkers launches background loops bound to ctx.
func (a *App) startWorkers(ctx context.Context, wg *sync.WaitGroup) {
	run := func(name string, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					a.Log.Error("worker panicked", slog.String("worker", name), slog.Any("panic", p))
				}
			}()
			fn(ctx)
		}()
	}
	run("janitor", a.runJanitor)
	for name, fn := range a.logTailers() {
		run(name, fn)
	}
	run("limiter-prune", func(ctx context.Context) { a.Limiter.RunPruner(ctx, time.Minute) })
	if p := a.HBBS.Presence; p != nil {
		run("hbbs-presence", p.Run)
		run("presence-sampler", a.runPresenceSampler)
	}
	if r := a.HBBS.Reconciler; r != nil {
		run("hbbs-reconcile", r.Run)
	}
	if a.Relays != nil {
		run("relays", a.Relays.Run)
	}
	if rs := a.HBBS.RelayStats; rs != nil {
		run("hbbr-relay-stats", rs.Run)
	}
	if c := a.HBBS.Control; c != nil {
		run("hbbs-punch-requests", func(ctx context.Context) { a.runPunchCollector(ctx, c) })
	}
	if a.Webhook != nil {
		run("webhook", a.Webhook.Run)
	}
	if a.LDAP != nil {
		run("ldap-sync", a.LDAP.Run)
	}
}

func (a *App) runJanitor(ctx context.Context) {
	t := time.NewTicker(janitorInterval)
	defer t.Stop()
	for {
		a.janitorPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// janitorPass deletes stale tokens and OAuth states, and audit rows past the
// audit.retention_days setting (see purgeRetention).
func (a *App) janitorPass(ctx context.Context) {
	now := time.Now()
	a.purgeRetention(ctx, now)
	if n, err := a.Store.AuthTokens.PurgeInactive(ctx, now.Add(-tokenGrace)); err != nil {
		if ctx.Err() == nil {
			a.Log.Warn("janitor: purge tokens", slog.Any("err", err))
		}
	} else if n > 0 {
		a.Log.Info("janitor: purged tokens", slog.Int64("count", n))
	}
	for _, p := range []struct {
		name, query string
		cutoff      time.Time
	}{
		{"oauth states", `DELETE FROM oauth_states WHERE expires_at < ?`, now},
		{"webauthn challenges", `DELETE FROM webauthn_challenges WHERE expires_at < ?`, now},
		{"audit nonces", `DELETE FROM audit_nonces WHERE created_at < ?`, now.Add(-auditNonceTTL)},
		// Kept a day after expiry: the hourly e-mail caps count recent rows.
		{"email codes", `DELETE FROM email_codes WHERE expires_at < ?`, now.Add(-24 * time.Hour)},
	} {
		res, err := a.DB.ExecContext(ctx, p.query, p.cutoff.UnixMilli())
		if err != nil {
			if ctx.Err() == nil {
				a.Log.Warn("janitor: purge "+p.name, slog.Any("err", err))
			}
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			a.Log.Debug("janitor: purged "+p.name, slog.Int64("count", n))
		}
	}
}
