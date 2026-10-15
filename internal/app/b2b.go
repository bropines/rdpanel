package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/bropines/rustdesk-panel/internal/adminapi"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Stream B2b wiring: hbbs collaborators of the admin API, audit retention and
// the punch-request collector.

// b2bDeps fills the hbbs collaborators of the admin API. Nil concrete values
// stay nil interfaces (the endpoints then answer 503).
func (a *App) b2bDeps(d *adminapi.Deps) {
	if p := a.HBBS.Presence; p != nil {
		d.PresenceSrc = p
	}
	if c := a.HBBS.Control; c != nil {
		d.Control = c
	}
	if r := a.HBBS.Reconciler; r != nil {
		d.Reconciler = r
	}
}

// purgeRetention deletes audit rows older than audit.retention_days.
func (a *App) purgeRetention(ctx context.Context, now time.Time) {
	n, err := a.Store.PurgeRetention(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			a.Log.Warn("janitor: audit retention", slog.Any("err", err))
		}
		return
	}
	for cat, c := range n {
		if c > 0 {
			a.Log.Info("janitor: purged audit rows", slog.String("category", cat), slog.Int64("count", c))
		}
	}
}

// Punch-request collection (docs/research/hbbs-integration.md §1.3, §1.7):
// hbbs keeps "pr" entries in memory forever until "pr -".
const (
	punchInterval = time.Minute
	punchPageSize = 1000
	// punchMaxRows bounds one collection pass.
	punchMaxRows = 100_000
)

// punchSource is the subset of control.Client the collector needs.
type punchSource interface {
	PunchRequests(ctx context.Context, start, pageSize int) ([]control.PunchRequest, error)
	ClearPunchRequests(ctx context.Context) error
}

func (a *App) runPunchCollector(ctx context.Context, src punchSource) {
	t := time.NewTicker(punchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := collectPunchRequests(ctx, src, a.Store); err != nil {
			if ctx.Err() == nil {
				a.Log.Debug("punch requests: collect", slog.Any("err", err))
			}
		} else if n > 0 {
			a.Log.Debug("punch requests: collected", slog.Int("count", n))
		}
	}
}

// collectPunchRequests pages through hbbs' punch requests, stores them and
// clears hbbs' list. Entries recorded between the last read and the clear are
// lost (hbbs offers no atomic read-and-clear).
func collectPunchRequests(ctx context.Context, src punchSource, st *store.Store) (int, error) {
	ctx = control.WithActor(ctx, control.Actor{Name: adminapi.PunchCollectorActor})
	var rows []store.PunchRequest
	for start := 0; len(rows) < punchMaxRows; start += punchPageSize {
		page, err := src.PunchRequests(ctx, start, punchPageSize)
		if err != nil {
			return 0, err
		}
		for _, p := range page {
			rows = append(rows, store.PunchRequest{At: p.Time, FromIP: p.FromIP, ToRustdeskID: p.ToID, ToIP: p.ToIP})
		}
		if len(page) < punchPageSize {
			break
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := st.PunchRequests.Insert(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), src.ClearPunchRequests(ctx)
}
