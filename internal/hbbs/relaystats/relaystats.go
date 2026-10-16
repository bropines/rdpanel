// Package relaystats exports hbbr relay activity as Prometheus gauges.
//
// Every Interval it runs the hbbr admin command "usage", which prints one
// line per active relay (see docs/research/hbbs-integration.md §1.4), and
// publishes aggregates only: the number of active relays, their summed
// current bandwidth and the bytes they have moved so far. Per-relay values
// (peer IP:port) are never used as labels.
//
// hbbr's numbers are coarse: a relay appears only after its first second,
// "current" speed is refreshed only while packets flow (an idle relay keeps
// its last value) and the line disappears when the relay closes, so there is
// no exact running total of relayed bytes.
package relaystats

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
)

// DefaultInterval is the usage poll period.
const DefaultInterval = 15 * time.Second

// Actor names usage polls in the admin-channel log.
const Actor = "metrics"

// Source runs hbbr "usage" (implemented by *control.Client).
type Source interface {
	Usage(ctx context.Context) ([]control.RelayUsage, error)
}

// Config configures a Poller.
type Config struct {
	Source Source
	// Registerer for metrics; may be nil.
	Registerer prometheus.Registerer
	Log        *slog.Logger
	Interval   time.Duration
	// Reachable, when set, gates polling: while it reports false (the
	// reconciler saw hbbr's admin channel down) the poll is skipped and the
	// gauges read zero, which avoids a second stream of connection errors.
	Reachable func() bool
}

// Poller periodically samples hbbr usage.
type Poller struct {
	cfg Config
	log *slog.Logger

	mUp        prometheus.Gauge
	mSessions  prometheus.Gauge
	mBandwidth prometheus.Gauge
	mBytes     prometheus.Gauge
	mErrors    prometheus.Counter
}

// New validates cfg and registers the gauges.
func New(cfg Config) (*Poller, error) {
	if cfg.Source == nil {
		return nil, errors.New("relaystats: Source is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p := &Poller{
		cfg: cfg,
		log: cfg.Log,
		mUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbr", Name: "usage_up",
			Help: "1 if the last hbbr \"usage\" poll succeeded.",
		}),
		mSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbr", Name: "relay_sessions",
			Help: "Active relay sessions reported by hbbr \"usage\".",
		}),
		mBandwidth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbr", Name: "relay_bandwidth_bits_per_second",
			Help: "Sum of the current speed of all active relays (bits/s, as last measured by hbbr).",
		}),
		mBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbr", Name: "relay_active_bytes",
			Help: "Bytes relayed so far by the currently active relays (drops when a relay closes).",
		}),
		mErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "hbbr", Name: "usage_errors_total",
			Help: "Failed hbbr \"usage\" polls.",
		}),
	}
	if cfg.Registerer != nil {
		for _, c := range []prometheus.Collector{p.mUp, p.mSessions, p.mBandwidth, p.mBytes, p.mErrors} {
			if err := cfg.Registerer.Register(c); err != nil {
				return nil, fmt.Errorf("relaystats: register metrics: %w", err)
			}
		}
	}
	return p, nil
}

// Run polls until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		p.PollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// PollOnce takes one sample (exported for tests).
func (p *Poller) PollOnce(ctx context.Context) {
	if p.cfg.Reachable != nil && !p.cfg.Reachable() {
		p.set(0, nil)
		return
	}
	ctx = control.WithActor(ctx, control.Actor{Name: Actor})
	rows, err := p.cfg.Source.Usage(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		p.mErrors.Inc()
		p.log.Debug("relaystats: hbbr usage", slog.Any("err", err))
		p.set(0, nil)
		return
	}
	p.set(1, rows)
}

func (p *Poller) set(up float64, rows []control.RelayUsage) {
	var kbps int64
	var mb float64
	for _, r := range rows {
		kbps += r.CurrentKbps
		mb += r.TotalMB
	}
	p.mUp.Set(up)
	p.mSessions.Set(float64(len(rows)))
	// hbbr prints bits/ms as "kb/s" (1 kb/s = 1000 bit/s) and the total as
	// bits / 8 / 1024 / 1024 ("MB" = MiB).
	p.mBandwidth.Set(float64(kbps) * 1000)
	p.mBytes.Set(mb * 1024 * 1024)
}
