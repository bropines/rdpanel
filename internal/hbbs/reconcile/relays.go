package reconcile

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/bropines/rdpanel/internal/hbbs/control"
)

// RelaySource computes hbbs' relay list (internal/relays: healthy relays
// repeated by weight). A nil list leaves relay_servers unmanaged (hbbs keeps
// its -r / RELAY list); an empty list is never sent.
type RelaySource interface {
	RelayServers(ctx context.Context) []string
}

// load returns the stored desired settings with the computed relay list.
func (r *Reconciler) load(ctx context.Context) (Settings, error) {
	s, err := r.cfg.Store.Load(ctx)
	if err != nil {
		return s, err
	}
	return r.overlay(ctx, s), nil
}

// overlay replaces RelayServers with the RelaySource's list (when one is
// configured).
func (r *Reconciler) overlay(ctx context.Context, s Settings) Settings {
	if r.cfg.Relays == nil {
		return s
	}
	s.RelayServers = nil
	if rs := r.cfg.Relays.RelayServers(ctx); len(rs) > 0 {
		s.RelayServers = rs
	}
	return s
}

// ReconcileTarget pushes the desired state (with the computed relay list) to
// one server now, e.g. after the relay rotation changed. A rotation that only
// grows is not drift for the periodic check, so callers push explicitly.
func (r *Reconciler) ReconcileTarget(ctx context.Context, t control.Target) error {
	desired, err := r.load(ctx)
	if err != nil {
		return err
	}
	return r.applyTarget(ctx, t, desired, true)
}

// KeyResult is the outcome of applying one hbbr key.
type KeyResult struct {
	Key     string
	Changed bool
	Err     error
}

// ApplyHBBR applies the hbbr keys of s through ctl (nil fields stay
// unmanaged): lists are diffed, rates and thresholds are set and read back.
// With force every managed key is written. This is the hbbr half of the
// reconciler, used unchanged by `rdpanel agent` on relay hosts (where hbbr's
// admin channel is on loopback too).
func ApplyHBBR(ctx context.Context, ctl *control.Client, s Settings, force bool) []KeyResult {
	r := &Reconciler{ctl: ctl}
	res := r.applyHBBR(ctx, s, force)
	out := make([]KeyResult, 0, len(res))
	for _, k := range res {
		out = append(out, KeyResult{Key: k.key, Changed: k.changed, Err: k.err})
	}
	return out
}

// WriteListFiles writes the managed hbbr lists of s into dir (blacklist.txt,
// blocklist.txt), atomically and only when the content changed, so hbbr picks
// them up when it restarts. Unmanaged (nil) lists are left untouched.
func WriteListFiles(dir string, s Settings) error {
	var errs []error
	for _, f := range []struct {
		name string
		ips  []string
	}{{BlacklistFile, s.Blacklist}, {BlocklistFile, s.Blocklist}} {
		if f.ips == nil {
			continue
		}
		if err := writeListFile(filepath.Join(dir, f.name), f.ips); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RelayKeys are the hbbr keys of Settings (the relay profile).
var RelayKeys = []string{KeyBlacklist, KeyBlocklist, KeyLimitSpeed, KeyTotalBandwidth,
	KeySingleBandwidth, KeyDowngradeThreshold, KeyDowngradeStartCheck}

// HBBROnly returns s with every hbbs field cleared.
func HBBROnly(s Settings) Settings {
	return Settings{Blacklist: s.Blacklist, Blocklist: s.Blocklist, LimitSpeedMbps: s.LimitSpeedMbps,
		TotalBandwidthMbps: s.TotalBandwidthMbps, SingleBandwidthMbps: s.SingleBandwidthMbps,
		DowngradeThreshold: s.DowngradeThreshold, DowngradeStartCheckSecs: s.DowngradeStartCheckSecs}
}
