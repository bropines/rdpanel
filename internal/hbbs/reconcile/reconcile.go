package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/events"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
)

// DefaultInterval is the probe / drift-check period.
const DefaultInterval = 15 * time.Second

// hbbr list file names (read once by hbbr at startup from its working directory).
const (
	BlacklistFile = "blacklist.txt"
	BlocklistFile = "blocklist.txt"
)

// Config configures a Reconciler.
type Config struct {
	Control *control.Client
	Store   *Store
	Bus     *events.Bus
	Log     *slog.Logger
	// Registerer for metrics; may be nil.
	Registerer prometheus.Registerer
	Interval   time.Duration
	// ListDir is hbbr's working directory (where blacklist.txt/blocklist.txt
	// live, /data in the s6 image). Empty disables writing the files.
	ListDir string
}

// TargetStatus is the reconciler's view of one admin channel.
type TargetStatus struct {
	Reachable   bool          `json:"reachable"`
	Since       time.Time     `json:"since,omitempty"`
	RTT         time.Duration `json:"rtt_ns"`
	LastError   string        `json:"last_error,omitempty"`
	LastApplied time.Time     `json:"last_applied,omitempty"`
	// Applies counts full re-applications (startup, restart detection, Apply).
	Applies uint64 `json:"applies"`
	// Drifts counts keys corrected after a periodic read-back.
	Drifts uint64 `json:"drifts"`
}

// Status is the state of both admin channels.
type Status struct {
	HBBS TargetStatus `json:"hbbs"`
	HBBR TargetStatus `json:"hbbr"`
}

// Observed is what the servers currently report.
type Observed struct {
	Settings Settings `json:"settings"`
	// Errors per target ("hbbs", "hbbr"); a target with an error has its
	// fields left nil.
	Errors map[control.Target]string `json:"errors,omitempty"`
}

// Reconciler applies desired settings to hbbs/hbbr.
type Reconciler struct {
	cfg  Config
	log  *slog.Logger
	ctl  *control.Client
	pass sync.Mutex // serializes apply passes

	mu     sync.Mutex
	status map[control.Target]*TargetStatus
	known  map[control.Target]bool // reachability observed at least once

	mUp     *prometheus.GaugeVec
	mRuns   *prometheus.CounterVec
	mDrifts *prometheus.CounterVec
}

// New builds a Reconciler.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Control == nil || cfg.Store == nil {
		return nil, errors.New("reconcile: Control and Store are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	r := &Reconciler{
		cfg:    cfg,
		log:    cfg.Log,
		ctl:    cfg.Control,
		status: map[control.Target]*TargetStatus{control.HBBS: {}, control.HBBR: {}},
		known:  map[control.Target]bool{},
		mUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbb_admin", Name: "up",
			Help: "1 if the loopback admin channel of the target answered the last probe.",
		}, []string{"target"}),
		mRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "hbb_admin", Name: "reconcile_total",
			Help: "Full re-applications of desired settings, by target and result.",
		}, []string{"target", "result"}),
		mDrifts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "hbb_admin", Name: "drift_corrections_total",
			Help: "Settings corrected after a read-back showed drift, by target and key.",
		}, []string{"target", "key"}),
	}
	if cfg.Registerer != nil {
		for _, c := range []prometheus.Collector{r.mUp, r.mRuns, r.mDrifts} {
			if err := cfg.Registerer.Register(c); err != nil {
				return nil, fmt.Errorf("reconcile: register metrics: %w", err)
			}
		}
	}
	return r, nil
}

// Run probes both admin channels every Interval until ctx ends. A channel
// that is seen for the first time, or comes back after being unreachable
// (process restart), gets the full desired state; otherwise only keys whose
// read-back differs are re-applied.
func (r *Reconciler) Run(ctx context.Context) {
	if err := r.syncListFiles(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn("reconcile: write hbbr list files", slog.Any("err", err))
	}
	t := time.NewTicker(r.cfg.Interval)
	defer t.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one probe / reconcile pass (exported for tests).
func (r *Reconciler) Tick(ctx context.Context) {
	for _, t := range []control.Target{control.HBBS, control.HBBR} {
		rtt, err := r.ctl.Ping(ctx, t)
		if ctx.Err() != nil {
			return
		}
		cameUp := r.observe(t, rtt, err)
		if err != nil {
			continue
		}
		desired, lerr := r.cfg.Store.Load(ctx)
		if lerr != nil {
			r.log.Warn("reconcile: load desired settings", slog.Any("err", lerr))
			return
		}
		if cameUp {
			r.log.Info("reconcile: admin channel up, applying desired state", slog.String("target", string(t)))
			_ = r.applyTarget(ctx, t, desired, true)
		} else {
			_ = r.applyTarget(ctx, t, desired, false)
		}
	}
}

// observe records a probe result; it reports whether t just became reachable
// (first sighting or recovery).
func (r *Reconciler) observe(t control.Target, rtt time.Duration, err error) bool {
	r.mu.Lock()
	st := r.status[t]
	was, seen := st.Reachable, r.known[t]
	r.known[t] = true
	now := time.Now()
	if err == nil {
		st.Reachable, st.RTT, st.LastError = true, rtt, ""
		r.mUp.WithLabelValues(string(t)).Set(1)
	} else {
		st.Reachable, st.LastError = false, err.Error()
		r.mUp.WithLabelValues(string(t)).Set(0)
	}
	changed := !seen || was != st.Reachable
	if changed {
		st.Since = now
	}
	up := st.Reachable
	r.mu.Unlock()

	if changed {
		ev := events.Health{Component: string(t) + "-admin", Up: up}
		if err != nil {
			ev.Error = err.Error()
			r.log.Warn("reconcile: admin channel unreachable", slog.String("target", string(t)),
				slog.String("addr", r.ctl.Addr(t)), slog.Any("err", err))
		} else {
			ev.RTTMillis = float64(rtt.Microseconds()) / 1000
		}
		r.cfg.Bus.Publish(events.HBBSHealth, ev, events.Admins)
	}
	return changed && up
}

// Status returns the reconciler's view of both channels.
func (r *Reconciler) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Status{HBBS: *r.status[control.HBBS], HBBR: *r.status[control.HBBR]}
}

// Desired returns the stored desired settings.
func (r *Reconciler) Desired(ctx context.Context) (Settings, error) {
	return r.cfg.Store.Load(ctx)
}

// KeyStatus returns per-key bookkeeping (updated/applied/last error).
func (r *Reconciler) KeyStatus(ctx context.Context) ([]KeyStatus, error) {
	return r.cfg.Store.Status(ctx)
}

// Apply validates s, stores it as the complete desired state (nil fields
// become unmanaged) and pushes it to both servers. The settings are saved
// even if a server is unreachable; the error then reports the push failure
// and the reconciler retries when the server comes back.
func (r *Reconciler) Apply(ctx context.Context, s Settings, updatedBy *int64) error {
	norm, err := Normalize(s)
	if err != nil {
		return err
	}
	if err := r.cfg.Store.Save(ctx, norm, updatedBy); err != nil {
		return fmt.Errorf("reconcile: save: %w", err)
	}
	var errs []error
	if err := r.syncListFiles(ctx); err != nil {
		errs = append(errs, fmt.Errorf("write list files: %w", err))
	}
	for _, t := range []control.Target{control.HBBS, control.HBBR} {
		if err := r.applyTarget(ctx, t, norm, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Reconcile pushes the stored desired state to both servers now.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	desired, err := r.cfg.Store.Load(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range []control.Target{control.HBBS, control.HBBR} {
		if err := r.applyTarget(ctx, t, desired, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// applyTarget pushes the managed keys of t. With force every managed key is
// written; otherwise only keys whose read-back differs.
func (r *Reconciler) applyTarget(ctx context.Context, t control.Target, s Settings, force bool) error {
	r.pass.Lock()
	defer r.pass.Unlock()
	var results []keyResult
	if t == control.HBBS {
		results = r.applyHBBS(ctx, s, force)
	} else {
		results = r.applyHBBR(ctx, s, force)
	}
	var errs []error
	drifts := 0
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", t, res.key, res.err))
		}
		if res.changed && !force {
			drifts++
			r.mDrifts.WithLabelValues(string(t), res.key).Inc()
			r.log.Info("reconcile: corrected drift", slog.String("target", string(t)), slog.String("key", res.key))
		}
		if res.changed || force || res.err != nil {
			if err := r.cfg.Store.MarkApplied(ctx, t, res.key, res.err); err != nil && ctx.Err() == nil {
				r.log.Warn("reconcile: mark applied", slog.Any("err", err))
			}
		}
	}
	err := errors.Join(errs...)
	r.mu.Lock()
	st := r.status[t]
	st.Drifts += uint64(drifts)
	if force {
		st.Applies++
		if err == nil {
			st.LastApplied = time.Now()
		}
	}
	r.mu.Unlock()
	if force && len(results) > 0 {
		result := "ok"
		if err != nil {
			result = "error"
			r.log.Warn("reconcile: apply failed", slog.String("target", string(t)), slog.Any("err", err))
		}
		r.mRuns.WithLabelValues(string(t), result).Inc()
	}
	return err
}

type keyResult struct {
	key     string
	changed bool
	err     error
}

func (r *Reconciler) applyHBBS(ctx context.Context, s Settings, force bool) []keyResult {
	var out []keyResult
	ctl := r.ctl
	if s.MustLogin != nil {
		out = append(out, applyFlag(ctx, KeyMustLogin, *s.MustLogin, force, ctl.MustLogin, ctl.SetMustLogin))
	}
	relaysResent := false
	if s.AlwaysUseRelay != nil {
		res := keyResult{key: KeyAlwaysUseRelay}
		cur, err := ctl.AlwaysUseRelay(ctx)
		if force || err != nil || cur != *s.AlwaysUseRelay {
			// Upstream hbbs wipes the relay list on "aur Y|N" (it re-parses the
			// list from the literal "Y"/"N"). Remember the list and re-send it.
			keep := s.RelayServers
			if keep == nil {
				keep, _ = ctl.RelayServers(ctx)
			}
			res.changed = true
			if res.err = ctl.SetAlwaysUseRelay(ctx, *s.AlwaysUseRelay); res.err == nil {
				if len(keep) > 0 {
					res.err = ctl.SetRelayServers(ctx, keep)
					relaysResent = true
				}
				if res.err == nil {
					res.err = verifyFlag(ctx, ctl.AlwaysUseRelay, *s.AlwaysUseRelay)
				}
			}
		}
		out = append(out, res)
	}
	if s.RelayServers != nil {
		res := keyResult{key: KeyRelayServers}
		if relaysResent {
			res.changed = true
		} else {
			cur, err := ctl.RelayServers(ctx)
			if force || err != nil || relayDrift(cur, s.RelayServers) {
				res.changed = true
				res.err = ctl.SetRelayServers(ctx, s.RelayServers)
			}
		}
		out = append(out, res)
	}
	return out
}

// relayDrift reports whether hbbs' relay list deviates from desired in a way
// a re-send can fix. hbbs drops entries that fail DNS (and, with more than one
// relay, unreachable ones), so a strict subset is not drift: re-sending would
// loop forever. An empty list or a foreign entry is.
func relayDrift(cur, desired []string) bool {
	if len(cur) == 0 {
		return len(desired) > 0
	}
	for _, c := range cur {
		if !slices.Contains(desired, c) {
			return true
		}
	}
	return false
}

func applyFlag(ctx context.Context, key string, want, force bool,
	get func(context.Context) (bool, error), set func(context.Context, bool) error) keyResult {
	res := keyResult{key: key}
	cur, err := get(ctx)
	if !force && err == nil && cur == want {
		return res
	}
	res.changed = true
	if res.err = set(ctx, want); res.err == nil {
		res.err = verifyFlag(ctx, get, want)
	}
	return res
}

func verifyFlag(ctx context.Context, get func(context.Context) (bool, error), want bool) error {
	got, err := get(ctx)
	if err != nil {
		return fmt.Errorf("read-back: %w", err)
	}
	if got != want {
		return fmt.Errorf("read-back: server reports %v, want %v", got, want)
	}
	return nil
}

func (r *Reconciler) applyHBBR(ctx context.Context, s Settings, force bool) []keyResult {
	var out []keyResult
	ctl := r.ctl
	for _, l := range []struct {
		key  string
		list control.List
		want []string
	}{
		{KeyBlacklist, control.Blacklist, s.Blacklist},
		{KeyBlocklist, control.Blocklist, s.Blocklist},
	} {
		if l.want == nil {
			continue
		}
		res := keyResult{key: l.key}
		cur, err := ctl.ListIPs(ctx, l.list)
		if err != nil {
			res.changed, res.err = true, err
			out = append(out, res)
			continue
		}
		add, remove := diff(cur, l.want)
		if len(add)+len(remove) > 0 {
			res.changed = true
			if len(remove) > 0 {
				res.err = ctl.ListRemove(ctx, l.list, remove)
			}
			if res.err == nil && len(add) > 0 {
				res.err = ctl.ListAdd(ctx, l.list, add)
			}
		}
		out = append(out, res)
	}
	for _, rt := range []struct {
		key  string
		rate control.Rate
		want *float64
	}{
		{KeyLimitSpeed, control.LimitSpeed, s.LimitSpeedMbps},
		{KeyTotalBandwidth, control.TotalBandwidth, s.TotalBandwidthMbps},
		{KeySingleBandwidth, control.SingleBandwidth, s.SingleBandwidthMbps},
	} {
		if rt.want == nil {
			continue
		}
		rate := rt.rate
		out = append(out, applyFloat(ctx, rt.key, *rt.want, force, rateEqual,
			func(ctx context.Context) (float64, error) { return ctl.GetRate(ctx, rate) },
			func(ctx context.Context, v float64) error { return ctl.SetRate(ctx, rate, v) }))
	}
	if s.DowngradeThreshold != nil {
		out = append(out, applyFloat(ctx, KeyDowngradeThreshold, *s.DowngradeThreshold, force, thresholdEqual,
			ctl.DowngradeThreshold, ctl.SetDowngradeThreshold))
	}
	if s.DowngradeStartCheckSecs != nil {
		want := float64(*s.DowngradeStartCheckSecs)
		out = append(out, applyFloat(ctx, KeyDowngradeStartCheck, want, force,
			func(a, b float64) bool { return a == b },
			func(ctx context.Context) (float64, error) {
				d, err := ctl.DowngradeStartCheck(ctx)
				return d.Seconds(), err
			},
			func(ctx context.Context, v float64) error {
				return ctl.SetDowngradeStartCheck(ctx, time.Duration(v)*time.Second)
			}))
	}
	return out
}

// rateEqual compares Mb/s values: hbbr stores (v*1024*1024) as an integer
// number of bits per second and prints it back divided by 1024*1024.
func rateEqual(want, got float64) bool {
	return math.Abs(math.Floor(want*1024*1024)-math.Round(got*1024*1024)) <= 1
}

// thresholdEqual: hbbr stores (v*100) as an integer.
func thresholdEqual(want, got float64) bool {
	return math.Abs(math.Floor(want*100)/100-got) < 1e-9
}

func applyFloat(ctx context.Context, key string, want float64, force bool, eq func(want, got float64) bool,
	get func(context.Context) (float64, error), set func(context.Context, float64) error) keyResult {
	res := keyResult{key: key}
	cur, err := get(ctx)
	if !force && err == nil && eq(want, cur) {
		return res
	}
	res.changed = true
	if res.err = set(ctx, want); res.err != nil {
		return res
	}
	got, err := get(ctx)
	switch {
	case err != nil:
		res.err = fmt.Errorf("read-back: %w", err)
	case !eq(want, got):
		res.err = fmt.Errorf("read-back: server reports %v, want %v", got, want)
	}
	return res
}

func diff(cur, want []string) (add, remove []string) {
	in := func(list []string) map[string]bool {
		m := make(map[string]bool, len(list))
		for _, v := range list {
			m[v] = true
		}
		return m
	}
	c, w := in(cur), in(want)
	for _, v := range want {
		if !c[v] {
			add = append(add, v)
		}
	}
	for _, v := range cur {
		if !w[v] {
			remove = append(remove, v)
		}
	}
	return add, remove
}

// Current reads the live settings back from both servers.
func (r *Reconciler) Current(ctx context.Context) (Observed, error) {
	obs := Observed{Errors: map[control.Target]string{}}
	ctl := r.ctl
	s := &obs.Settings
	if err := func() error {
		rs, err := ctl.RelayServers(ctx)
		if err != nil {
			return err
		}
		aur, err := ctl.AlwaysUseRelay(ctx)
		if err != nil {
			return err
		}
		ml, err := ctl.MustLogin(ctx)
		if err != nil {
			return err
		}
		s.RelayServers, s.AlwaysUseRelay, s.MustLogin = rs, &aur, &ml
		return nil
	}(); err != nil {
		obs.Errors[control.HBBS] = err.Error()
	}
	if err := func() error {
		black, err := ctl.ListIPs(ctx, control.Blacklist)
		if err != nil {
			return err
		}
		block, err := ctl.ListIPs(ctx, control.Blocklist)
		if err != nil {
			return err
		}
		var rates [3]float64
		for i, rt := range []control.Rate{control.LimitSpeed, control.TotalBandwidth, control.SingleBandwidth} {
			if rates[i], err = ctl.GetRate(ctx, rt); err != nil {
				return err
			}
		}
		dt, err := ctl.DowngradeThreshold(ctx)
		if err != nil {
			return err
		}
		t, err := ctl.DowngradeStartCheck(ctx)
		if err != nil {
			return err
		}
		secs := int64(t / time.Second)
		sort.Strings(black)
		sort.Strings(block)
		s.Blacklist, s.Blocklist = black, block
		s.LimitSpeedMbps, s.TotalBandwidthMbps, s.SingleBandwidthMbps = &rates[0], &rates[1], &rates[2]
		s.DowngradeThreshold, s.DowngradeStartCheckSecs = &dt, &secs
		return nil
	}(); err != nil {
		obs.Errors[control.HBBR] = err.Error()
	}
	if len(obs.Errors) == 2 {
		return obs, errors.New("reconcile: hbbs and hbbr admin channels unreachable")
	}
	if len(obs.Errors) == 0 {
		obs.Errors = nil
	}
	return obs, nil
}

// syncListFiles writes the managed hbbr lists into ListDir (atomically, only
// when the content changed). Unmanaged lists are left untouched.
func (r *Reconciler) syncListFiles(ctx context.Context) error {
	if r.cfg.ListDir == "" {
		return nil
	}
	s, err := r.cfg.Store.Load(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range []struct {
		name string
		ips  []string
	}{{BlacklistFile, s.Blacklist}, {BlocklistFile, s.Blocklist}} {
		if f.ips == nil {
			continue
		}
		if err := writeListFile(filepath.Join(r.cfg.ListDir, f.name), f.ips); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// writeListFile writes one IP per line. hbbr splits the file on '\n' and
// takes the first space-separated token of each line.
func writeListFile(path string, ips []string) error {
	var b bytes.Buffer
	b.WriteString(strings.Join(ips, "\n"))
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b.Bytes()) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
