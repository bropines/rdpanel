// Package relays manages hbbr relays for hbbs: the co-located relay, static
// (unmanaged) relays and remote relay agents (`rdpanel agent`). It probes
// every relay end to end, keeps a health state machine, computes hbbs'
// weighted relay list (reconcile.RelaySource) and runs the agent hub at
// /agent/v1/* (enrollment, ed25519-authenticated WebSocket sessions, config
// versions and ACKs). Design: docs/design/relay-agents.md.
package relays

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/relays/agentproto"
	"github.com/bropines/rdpanel/internal/store"
)

// Defaults.
const (
	DefaultInterval      = 10 * time.Second
	DefaultProbeInterval = 60 * time.Second
	DefaultEnrollTTL     = time.Hour
	// OfflineAfter consecutive probe failures take a relay out of rotation.
	OfflineAfter = 3
	// ConfigLagLimit: an agent that has not acked its config for this long
	// is degraded.
	ConfigLagLimit = 5 * time.Minute
	// statusSaveEvery throttles last_status writes.
	statusSaveEvery = 30 * time.Second
)

// Panel settings keys (store.Settings).
const (
	SettingRequireApproval   = "relays.require_approval"
	SettingAllowPrivateHosts = "relays.allow_private_hosts"
)

// RotationPusher sends the computed relay list to hbbs now
// (*reconcile.Reconciler).
type RotationPusher interface {
	ReconcileTarget(ctx context.Context, t control.Target) error
}

// GlobalProfile returns the desired server settings; their hbbr part is the
// global relay profile (*reconcile.Reconciler).
type GlobalProfile interface {
	Desired(ctx context.Context) (reconcile.Settings, error)
}

// LocalControl is the co-located hbbs/hbbr admin channel (*control.Client).
type LocalControl interface {
	RelayServers(ctx context.Context) ([]string, error)
	Usage(ctx context.Context) ([]control.RelayUsage, error)
	Stats(ctx context.Context) (*control.RelayStats, error)
	ReloadGeo(ctx context.Context) (string, error)
}

// Config configures a Service.
type Config struct {
	Store  *Store
	Panel  *store.Store // settings and admin_audit
	Bus    *events.Bus
	Log    *slog.Logger
	Pusher RotationPusher
	Global GlobalProfile
	Local  LocalControl
	// LocalUp reports whether the local hbbr admin channel answers (nil: unknown/false).
	LocalUp func() bool
	// PublicKey returns the hbbs public key handed to agents and used by the probe.
	PublicKey func() (string, error)
	// LocalHost is the public host[:port] of the co-located hbbr; the local
	// relay row is seeded from it (empty: no local relay).
	LocalHost string
	// PublicURL is the panel's public (client API) URL, used in the compose snippet.
	PublicURL string
	// Image is the container image shown in the compose snippet.
	Image      string
	Registerer prometheus.Registerer
	Prober     *Prober
	// Limiter backs off repeated enrollment/auth failures per IP (nil: none).
	Limiter       auth.Limiter
	Interval      time.Duration
	ProbeInterval time.Duration
	EnrollTTL     time.Duration
	ClockSkew     time.Duration
	Now           func() time.Time
	// ListDir is hbbs/hbbr's working directory (where relay_map.txt lives).
	ListDir string
}

// Service is the relay manager and agent hub.
type Service struct {
	cfg  Config
	st   *Store
	log  *slog.Logger
	m    *metrics
	now  func() time.Time
	kick chan struct{}
	rate *ipRate

	cfgMu sync.Mutex // serialises desired-version bumps

	mu        sync.Mutex
	fails     map[int64]int
	probedAt  map[int64]time.Time
	conns     map[int64]*agentConn
	lastPush  string
	statusAt  map[int64]time.Time
	liveStats map[int64]agentproto.Status

	lastTraffic map[int64]trafficState
	lastPurge   time.Time

	installScript []byte // install.sh with this panel's URL and image
}

type trafficState struct {
	at             time.Time
	totalMB        float64
	activeSessions int
	bytesTotal     uint64
	relaysTotal    uint64
}

// New builds a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Panel == nil {
		return nil, errors.New("relays: Store and Panel are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.ProbeInterval <= 0 {
		cfg.ProbeInterval = DefaultProbeInterval
	}
	if cfg.EnrollTTL <= 0 {
		cfg.EnrollTTL = DefaultEnrollTTL
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = agentproto.DefaultClockSkew
	}
	if cfg.Prober == nil {
		cfg.Prober = &Prober{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Image == "" {
		cfg.Image = "ghcr.io/bropines/rdpanel:latest"
	}
	m, err := newMetrics(cfg.Registerer)
	if err != nil {
		return nil, fmt.Errorf("relays: register metrics: %w", err)
	}
	return &Service{
		cfg: cfg, st: cfg.Store, log: cfg.Log, m: m, now: cfg.Now, kick: make(chan struct{}, 1),
		rate:          newIPRate(1, 30, cfg.Now),
		installScript: renderInstallScript(cfg.PublicURL, cfg.Image),
		fails:         map[int64]int{}, probedAt: map[int64]time.Time{}, conns: map[int64]*agentConn{},
		statusAt: map[int64]time.Time{}, liveStats: map[int64]agentproto.Status{},
		lastTraffic: map[int64]trafficState{},
	}, nil
}

// SetPusher installs the rotation pusher (the reconciler is built after the
// Service because it uses the Service as its RelaySource).
func (s *Service) SetPusher(p RotationPusher, g GlobalProfile) {
	s.mu.Lock()
	s.cfg.Pusher, s.cfg.Global = p, g
	s.mu.Unlock()
}

// SetLocal installs the co-located admin channel and its reachability.
func (s *Service) SetLocal(c LocalControl, up func() bool) {
	s.mu.Lock()
	s.cfg.Local, s.cfg.LocalUp = c, up
	s.mu.Unlock()
}

func (s *Service) local() (LocalControl, func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Local, s.cfg.LocalUp
}

func (s *Service) pusher() (RotationPusher, GlobalProfile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Pusher, s.cfg.Global
}

// Notify schedules an immediate pass (health, configs, rotation).
func (s *Service) Notify() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// RelayServers implements reconcile.RelaySource.
func (s *Service) RelayServers(ctx context.Context) []string {
	rs, err := s.st.List(ctx)
	if err != nil {
		s.log.Warn("relays: list for rotation", slog.Any("err", err))
		return nil
	}
	return ComputeRotation(rs).Servers
}

// EnsureLocal seeds the local relay from Config.LocalHost: an existing
// static relay with the same host becomes the local one; otherwise a new row
// is created. A no-op when a local relay exists or LocalHost is empty.
func (s *Service) EnsureLocal(ctx context.Context) error {
	host := strings.TrimSpace(s.cfg.LocalHost)
	if host == "" {
		return nil
	}
	rs, err := s.st.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Kind == KindLocal {
			return nil
		}
	}
	for _, r := range rs {
		if r.Kind == KindStatic && r.PublicHost == host {
			_, err := s.st.DB.ExecContext(ctx, `UPDATE relays SET kind = 'local', updated_at = ? WHERE id = ?`, ms(s.now()), r.ID)
			if err == nil {
				s.log.Info("relays: static relay is the local hbbr", slog.String("host", host))
			}
			return err
		}
	}
	name := "local"
	for _, r := range rs {
		if r.Name == name {
			name = "local-hbbr"
		}
	}
	_, err = s.st.Create(ctx, NewRelay{Name: name, Kind: KindLocal, PublicHost: host, Weight: 1, InRotation: true, Health: HealthActive})
	if err == nil {
		s.log.Info("relays: seeded local relay", slog.String("host", host))
	}
	return err
}

// Run is the background loop: probes, health, configs and rotation.
func (s *Service) Run(ctx context.Context) {
	if err := s.EnsureLocal(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("relays: seed local relay", slog.Any("err", err))
	}
	if err := s.SyncPrefixRules(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("relays: sync prefix rules", slog.Any("err", err))
	}
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		s.Pass(ctx, false)
		select {
		case <-ctx.Done():
			s.closeAll()
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Pass runs one manager pass (exported for tests). With probeAll every
// probeable relay is probed regardless of its schedule.
func (s *Service) Pass(ctx context.Context, probeAll bool) {
	rs, err := s.st.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("relays: list", slog.Any("err", err))
		}
		return
	}
	s.probeDue(ctx, rs, probeAll)
	if rs, err = s.st.List(ctx); err != nil {
		return
	}
	for i := range rs {
		s.evaluate(ctx, &rs[i])
		if rs[i].Kind == KindAgent {
			if err := s.pushConfig(ctx, rs[i]); err != nil && ctx.Err() == nil {
				s.log.Warn("relays: push config", slog.String("relay", rs[i].Name), slog.Any("err", err))
			}
		}
	}
	rot := ComputeRotation(rs)
	s.updateMetrics(rs, rot)
	s.sampleTraffic(ctx, rs)
	key := strings.Join(rot.Servers, ",")
	s.mu.Lock()
	changed := key != s.lastPush
	s.mu.Unlock()
	if changed && len(rot.Servers) > 0 {
		if p, _ := s.pusher(); p != nil {
			if err := p.ReconcileTarget(ctx, control.HBBS); err != nil {
				if ctx.Err() == nil {
					s.log.Warn("relays: push rotation to hbbs", slog.Any("err", err))
				}
				return // retried on the next pass
			}
			s.log.Info("relays: rotation updated", slog.String("rs", key))
		}
		s.mu.Lock()
		s.lastPush = key
		s.mu.Unlock()
	}
	if rot.Error != "" {
		s.log.Warn("relays: rotation", slog.String("err", rot.Error))
	}
}

// probeable reports whether r gets end-to-end probes.
func (s *Service) probeable(r Relay) bool {
	if r.AdminState == StateDisabled {
		return false
	}
	if r.Kind == KindAgent && !r.Agent.Approved() {
		return false
	}
	if r.Kind == KindLocal && s.localLoopback(r) {
		return false // would hit hbbr's loopback admin branch
	}
	return true
}

func (s *Service) localLoopback(r Relay) bool {
	host, _, err := SplitHost(r.TargetHost())
	if err != nil {
		return true
	}
	return host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1"
}

func (s *Service) probeDue(ctx context.Context, rs []Relay, all bool) {
	now := s.now()
	var due []Relay
	s.mu.Lock()
	for _, r := range rs {
		if !s.probeable(r) {
			continue
		}
		if all || now.Sub(s.probedAt[r.ID]) >= s.cfg.ProbeInterval {
			due = append(due, r)
			s.probedAt[r.ID] = now
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, r := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func(r Relay) {
			defer wg.Done()
			defer func() { <-sem }()
			s.probeOne(ctx, r)
		}(r)
	}
	wg.Wait()
}

func (s *Service) allowPrivate(ctx context.Context) bool {
	var v bool
	if err := s.cfg.Panel.Settings.Get(ctx, SettingAllowPrivateHosts, &v); err != nil {
		return false
	}
	return v
}

func (s *Service) publicKey() string {
	if s.cfg.PublicKey == nil {
		return ""
	}
	k, err := s.cfg.PublicKey()
	if err != nil {
		s.log.Warn("relays: read hbbs public key", slog.Any("err", err))
		return ""
	}
	return k
}

func (s *Service) probeOne(ctx context.Context, r Relay) ProbeResult {
	// The local relay is ours: no SSRF guard (it may well be a private address).
	allow := r.Kind == KindLocal || s.allowPrivate(ctx)
	res := s.cfg.Prober.Probe(ctx, r.TargetHost(), s.publicKey(), allow)
	if ctx.Err() != nil {
		return res
	}
	var pubReachable *bool
	if r.HasSeparateMgmt() {
		err := s.cfg.Prober.TCPCheck(ctx, r.PublicHost, allow)
		ok := err == nil
		pubReachable = &ok
		if err != nil {
			s.log.Debug("relays: public host TCP check failed", slog.String("relay", r.Name), slog.String("public_host", r.PublicHost), slog.Any("err", err))
		}
	} else if res.OK() {
		ok := true
		pubReachable = &ok
	} else if res.Err != nil {
		ok := false
		pubReachable = &ok
	}
	var msPtr *float64
	errStr := ""
	s.mu.Lock()
	if res.OK() {
		s.fails[r.ID] = 0
		v := float64(res.RTT.Microseconds()) / 1000
		msPtr = &v
	} else {
		s.fails[r.ID]++
		errStr = res.Err.Error()
	}
	s.mu.Unlock()
	result := "ok"
	if !res.OK() {
		result = "error"
		s.log.Debug("relays: probe failed", slog.String("relay", r.Name), slog.Any("err", res.Err))
	} else {
		s.m.probe.WithLabelValues(r.Name).Set(res.RTT.Seconds())
	}
	s.m.probes.WithLabelValues(r.Name, result).Inc()
	if err := s.st.SetHealth(ctx, r.ID, r.Health, false, res.At, msPtr, errStr, pubReachable); err != nil && ctx.Err() == nil {
		s.log.Warn("relays: save probe", slog.Any("err", err))
	}
	return res
}

// healthInput is what the state machine looks at.
type healthInput struct {
	Kind      Kind
	Prev      Health
	Probeable bool
	Probed    bool // a probe result exists (last_probe_at set)
	ProbeOK   bool
	Fails     int
	// Agent only.
	Approved  bool
	Connected bool
	ConfigLag bool
	// Local only.
	LocalUp bool
}

// nextHealth is the relay health state machine (design §6.4):
//
//	pending  -> active   first passing probe (agent: approved, connected, acked)
//	active  <-> degraded probe passes but the agent WS is down or its config
//	                     lags > 5 min; or 1-2 failed probes
//	*        -> offline  3 failed probes in a row (local relay: only while its
//	                     admin channel is down too)
func nextHealth(in healthInput) Health {
	if in.Kind == KindAgent && !in.Approved {
		return HealthPending
	}
	if in.Kind == KindLocal && !in.Probeable {
		if in.LocalUp {
			return HealthActive
		}
		return HealthOffline
	}
	if !in.Probeable || !in.Probed {
		return in.Prev
	}
	if in.ProbeOK {
		if in.Kind == KindAgent && (!in.Connected || in.ConfigLag) {
			return HealthDegraded
		}
		return HealthActive
	}
	if in.Fails >= OfflineAfter {
		if in.Kind == KindLocal && in.LocalUp {
			return HealthDegraded // never drop our own relay while hbbr runs
		}
		return HealthOffline
	}
	if in.Prev == HealthActive || in.Prev == HealthDegraded {
		return HealthDegraded
	}
	if in.Kind == KindLocal && in.LocalUp {
		return HealthDegraded
	}
	return in.Prev
}

func (s *Service) evaluate(ctx context.Context, r *Relay) {
	s.mu.Lock()
	fails := s.fails[r.ID]
	_, connected := s.conns[r.ID]
	s.mu.Unlock()
	in := healthInput{Kind: r.Kind, Prev: r.Health, Probeable: s.probeable(*r), Probed: r.LastProbeAt != nil,
		ProbeOK: r.LastProbeAt != nil && r.LastProbeError == "", Fails: fails, Connected: connected}
	if r.LastProbeError != "" && fails == 0 {
		// Restarted panel: the stored error counts as one failure.
		in.Fails = 1
	}
	if r.Kind == KindAgent {
		in.Approved = r.Agent.Approved()
		if a := r.Agent; a.AckedVersion < a.DesiredVersion && a.DesiredAt != nil && s.now().Sub(*a.DesiredAt) > ConfigLagLimit {
			in.ConfigLag = true
		}
	}
	if _, up := s.local(); r.Kind == KindLocal && up != nil {
		in.LocalUp = up()
	}
	next := nextHealth(in)
	if next == r.Health {
		return
	}
	if err := s.st.SetHealthOnly(ctx, r.ID, next); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("relays: save health", slog.Any("err", err))
		}
		return
	}
	s.log.Info("relays: health changed", slog.String("relay", r.Name), slog.String("from", string(r.Health)),
		slog.String("to", string(next)))
	prev := r.Health
	r.Health = next
	s.publish(*r, prev)
}

func (s *Service) publish(r Relay, prev Health) {
	if s.cfg.Bus == nil {
		return
	}
	s.mu.Lock()
	_, connected := s.conns[r.ID]
	s.mu.Unlock()
	s.cfg.Bus.Publish(events.RelayHealth, events.RelayState{ID: r.ID, Name: r.Name, Health: string(r.Health),
		Previous: string(prev), Connected: connected, Error: r.LastProbeError}, events.Admins)
}

func (s *Service) updateMetrics(rs []Relay, rot Rotation) {
	s.m.up.Reset()
	s.m.health.Reset()
	s.m.active.Reset()
	s.m.configLag.Reset()
	s.m.weight.Reset()
	weights := map[int64]int{}
	for _, m := range rot.Members {
		weights[m.ID] = m.Weight
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rs {
		up := 0.0
		if r.LastProbeAt != nil && r.LastProbeError == "" {
			up = 1
		}
		s.m.up.WithLabelValues(r.Name, string(r.Kind)).Set(up)
		for _, h := range []Health{HealthPending, HealthActive, HealthDegraded, HealthOffline} {
			v := 0.0
			if r.Health == h {
				v = 1
			}
			s.m.health.WithLabelValues(r.Name, string(h)).Set(v)
		}
		s.m.weight.WithLabelValues(r.Name).Set(float64(weights[r.ID]))
		if r.Kind == KindAgent && r.Agent != nil {
			s.m.configLag.WithLabelValues(r.Name).Set(float64(r.Agent.DesiredVersion - r.Agent.AckedVersion))
			if st, ok := s.liveStats[r.ID]; ok {
				s.m.active.WithLabelValues(r.Name).Set(float64(st.Usage.RelaysActive))
			}
		}
	}
	s.m.connected.Set(float64(len(s.conns)))
	s.m.rotation.Set(float64(len(rot.Servers)))
}

// configFor computes the effective config of an agent relay and bumps the
// desired version when it changed.
func (s *Service) configFor(ctx context.Context, r Relay) (agentproto.Config, error) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	var global reconcile.Settings
	if _, g := s.pusher(); g != nil {
		d, err := g.Desired(ctx)
		if err != nil {
			return agentproto.Config{}, err
		}
		global = d
	}
	ov, err := s.st.Overrides(ctx, r.ID)
	if err != nil {
		return agentproto.Config{}, err
	}
	cfg := agentproto.Config{
		Run:           r.Agent.Approved() && r.AdminState != StateDisabled,
		HBBSPublicKey: s.publicKey(),
		Settings:      agentproto.FromReconcile(Merge(global, ov)),
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	cfg.Version = r.Agent.DesiredVersion
	if hash != r.Agent.ConfigHash || cfg.Version == 0 {
		// Re-read: another goroutine may have bumped it meanwhile.
		cur, err := s.st.Get(ctx, r.ID)
		if err != nil {
			return cfg, err
		}
		cfg.Version = cur.Agent.DesiredVersion
		if hash != cur.Agent.ConfigHash || cfg.Version == 0 {
			cfg.Version++
			if err := s.st.SetDesired(ctx, r.ID, cfg.Version, hash); err != nil {
				return cfg, err
			}
		}
	}
	return cfg, nil
}

// pushConfig sends the effective config to a connected agent when it has not
// seen that version yet.
func (s *Service) pushConfig(ctx context.Context, r Relay) error {
	if r.Agent == nil || !r.Agent.Enrolled() || r.Agent.RevokedAt != nil {
		return nil
	}
	cfg, err := s.configFor(ctx, r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	c := s.conns[r.ID]
	s.mu.Unlock()
	if c == nil || !c.helloDone() || c.sentVersion() == cfg.Version {
		return nil
	}
	return c.sendConfig(ctx, cfg)
}

// ---- admin operations ----

// View is a relay as shown in the admin API.
type View struct {
	Relay
	Connected   bool               `json:"connected"`
	ConnectedAt *time.Time         `json:"connected_at,omitempty"`
	RemoteIP    string             `json:"remote_ip,omitempty"`
	Live        *agentproto.Status `json:"live,omitempty"`
	// RotationWeight is the relay's entries in hbbs' computed list.
	RotationWeight int `json:"rotation_weight"`
}

func (s *Service) views(rs []Relay, rot Rotation) []View {
	weights := map[int64]int{}
	for _, m := range rot.Members {
		weights[m.ID] = m.Weight
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]View, 0, len(rs))
	for _, r := range rs {
		v := View{Relay: r, RotationWeight: weights[r.ID]}
		if c := s.conns[r.ID]; c != nil {
			v.Connected = true
			t := c.connectedAt
			v.ConnectedAt, v.RemoteIP = &t, c.remoteIP
		}
		if st, ok := s.liveStats[r.ID]; ok {
			st := st
			v.Live = &st
		}
		out = append(out, v)
	}
	return out
}

// List returns every relay and the computed rotation.
func (s *Service) List(ctx context.Context) ([]View, Rotation, error) {
	rs, err := s.st.List(ctx)
	if err != nil {
		return nil, Rotation{}, err
	}
	rot := ComputeRotation(rs)
	return s.views(rs, rot), rot, nil
}

// Get returns one relay.
func (s *Service) Get(ctx context.Context, id int64) (View, error) {
	rs, err := s.st.List(ctx)
	if err != nil {
		return View{}, err
	}
	rot := ComputeRotation(rs)
	for _, v := range s.views(rs, rot) {
		if v.ID == id {
			return v, nil
		}
	}
	return View{}, store.ErrNotFound
}

// Observed returns the relay list hbbs currently hands out.
func (s *Service) Observed(ctx context.Context) ([]string, error) {
	l, _ := s.local()
	if l == nil {
		return nil, errors.New("the hbbs admin channel is not configured")
	}
	return l.RelayServers(ctx)
}

// CreateInput creates a relay from the admin API.
type CreateInput struct {
	Kind       Kind
	Name       string
	PublicHost string
	MgmtHost   string
	Region     string
	Weight     int
	InRotation bool
	CreatedBy  *int64
}

// Enrollment is the one-time enrollment material of an agent relay.
type Enrollment struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Compose   string    `json:"compose"`
	PanelURL  string    `json:"panel_url"`
	// InstallCommand runs /agent/install.sh with the token (one-line install).
	InstallCommand string `json:"install_command"`
	// DockerRun is the same setup as a plain `docker run`.
	DockerRun string `json:"docker_run"`
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Options are the relay settings shown on the Relays tab.
type Options struct {
	RequireApproval   bool `json:"require_approval"`
	AllowPrivateHosts bool `json:"allow_private_hosts"`
}

// GetOptions reads the relay options (require_approval defaults to on).
func (s *Service) GetOptions(ctx context.Context) (Options, error) {
	o := Options{RequireApproval: true}
	if err := s.cfg.Panel.Settings.Get(ctx, SettingRequireApproval, &o.RequireApproval); err != nil && !errors.Is(err, store.ErrNotFound) {
		return o, err
	}
	if err := s.cfg.Panel.Settings.Get(ctx, SettingAllowPrivateHosts, &o.AllowPrivateHosts); err != nil && !errors.Is(err, store.ErrNotFound) {
		return o, err
	}
	return o, nil
}

// SetOptions stores the relay options.
func (s *Service) SetOptions(ctx context.Context, o Options, by *int64) error {
	if err := s.cfg.Panel.Settings.Set(ctx, SettingRequireApproval, o.RequireApproval, by); err != nil {
		return err
	}
	return s.cfg.Panel.Settings.Set(ctx, SettingAllowPrivateHosts, o.AllowPrivateHosts, by)
}

// Create adds a static or agent relay. Agents get their enrollment material.
func (s *Service) Create(ctx context.Context, in CreateInput) (View, *Enrollment, error) {
	n := NewRelay{Name: in.Name, Kind: in.Kind, PublicHost: in.PublicHost, MgmtHost: in.MgmtHost, Region: in.Region, Weight: in.Weight,
		InRotation: in.InRotation, CreatedBy: in.CreatedBy}
	var enr *Enrollment
	switch in.Kind {
	case KindStatic:
		if err := s.checkHost(ctx, in.PublicHost); err != nil {
			return View{}, nil, err
		}
		if in.MgmtHost != "" {
			if err := s.checkHost(ctx, in.MgmtHost); err != nil {
				return View{}, nil, err
			}
		}
	case KindAgent:
		opts, err := s.GetOptions(ctx)
		if err != nil {
			return View{}, nil, err
		}
		tok, err := newToken()
		if err != nil {
			return View{}, nil, err
		}
		n.TokenHash, n.TokenExpires, n.RequireApproval = TokenHash(tok), s.now().Add(s.cfg.EnrollTTL), opts.RequireApproval
		enr = s.enrollment(tok, n.TokenExpires)
	default:
		return View{}, nil, fmt.Errorf("%w: kind must be static or agent", store.ErrInvalid)
	}
	id, err := s.st.Create(ctx, n)
	if err != nil {
		return View{}, nil, err
	}
	s.Notify()
	v, err := s.Get(ctx, id)
	return v, enr, err
}

// checkHost applies the SSRF guard to a new or changed static relay host
// when it is an IP literal (names are checked at probe time).
func (s *Service) checkHost(ctx context.Context, h string) error {
	if err := ValidateHost(h); err != nil {
		return err
	}
	host, _, _ := SplitHost(h)
	if a, err := netip.ParseAddr(host); err == nil && IsPrivate(a) && !s.allowPrivate(ctx) {
		return fmt.Errorf("%w: %v", store.ErrInvalid, ErrPrivateHost)
	}
	return nil
}

func (s *Service) enrollment(tok string, exp time.Time) *Enrollment {
	url := strings.TrimRight(s.cfg.PublicURL, "/")
	if url == "" {
		url = "https://<panel public URL>"
	}
	compose := fmt.Sprintf(`services:
  relay:
    image: %s
    container_name: rdpanel-relay
    network_mode: host        # hbbr must see real client IPs
    restart: unless-stopped
    environment:
      RDPANEL_MODE: agent
      RDPANEL_AGENT_PANEL_URL: %s
      RDPANEL_AGENT_ENROLL_TOKEN: "%s"   # one-time, expires %s
    volumes:
      - ./relay-data:/data
    stop_grace_period: 20s
    ulimits:
      nofile: { soft: 1048576, hard: 1048576 }
`, s.cfg.Image, url, tok, exp.UTC().Format(time.RFC3339))
	return &Enrollment{Token: tok, ExpiresAt: exp.UTC(), Compose: compose, PanelURL: url,
		InstallCommand: installCommand(url, tok), DockerRun: dockerRunCommand(url, s.cfg.Image, tok)}
}

// Update changes relay fields.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (View, error) {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if p.PublicHost != nil && cur.Kind == KindStatic && *p.PublicHost != cur.PublicHost {
		if err := s.checkHost(ctx, *p.PublicHost); err != nil {
			return View{}, err
		}
	}
	if p.MgmtHost != nil && *p.MgmtHost != "" && cur.Kind == KindStatic && *p.MgmtHost != cur.MgmtHost {
		if err := s.checkHost(ctx, *p.MgmtHost); err != nil {
			return View{}, err
		}
	}
	if err := s.st.Update(ctx, id, p); err != nil {
		return View{}, err
	}
	if p.Name != nil && *p.Name != cur.Name {
		s.m.probes.DeletePartialMatch(prometheus.Labels{"relay": cur.Name})
		s.m.probe.DeletePartialMatch(prometheus.Labels{"relay": cur.Name})
	}
	if (p.PublicHost != nil && *p.PublicHost != cur.PublicHost) || (p.MgmtHost != nil && *p.MgmtHost != cur.MgmtHost) {
		s.mu.Lock()
		delete(s.probedAt, id) // probe the new host on the next pass
		s.fails[id] = 0
		s.mu.Unlock()
	}
	s.Notify()
	return s.Get(ctx, id)
}

// ErrLocalRelay refuses deleting the co-located relay.
var ErrLocalRelay = fmt.Errorf("%w: the local relay cannot be deleted; disable it or take it out of rotation instead", store.ErrInvalid)

// Delete removes a relay; a connected agent is disconnected.
func (s *Service) Delete(ctx context.Context, id int64) error {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Kind == KindLocal {
		return ErrLocalRelay
	}
	if err := s.st.Delete(ctx, id); err != nil {
		return err
	}
	s.disconnect(id, agentproto.CloseRevoked, "relay deleted")
	s.m.probes.DeletePartialMatch(prometheus.Labels{"relay": cur.Name})
	s.m.probe.DeletePartialMatch(prometheus.Labels{"relay": cur.Name})
	s.mu.Lock()
	delete(s.fails, id)
	delete(s.probedAt, id)
	delete(s.liveStats, id)
	delete(s.lastTraffic, id)
	s.mu.Unlock()
	s.Notify()
	return nil
}

// ErrNotAgent is returned for agent-only operations on other relay kinds.
var ErrNotAgent = fmt.Errorf("%w: not an agent relay", store.ErrInvalid)

// ResetEnrollment issues a new one-time token; the enrolled agent (if any)
// is disconnected and its key forgotten.
func (s *Service) ResetEnrollment(ctx context.Context, id int64) (*Enrollment, error) {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Kind != KindAgent {
		return nil, ErrNotAgent
	}
	opts, err := s.GetOptions(ctx)
	if err != nil {
		return nil, err
	}
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	exp := s.now().Add(s.cfg.EnrollTTL)
	if err := s.st.ResetEnrollment(ctx, id, TokenHash(tok), exp, opts.RequireApproval); err != nil {
		return nil, err
	}
	s.disconnect(id, agentproto.CloseRevoked, "enrollment reset")
	_ = s.st.SetHealthOnly(ctx, id, HealthPending)
	s.Notify()
	return s.enrollment(tok, exp), nil
}

// Approve lets an enrolled agent receive its running config.
func (s *Service) Approve(ctx context.Context, id int64, by *int64) (View, error) {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if cur.Kind != KindAgent {
		return View{}, ErrNotAgent
	}
	if !cur.Agent.Enrolled() {
		return View{}, fmt.Errorf("%w: the agent has not enrolled yet", store.ErrInvalid)
	}
	if err := s.st.Approve(ctx, id, by); err != nil {
		return View{}, err
	}
	s.Notify()
	return s.Get(ctx, id)
}

// Revoke disables an agent's key and disconnects it.
func (s *Service) Revoke(ctx context.Context, id int64) (View, error) {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if cur.Kind != KindAgent {
		return View{}, ErrNotAgent
	}
	if err := s.st.Revoke(ctx, id); err != nil {
		return View{}, err
	}
	s.disconnect(id, agentproto.CloseRevoked, "agent revoked")
	_ = s.st.SetHealthOnly(ctx, id, HealthPending)
	s.Notify()
	return s.Get(ctx, id)
}

// ProbeNow probes one relay immediately and re-evaluates its health.
func (s *Service) ProbeNow(ctx context.Context, id int64) (View, error) {
	r, err := s.st.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	if s.probeable(r) {
		s.mu.Lock()
		s.probedAt[id] = s.now()
		s.mu.Unlock()
		s.probeOne(ctx, r)
		if r, err = s.st.Get(ctx, id); err != nil {
			return View{}, err
		}
	}
	s.evaluate(ctx, &r)
	s.Notify()
	return s.Get(ctx, id)
}

// SettingsView is the per-relay settings state.
type SettingsView struct {
	Overrides reconcile.Settings `json:"overrides"`
	Global    reconcile.Settings `json:"global"`
	Effective reconcile.Settings `json:"effective"`
}

// Settings returns overrides, the global profile and the merge.
func (s *Service) Settings(ctx context.Context, id int64) (SettingsView, error) {
	if _, err := s.st.Get(ctx, id); err != nil {
		return SettingsView{}, err
	}
	var global reconcile.Settings
	if _, g := s.pusher(); g != nil {
		d, err := g.Desired(ctx)
		if err != nil {
			return SettingsView{}, err
		}
		global = reconcile.HBBROnly(d)
	}
	ov, err := s.st.Overrides(ctx, id)
	if err != nil {
		return SettingsView{}, err
	}
	return SettingsView{Overrides: ov, Global: global, Effective: Merge(global, ov)}, nil
}

// SaveSettings validates and stores the complete override set of an agent relay.
func (s *Service) SaveSettings(ctx context.Context, id int64, ov reconcile.Settings, by *int64) (SettingsView, error) {
	cur, err := s.st.Get(ctx, id)
	if err != nil {
		return SettingsView{}, err
	}
	if cur.Kind != KindAgent && HasOverrides(ov) {
		return SettingsView{}, fmt.Errorf("%w: per-relay settings apply to agent relays only (the local relay uses the global profile)", store.ErrInvalid)
	}
	norm, err := reconcile.Normalize(reconcile.HBBROnly(ov))
	if err != nil {
		return SettingsView{}, fmt.Errorf("%w: %v", store.ErrInvalid, strings.TrimPrefix(err.Error(), reconcile.ErrInvalid.Error()+": "))
	}
	if err := s.st.SaveOverrides(ctx, id, norm, by); err != nil {
		return SettingsView{}, err
	}
	s.Notify()
	return s.Settings(ctx, id)
}

// ErrNoUsage is returned when live usage is not available for a relay.
var ErrNoUsage = errors.New("live usage is only available for the local relay and connected agents")

// Usage returns hbbr's active relays (local admin channel or agent command).
func (s *Service) Usage(ctx context.Context, id int64) ([]control.RelayUsage, error) {
	r, err := s.st.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case KindLocal:
		l, _ := s.local()
		if l == nil {
			return nil, ErrNoUsage
		}
		return l.Usage(ctx)
	case KindAgent:
		s.mu.Lock()
		c := s.conns[id]
		s.mu.Unlock()
		if c == nil {
			return nil, ErrNoUsage
		}
		res, err := c.command(ctx, agentproto.CommandUsage)
		if err != nil {
			return nil, err
		}
		if !res.OK {
			return nil, errors.New(res.Error)
		}
		if res.Usage == nil {
			res.Usage = []control.RelayUsage{}
		}
		return res.Usage, nil
	}
	return nil, ErrNoUsage
}

// audit writes an admin_audit row for an agent action (actor "agent:<id>").
func (s *Service) audit(ctx context.Context, id int64, ip, action string, details map[string]any) {
	e := &store.AuditEntry{ActorName: fmt.Sprintf("agent:%d", id), ActorIP: ip, Action: action,
		TargetType: "relay", TargetID: fmt.Sprint(id), Details: details}
	if err := s.cfg.Panel.AdminAudit.Record(ctx, e); err != nil {
		s.log.Warn("relays: audit", slog.String("action", action), slog.Any("err", err))
	}
}

// HBBSKeyFingerprint is the fingerprint of the hbbs public key agents should
// run hbbr with ("" when unknown).
func (s *Service) HBBSKeyFingerprint() string {
	return agentproto.KeyFingerprint(s.publicKey())
}

// Traffic returns hourly traffic samples for relay id since `since`.
func (s *Service) Traffic(ctx context.Context, id int64, since time.Time) ([]TrafficSample, error) {
	if _, err := s.st.Get(ctx, id); err != nil {
		return nil, err
	}
	return s.st.TrafficSince(ctx, id, since)
}

func (s *Service) sampleTraffic(ctx context.Context, rs []Relay) {
	now := s.now()
	l, _ := s.local()

	type trafficDelta struct {
		id             int64
		bytesDelta     int64
		sessionsDelta  int
		activeSessions int
	}
	var deltas []trafficDelta

	// Fetch local usage or stats outside the mutex if needed
	var (
		localStats    *control.RelayStats
		localUsage    []control.RelayUsage
		hasLocalUsage bool
	)
	for _, r := range rs {
		if r.Kind == KindLocal && l != nil {
			sCtx, sCancel := context.WithTimeout(ctx, time.Second)
			st, err := l.Stats(sCtx)
			sCancel()
			if err == nil && st != nil {
				localStats = st
				hasLocalUsage = true
			} else {
				uCtx, uCancel := context.WithTimeout(ctx, time.Second)
				u, err := l.Usage(uCtx)
				uCancel()
				if err == nil {
					localUsage = u
					hasLocalUsage = true
				}
			}
			break
		}
	}

	s.mu.Lock()
	for _, r := range rs {
		prev := s.lastTraffic[r.ID]
		if prev.at.IsZero() {
			s.lastTraffic[r.ID] = trafficState{at: now}
			continue
		}
		elapsed := now.Sub(prev.at).Seconds()
		if elapsed < 5 {
			continue
		}

		var (
			bytesDelta     int64
			sessionsDelta  int
			activeSessions int
		)

		switch r.Kind {
		case KindAgent:
			st, ok := s.liveStats[r.ID]
			if !ok {
				continue
			}
			activeSessions = st.Usage.RelaysActive
			rateBps := st.Host.RxBps + st.Host.TxBps
			if rateBps <= 0 && st.Usage.CurrentKbps > 0 {
				rateBps = st.Usage.CurrentKbps * 1000
			}
			bytesDelta = int64(rateBps / 8 * elapsed)
			if st.Usage.TotalMB > prev.totalMB && prev.totalMB > 0 {
				mbDelta := int64((st.Usage.TotalMB - prev.totalMB) * 1024 * 1024)
				if mbDelta > bytesDelta {
					bytesDelta = mbDelta
				}
			}
			if activeSessions > prev.activeSessions {
				sessionsDelta = activeSessions - prev.activeSessions
			}
			s.lastTraffic[r.ID] = trafficState{
				at:             now,
				totalMB:        st.Usage.TotalMB,
				activeSessions: activeSessions,
			}

		case KindLocal:
			if !hasLocalUsage {
				continue
			}
			if localStats != nil {
				activeSessions = localStats.RelaysActive
				if prev.bytesTotal > 0 && localStats.BytesTotal >= prev.bytesTotal {
					bytesDelta = int64(localStats.BytesTotal - prev.bytesTotal)
				}
				if prev.relaysTotal > 0 && localStats.RelaysTotal >= prev.relaysTotal {
					sessionsDelta = int(localStats.RelaysTotal - prev.relaysTotal)
				}
				s.lastTraffic[r.ID] = trafficState{
					at:             now,
					bytesTotal:     localStats.BytesTotal,
					relaysTotal:    localStats.RelaysTotal,
					activeSessions: activeSessions,
				}
			} else {
				activeSessions = len(localUsage)
				var totalMB, sumKbps float64
				for _, u := range localUsage {
					totalMB += u.TotalMB
					sumKbps += float64(u.CurrentKbps)
				}
				rateBps := sumKbps * 1000
				bytesDelta = int64(rateBps / 8 * elapsed)
				if totalMB > prev.totalMB && prev.totalMB > 0 {
					mbDelta := int64((totalMB - prev.totalMB) * 1024 * 1024)
					if mbDelta > bytesDelta {
						bytesDelta = mbDelta
					}
				}
				if activeSessions > prev.activeSessions {
					sessionsDelta = activeSessions - prev.activeSessions
				}
				s.lastTraffic[r.ID] = trafficState{
					at:             now,
					totalMB:        totalMB,
					activeSessions: activeSessions,
				}
			}
		}

		if bytesDelta > 0 || sessionsDelta > 0 || activeSessions > 0 {
			deltas = append(deltas, trafficDelta{
				id:             r.ID,
				bytesDelta:     bytesDelta,
				sessionsDelta:  sessionsDelta,
				activeSessions: activeSessions,
			})
		}
	}
	purgeDue := now.Sub(s.lastPurge) >= time.Hour
	if purgeDue {
		s.lastPurge = now
	}
	s.mu.Unlock()

	for _, d := range deltas {
		_ = s.st.RecordTraffic(ctx, d.id, now, d.bytesDelta, d.sessionsDelta, d.activeSessions)
	}

	if purgeDue {
		_, _ = s.st.PurgeTrafficBefore(ctx, now.Add(-30*24*time.Hour))
	}
}

// ListPrefixRules returns all prefix routing rules ordered by id.
func (s *Service) ListPrefixRules(ctx context.Context) ([]PrefixRule, error) {
	return s.st.ListPrefixRules(ctx)
}

// CreatePrefixRule validates and inserts a new rule, syncs the map file, and reloads geo routing.
func (s *Service) CreatePrefixRule(ctx context.Context, rule *PrefixRule) error {
	if err := s.st.CreatePrefixRule(ctx, rule); err != nil {
		return err
	}
	if syncErr := s.SyncPrefixRules(ctx); syncErr != nil {
		s.log.Warn("relays: sync prefix rules after create", slog.Any("err", syncErr))
	}
	return nil
}

// DeletePrefixRule deletes a rule by ID, syncs the map file, and reloads geo routing.
func (s *Service) DeletePrefixRule(ctx context.Context, id int64) error {
	if err := s.st.DeletePrefixRule(ctx, id); err != nil {
		return err
	}
	if syncErr := s.SyncPrefixRules(ctx); syncErr != nil {
		s.log.Warn("relays: sync prefix rules after delete", slog.Any("err", syncErr))
	}
	return nil
}

// SyncPrefixRules writes relay_map.txt to ListDir and sends reload-geo to hbbs if reachable.
func (s *Service) SyncPrefixRules(ctx context.Context) error {
	rules, err := s.st.ListPrefixRules(ctx)
	if err != nil {
		return err
	}
	if s.cfg.ListDir != "" {
		content := FormatRelayMap(rules)
		if err := WriteRelayMapFile(s.cfg.ListDir, content); err != nil {
			return err
		}
	}
	ctl, up := s.local()
	if ctl != nil && (up == nil || up()) {
		if _, err := ctl.ReloadGeo(ctx); err != nil {
			return fmt.Errorf("reload geo: %w", err)
		}
	}
	return nil
}
