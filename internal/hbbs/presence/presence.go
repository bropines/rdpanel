// Package presence polls hbbs for the online state of every known device.
//
// Every Interval the poller sends one batched OnlineRequest per ChunkSize ids
// to the hbbs rendezvous TCP port (127.0.0.1:21116 by default), skipping the
// KeyExchange frame hbbs sends unsolicited on connect, and decodes the
// MSB-first bitmap of the OnlineResponse. Transitions are published on the
// event bus; devices.last_seen_at is persisted at most once per device per
// LastSeenEvery. When hbbs is unreachable the poller falls back to the client
// heartbeat (devices.last_heartbeat_at within HeartbeatFallback), if the
// device source can provide it.
package presence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/wire"
)

// Sources of a presence observation.
const (
	SourceHBBS      = "hbbs"
	SourceHeartbeat = "heartbeat"
)

// Defaults.
const (
	DefaultInterval          = 5 * time.Second
	DefaultTimeout           = 2 * time.Second
	DefaultChunkSize         = 500
	DefaultLastSeenEvery     = 60 * time.Second
	DefaultHeartbeatFallback = 45 * time.Second
	// maxResponseFrame bounds what we accept from hbbs (a 500-id bitmap is 63 bytes;
	// a KeyExchange is ~100 bytes).
	maxResponseFrame = 64 << 10
	// maxFrames bounds how many non-response frames we skip.
	maxFrames = 8
)

// DeviceLister lists the RustDesk ids whose presence should be tracked.
type DeviceLister interface {
	ListRustdeskIDs(ctx context.Context) ([]string, error)
}

// DeviceRef is the richer per-device data a lister may provide.
type DeviceRef struct {
	ID string
	// OwnerUserID scopes presence events to the owner (plus admins); nil = admins only.
	OwnerUserID *int64
	// LastHeartbeatAt is the last /api/heartbeat (zero if never); used while hbbs is down.
	LastHeartbeatAt time.Time
}

// DeviceRefLister is an optional extension of DeviceLister.
type DeviceRefLister interface {
	ListDeviceRefs(ctx context.Context) ([]DeviceRef, error)
}

// LastSeenWriter persists last-seen timestamps (devices.last_seen_at).
type LastSeenWriter interface {
	TouchLastSeen(ctx context.Context, seen map[string]time.Time) error
}

// Config configures a Poller.
type Config struct {
	// Addr is the hbbs rendezvous TCP address, e.g. 127.0.0.1:21116.
	Addr    string
	Devices DeviceLister
	// LastSeen may be nil (no persistence).
	LastSeen LastSeenWriter
	Bus      *events.Bus
	Log      *slog.Logger
	// Registerer for metrics; may be nil.
	Registerer prometheus.Registerer

	Interval          time.Duration
	Timeout           time.Duration
	ChunkSize         int
	LastSeenEvery     time.Duration
	HeartbeatFallback time.Duration

	// Dial and Now are injectable for tests.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Now  func() time.Time
}

// State is the presence of one device.
type State struct {
	ID     string `json:"id"`
	Online bool   `json:"online"`
	// Since is when the current state was first observed.
	Since time.Time `json:"since"`
	// LastSeen is the last observation online (zero if never since start).
	LastSeen time.Time `json:"last_seen,omitempty"`
	Source   string    `json:"source"`
}

// Health describes hbbs reachability as seen by the poller.
type Health struct {
	Reachable bool `json:"reachable"`
	// Since is when Reachable last changed.
	Since time.Time `json:"since"`
	// LastOK is the last successful poll.
	LastOK time.Time `json:"last_ok,omitempty"`
	// RTT is the duration of the last successful round trip (connect + request + response).
	RTT       time.Duration `json:"rtt_ns"`
	LastError string        `json:"last_error,omitempty"`
	// Polls / Failures count poll cycles since start.
	Polls    uint64 `json:"polls"`
	Failures uint64 `json:"failures"`
	// Devices / Online summarise the current map.
	Devices int `json:"devices"`
	Online  int `json:"online"`
}

type entry struct {
	State
	owner     *int64
	persisted time.Time // last time LastSeen was written to the DB
}

// Poller tracks device presence. Its read methods are safe for concurrent use.
type Poller struct {
	cfg Config
	log *slog.Logger

	mu      sync.RWMutex
	devices map[string]*entry
	health  Health
	started bool

	mUp       prometheus.Gauge
	mRTT      prometheus.Gauge
	mDuration prometheus.Histogram
	mErrors   prometheus.Counter
	mOnline   prometheus.Gauge
	mKnown    prometheus.Gauge
}

// New validates cfg, applies defaults and registers metrics.
func New(cfg Config) (*Poller, error) {
	if cfg.Addr == "" {
		return nil, errors.New("presence: Addr is required")
	}
	if cfg.Devices == nil {
		return nil, errors.New("presence: Devices is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = DefaultChunkSize
	}
	if cfg.LastSeenEvery <= 0 {
		cfg.LastSeenEvery = DefaultLastSeenEvery
	}
	if cfg.HeartbeatFallback <= 0 {
		cfg.HeartbeatFallback = DefaultHeartbeatFallback
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Dial == nil {
		d := &net.Dialer{}
		cfg.Dial = d.DialContext
	}
	p := &Poller{
		cfg:     cfg,
		log:     cfg.Log,
		devices: map[string]*entry{},
		mUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbs", Name: "up",
			Help: "1 if the last OnlineRequest poll of hbbs succeeded.",
		}),
		mRTT: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "hbbs", Name: "rtt_seconds",
			Help: "Round-trip time of the last successful hbbs OnlineRequest.",
		}),
		mDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "rdpanel", Subsystem: "hbbs", Name: "poll_duration_seconds",
			Help:    "Duration of a full presence poll cycle.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
		}),
		mErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "hbbs", Name: "poll_errors_total",
			Help: "Failed presence poll cycles.",
		}),
		mOnline: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "presence", Name: "online_devices",
			Help: "Devices currently online.",
		}),
		mKnown: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "presence", Name: "known_devices",
			Help: "Devices tracked by the presence poller.",
		}),
	}
	if cfg.Registerer != nil {
		for _, c := range []prometheus.Collector{p.mUp, p.mRTT, p.mDuration, p.mErrors, p.mOnline, p.mKnown} {
			if err := cfg.Registerer.Register(c); err != nil {
				return nil, fmt.Errorf("presence: register metrics: %w", err)
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

// PollOnce runs one poll cycle (exported for tests and on-demand refresh).
func (p *Poller) PollOnce(ctx context.Context) {
	start := p.cfg.Now()
	refs, err := p.listDevices(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("presence: list devices", slog.Any("err", err))
		}
		return
	}
	ids := make([]string, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	online, rtt, qerr := p.Query(ctx, ids)
	if ctx.Err() != nil {
		return
	}
	now := p.cfg.Now()
	p.mDuration.Observe(now.Sub(start).Seconds())
	p.updateHealth(now, rtt, qerr)

	source := SourceHBBS
	if qerr != nil {
		// Fallback: heartbeat within the threshold.
		source = SourceHeartbeat
		online = make([]bool, len(refs))
		for i, r := range refs {
			online[i] = !r.LastHeartbeatAt.IsZero() && now.Sub(r.LastHeartbeatAt) < p.cfg.HeartbeatFallback
		}
	}
	p.apply(ctx, now, refs, online, source)
}

func (p *Poller) listDevices(ctx context.Context) ([]DeviceRef, error) {
	if rl, ok := p.cfg.Devices.(DeviceRefLister); ok {
		return rl.ListDeviceRefs(ctx)
	}
	ids, err := p.cfg.Devices.ListRustdeskIDs(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]DeviceRef, len(ids))
	for i, id := range ids {
		refs[i] = DeviceRef{ID: id}
	}
	return refs, nil
}

func (p *Poller) updateHealth(now time.Time, rtt time.Duration, err error) {
	p.mu.Lock()
	h := &p.health
	prev := h.Reachable
	first := !p.started
	p.started = true
	h.Polls++
	if err == nil {
		h.Reachable = true
		h.LastOK = now
		h.RTT = rtt
		h.LastError = ""
		p.mUp.Set(1)
		p.mRTT.Set(rtt.Seconds())
	} else {
		h.Reachable = false
		h.Failures++
		h.LastError = err.Error()
		p.mUp.Set(0)
		p.mErrors.Inc()
	}
	changed := first || prev != h.Reachable
	if changed {
		h.Since = now
	}
	ev := events.Health{Component: "hbbs", Up: h.Reachable, Error: h.LastError}
	if err == nil {
		ev.RTTMillis = float64(rtt.Microseconds()) / 1000
	}
	p.mu.Unlock()
	if changed {
		if err != nil {
			p.log.Warn("presence: hbbs unreachable, falling back to heartbeats", slog.String("addr", p.cfg.Addr), slog.Any("err", err))
		} else {
			p.log.Info("presence: hbbs reachable", slog.String("addr", p.cfg.Addr), slog.Duration("rtt", rtt))
		}
		p.cfg.Bus.Publish(events.HBBSHealth, ev, events.Admins)
	}
}

func (p *Poller) apply(ctx context.Context, now time.Time, refs []DeviceRef, online []bool, source string) {
	var changes []events.Presence
	var auds []events.Audience
	toPersist := map[string]time.Time{}

	p.mu.Lock()
	seen := make(map[string]struct{}, len(refs))
	nOnline := 0
	for i, r := range refs {
		seen[r.ID] = struct{}{}
		on := online[i]
		e, ok := p.devices[r.ID]
		if !ok {
			e = &entry{State: State{ID: r.ID, Since: now}}
			p.devices[r.ID] = e
		}
		e.owner = r.OwnerUserID
		e.Source = source
		if on {
			nOnline++
			e.LastSeen = now
			if now.Sub(e.persisted) >= p.cfg.LastSeenEvery {
				toPersist[r.ID] = now
			}
		}
		if (!ok && on) || (ok && e.Online != on) {
			e.Online = on
			e.Since = now
			changes = append(changes, events.Presence{ID: r.ID, Online: on, Since: now, LastSeen: e.LastSeen, Source: source})
			auds = append(auds, audienceFor(r.OwnerUserID))
		}
	}
	for id := range p.devices {
		if _, ok := seen[id]; !ok {
			delete(p.devices, id) // device deleted from the panel
		}
	}
	p.health.Devices = len(p.devices)
	p.health.Online = nOnline
	p.mu.Unlock()

	p.mOnline.Set(float64(nOnline))
	p.mKnown.Set(float64(len(refs)))
	for i, c := range changes {
		p.cfg.Bus.Publish(events.PresenceChanged, c, auds[i])
	}
	if len(toPersist) > 0 && p.cfg.LastSeen != nil {
		if err := p.cfg.LastSeen.TouchLastSeen(ctx, toPersist); err != nil {
			if ctx.Err() == nil {
				p.log.Warn("presence: persist last_seen_at", slog.Any("err", err))
			}
			return
		}
		p.mu.Lock()
		for id, ts := range toPersist {
			if e, ok := p.devices[id]; ok {
				e.persisted = ts
			}
		}
		p.mu.Unlock()
	}
}

func audienceFor(owner *int64) events.Audience {
	if owner == nil {
		return events.Admins
	}
	return events.Audience{UserIDs: []int64{*owner}}
}

// Query asks hbbs for the online state of ids (in chunks). It returns one
// bool per id and the RTT of the slowest chunk. With no ids it still performs
// one empty round trip, which serves as the health probe.
func (p *Poller) Query(ctx context.Context, ids []string) ([]bool, time.Duration, error) {
	out := make([]bool, len(ids))
	var maxRTT time.Duration
	for start := 0; start == 0 || start < len(ids); start += p.cfg.ChunkSize {
		end := min(start+p.cfg.ChunkSize, len(ids))
		chunk := ids[start:end]
		t0 := time.Now()
		states, err := p.queryChunk(ctx, chunk)
		if err != nil {
			return nil, 0, err
		}
		maxRTT = max(maxRTT, time.Since(t0))
		for i := range chunk {
			out[start+i] = wire.Online(states, i)
		}
		if len(ids) == 0 {
			break
		}
	}
	return out, maxRTT, nil
}

// ErrBadResponse marks protocol violations by the peer.
var ErrBadResponse = errors.New("presence: unexpected response from hbbs")

func (p *Poller) queryChunk(ctx context.Context, ids []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	conn, err := p.cfg.Dial(ctx, "tcp", p.cfg.Addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	frame, err := wire.AppendFrame(nil, wire.EncodeOnlineRequest(ids))
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(frame); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	for i := 0; i < maxFrames; i++ {
		msg, err := wire.ReadFrame(conn, maxResponseFrame)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		env, err := wire.DecodeEnvelope(msg)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadResponse, err)
		}
		switch env.Field {
		case wire.FieldKeyExchange:
			// hbbs sends its signed ephemeral key first; we stay unencrypted.
			continue
		case wire.FieldOnlineResponse:
			states, err := wire.DecodeOnlineResponse(env.Body)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadResponse, err)
			}
			if len(states) != wire.BitmapLen(len(ids)) {
				return nil, fmt.Errorf("%w: bitmap has %d bytes for %d ids", ErrBadResponse, len(states), len(ids))
			}
			return states, nil
		default:
			continue
		}
	}
	return nil, fmt.Errorf("%w: no OnlineResponse after %d frames", ErrBadResponse, maxFrames)
}

// IsOnline reports the current presence of id (false when unknown).
func (p *Poller) IsOnline(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.devices[id]
	return ok && e.Online
}

// Get returns the state of one device.
func (p *Poller) Get(id string) (State, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.devices[id]
	if !ok {
		return State{}, false
	}
	return e.State, true
}

// Snapshot returns the state of every tracked device, sorted by id.
func (p *Poller) Snapshot() []State {
	p.mu.RLock()
	out := make([]State, 0, len(p.devices))
	for _, e := range p.devices {
		out = append(out, e.State)
	}
	p.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// deviceOnlineDesc is the per-device presence series (opt-in, see
// DeviceCollector).
var deviceOnlineDesc = prometheus.NewDesc("rdpanel_presence_device_online",
	"1 if the device is online, 0 if offline (one series per tracked device; opt-in).",
	[]string{"id"}, nil)

// DeviceCollector returns a collector exporting one rdpanel_presence_device_online
// series per tracked device, labelled only with the RustDesk id. Its
// cardinality equals the fleet size, so the app registers it only when
// RDPANEL_METRICS_PER_DEVICE is set.
func (p *Poller) DeviceCollector() prometheus.Collector { return deviceCollector{p} }

type deviceCollector struct{ p *Poller }

func (c deviceCollector) Describe(ch chan<- *prometheus.Desc) { ch <- deviceOnlineDesc }

func (c deviceCollector) Collect(ch chan<- prometheus.Metric) {
	for _, s := range c.p.Snapshot() {
		v := 0.0
		if s.Online {
			v = 1
		}
		ch <- prometheus.MustNewConstMetric(deviceOnlineDesc, prometheus.GaugeValue, v, s.ID)
	}
}

// Health returns hbbs reachability as observed by the poller.
func (p *Poller) Health() Health {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.health
}
