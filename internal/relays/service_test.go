package relays

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/db/dbtest"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/store"
)

type fakePusher struct {
	mu     sync.Mutex
	pushes int
	global reconcile.Settings
}

func (f *fakePusher) ReconcileTarget(context.Context, control.Target) error {
	f.mu.Lock()
	f.pushes++
	f.mu.Unlock()
	return nil
}

func (f *fakePusher) Desired(context.Context) (reconcile.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.global, nil
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pushes
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type harness struct {
	svc   *Service
	st    *Store
	panel *store.Store
	push  *fakePusher
	bus   *events.Bus
	clk   *clock
}

func newHarness(t *testing.T, mut func(*Config)) *harness {
	t.Helper()
	ctx := context.Background()
	d := dbtest.Open(t)
	clk := &clock{t: time.Now()}
	h := &harness{st: &Store{DB: d, Now: clk.now}, panel: store.New(d), push: &fakePusher{}, bus: events.NewBus(nil), clk: clk}
	cfg := Config{Store: h.st, Panel: h.panel, Bus: h.bus, Pusher: h.push, Global: h.push, Now: clk.now,
		PublicKey: func() (string, error) { return testKey, nil }, PublicURL: "https://desk-api.example.com",
		Prober: &Prober{Timeout: time.Second}}
	if mut != nil {
		mut(&cfg)
	}
	var err error
	if h.svc, err = New(cfg); err != nil {
		t.Fatal(err)
	}
	// Tests probe 127.0.0.1 fakes.
	if err := h.panel.Settings.Set(ctx, SettingAllowPrivateHosts, true, nil); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestStaticRelayLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	fr := hbbstest.NewFakeRelay(t, testKey)
	v, enr, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "partner", PublicHost: fr.Addr(), Weight: 2, InRotation: true})
	if err != nil || enr != nil || v.Health != HealthPending {
		t.Fatalf("create: %+v %v %v", v, enr, err)
	}
	if _, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "partner", PublicHost: "x.example.com", Weight: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "bad", PublicHost: "a b", Weight: 1}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad host: %v", err)
	}
	h.svc.Pass(ctx, true)
	v, _ = h.svc.Get(ctx, v.ID)
	if v.Health != HealthActive || v.RotationWeight != 2 || v.LastProbeMs == nil {
		t.Fatalf("after probe: %+v", v)
	}
	if got := h.svc.RelayServers(ctx); strings.Join(got, ",") != fr.Addr()+","+fr.Addr() {
		t.Fatalf("rotation %v", got)
	}
	if h.push.count() != 1 {
		t.Fatalf("pushes %d", h.push.count())
	}
	// Unchanged rotation: no second push.
	h.svc.Pass(ctx, true)
	if h.push.count() != 1 {
		t.Fatalf("pushes after unchanged pass %d", h.push.count())
	}

	// The relay starts failing: degraded after 1-2 failures (still in rs),
	// offline after 3 and dropped from the rotation.
	fr.Close()
	sub := h.bus.Subscribe(func(e events.Event) bool { return e.Type == events.RelayHealth }, 16)
	defer sub.Close()
	h.svc.Pass(ctx, true)
	if v, _ = h.svc.Get(ctx, v.ID); v.Health != HealthDegraded || v.RotationWeight != 2 {
		t.Fatalf("after 1 failure: %s weight %d", v.Health, v.RotationWeight)
	}
	h.svc.Pass(ctx, true)
	h.svc.Pass(ctx, true)
	if v, _ = h.svc.Get(ctx, v.ID); v.Health != HealthOffline || v.RotationWeight != 0 || v.LastProbeError == "" {
		t.Fatalf("after 3 failures: %+v", v)
	}
	if got := h.svc.RelayServers(ctx); got != nil {
		t.Fatalf("offline relay still in rotation: %v", got)
	}
	var seen []string
	for {
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		e, err := sub.Next(cctx)
		cancel()
		if err != nil {
			break
		}
		seen = append(seen, e.Data.(events.RelayState).Health)
	}
	if strings.Join(seen, ",") != "degraded,offline" {
		t.Fatalf("events %v", seen)
	}

	// Drain and delete.
	st := StateDraining
	if v, err = h.svc.Update(ctx, v.ID, Patch{AdminState: &st}); err != nil || v.AdminState != StateDraining {
		t.Fatalf("drain: %+v %v", v, err)
	}
	if err := h.svc.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Get(ctx, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted relay: %v", err)
	}
}

func TestSSRFGuardOnCreate(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.panel.Settings.Set(ctx, SettingAllowPrivateHosts, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "lan", PublicHost: "10.0.0.5:21117", Weight: 1}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("private static relay accepted: %v", err)
	}
	// Names are allowed at creation; the prober refuses private answers.
	if _, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "named", PublicHost: "relay.example.com", Weight: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureLocal(t *testing.T) {
	ctx := context.Background()
	up := true
	h := newHarness(t, func(c *Config) {
		c.LocalHost = "rd.example.com"
		c.LocalUp = func() bool { return up }
		c.Prober = &Prober{Timeout: time.Second, Lookup: func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errors.New("no such host")
		}}
	})
	// A static relay with the local host (from the 00010 data migration)
	// becomes the local one.
	if _, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "rd.example.com", PublicHost: "rd.example.com", Weight: 1, InRotation: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.EnsureLocal(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	views, _, _ := h.svc.List(ctx)
	if len(views) != 1 || views[0].Kind != KindLocal {
		t.Fatalf("relays %+v", views)
	}
	if err := h.svc.Delete(ctx, views[0].ID); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("local relay deleted: %v", err)
	}
	// A public name is probed; here it does not resolve, but the local relay
	// stays in rotation (degraded) while its admin channel answers.
	h.svc.Pass(ctx, true)
	h.svc.Pass(ctx, true)
	h.svc.Pass(ctx, true)
	v, _ := h.svc.Get(ctx, views[0].ID)
	if v.Health != HealthDegraded || v.RotationWeight != 1 {
		t.Fatalf("local relay: %s weight %d (%s)", v.Health, v.RotationWeight, v.LastProbeError)
	}
	up = false
	h.svc.Pass(ctx, true)
	if v, _ = h.svc.Get(ctx, views[0].ID); v.Health != HealthOffline {
		t.Fatalf("local relay with hbbr down: %s", v.Health)
	}
	// Never an empty list: the local relay is the fallback.
	if got := h.svc.RelayServers(ctx); len(got) != 1 || got[0] != "rd.example.com" {
		t.Fatalf("fallback %v", got)
	}
}

func newAgentKey(t *testing.T) ed25519.PrivateKey {
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestEnrollmentTokenLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	v, enr, err := h.svc.Create(ctx, CreateInput{Kind: KindAgent, Name: "fra-1", PublicHost: "fra-1.example.com", Weight: 1, InRotation: true})
	if err != nil || enr == nil || enr.Token == "" {
		t.Fatalf("create agent: %v %+v", err, enr)
	}
	if !strings.Contains(enr.Compose, enr.Token) || !strings.Contains(enr.Compose, "RDPANEL_MODE: agent") ||
		!strings.Contains(enr.Compose, "https://desk-api.example.com") {
		t.Fatalf("compose:\n%s", enr.Compose)
	}
	if want := "curl -fsSL https://desk-api.example.com/agent/install.sh | sudo sh -s -- --token " + enr.Token; enr.InstallCommand != want {
		t.Fatalf("install command = %q, want %q", enr.InstallCommand, want)
	}
	if !strings.Contains(enr.DockerRun, "-e RDPANEL_AGENT_ENROLL_TOKEN="+enr.Token+" ") || !strings.Contains(enr.DockerRun, "--network host") ||
		!strings.HasSuffix(enr.DockerRun, " ghcr.io/bropines/rdpanel:latest") {
		t.Fatalf("docker run:\n%s", enr.DockerRun)
	}
	if !v.Agent.TokenPending || !v.Agent.RequireApproval || v.Agent.Enrolled() {
		t.Fatalf("agent row: %+v", v.Agent)
	}
	// Only the hash is stored.
	var n int
	_ = h.st.DB.QueryRow(`SELECT count(*) FROM relay_agents WHERE instr(CAST(enroll_token_hash AS TEXT), ?) > 0`, enr.Token).Scan(&n)
	if n != 0 {
		t.Fatal("token stored in clear")
	}
	key := newAgentKey(t)
	pub := key.Public().(ed25519.PublicKey)
	if _, err := h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash("wrong"), Pubkey: pub}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong token: %v", err)
	}
	r, err := h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash(enr.Token), Pubkey: pub, IP: "198.51.100.7", Hostname: "fra-1"})
	if err != nil || !r.Agent.Enrolled() || r.Agent.Approved() || r.Agent.TokenPending || r.Agent.EnrolledFromIP != "198.51.100.7" {
		t.Fatalf("enroll: %+v %v", r.Agent, err)
	}
	// Single use.
	if _, err := h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash(enr.Token), Pubkey: newAgentKey(t).Public().(ed25519.PublicKey)}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("token reuse: %v", err)
	}
	if _, err := h.svc.Approve(ctx, v.ID, nil); err != nil {
		t.Fatal(err)
	}
	if r, _ = h.st.Get(ctx, v.ID); !r.Agent.Approved() {
		t.Fatal("not approved")
	}
	// Reset: new token, old key forgotten, approval required again.
	enr2, err := h.svc.ResetEnrollment(ctx, v.ID)
	if err != nil || enr2.Token == enr.Token {
		t.Fatalf("reset: %v", err)
	}
	if r, _ = h.st.Get(ctx, v.ID); r.Agent.Enrolled() || r.Agent.Approved() || !r.Agent.TokenPending {
		t.Fatalf("after reset: %+v", r.Agent)
	}
	// Expiry (TTL 1 h).
	h.clk.add(2 * time.Hour)
	if _, err := h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash(enr2.Token), Pubkey: pub}); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired token: %v", err)
	}
	// Without the approval gate an enrolled agent is approved right away.
	if err := h.svc.SetOptions(ctx, Options{RequireApproval: false, AllowPrivateHosts: true}, nil); err != nil {
		t.Fatal(err)
	}
	enr3, _ := h.svc.ResetEnrollment(ctx, v.ID)
	if r, err = h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash(enr3.Token), Pubkey: pub}); err != nil || !r.Agent.Approved() {
		t.Fatalf("no approval gate: %+v %v", r.Agent, err)
	}
	if _, err := h.svc.Revoke(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ = h.st.Get(ctx, v.ID); r.Agent.Approved() || r.Agent.RevokedAt == nil {
		t.Fatalf("revoked: %+v", r.Agent)
	}
}

func TestOverridesAndConfigVersions(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ten, five := 10.0, 5.0
	h.push.global = reconcile.Settings{TotalBandwidthMbps: &ten, Blocklist: []string{"203.0.113.1"}, MustLogin: new(bool)}
	v, enr, _ := h.svc.Create(ctx, CreateInput{Kind: KindAgent, Name: "a1", PublicHost: "a1.example.com", Weight: 1, InRotation: true})
	r, err := h.st.Enroll(ctx, EnrollInput{TokenHash: TokenHash(enr.Token), Pubkey: newAgentKey(t).Public().(ed25519.PublicKey)})
	if err != nil {
		t.Fatal(err)
	}
	c1, err := h.svc.configFor(ctx, r)
	if err != nil || c1.Version != 1 || c1.Run || c1.HBBSPublicKey != testKey || *c1.Settings.TotalBandwidthMbps != 10 {
		t.Fatalf("config 1: %+v %v", c1, err)
	}
	r, _ = h.st.Get(ctx, v.ID)
	if c, _ := h.svc.configFor(ctx, r); c.Version != 1 {
		t.Fatalf("unchanged config bumped to %d", c.Version)
	}
	if _, err := h.svc.Approve(ctx, v.ID, nil); err != nil {
		t.Fatal(err)
	}
	r, _ = h.st.Get(ctx, v.ID)
	c2, _ := h.svc.configFor(ctx, r)
	if c2.Version != 2 || !c2.Run {
		t.Fatalf("approved config: %+v", c2)
	}
	sv, err := h.svc.SaveSettings(ctx, v.ID, reconcile.Settings{TotalBandwidthMbps: &five, MustLogin: new(bool)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *sv.Effective.TotalBandwidthMbps != 5 || sv.Effective.Blocklist[0] != "203.0.113.1" || sv.Effective.MustLogin != nil {
		t.Fatalf("effective %+v", sv.Effective)
	}
	r, _ = h.st.Get(ctx, v.ID)
	if c3, _ := h.svc.configFor(ctx, r); c3.Version != 3 || *c3.Settings.TotalBandwidthMbps != 5 {
		t.Fatalf("override config: %+v", c3)
	}
	if _, err := h.svc.SaveSettings(ctx, v.ID, reconcile.Settings{LimitSpeedMbps: new(float64)}, nil); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("invalid override accepted: %v", err)
	}
	st := StateDisabled
	_, _ = h.svc.Update(ctx, v.ID, Patch{AdminState: &st})
	r, _ = h.st.Get(ctx, v.ID)
	if c4, _ := h.svc.configFor(ctx, r); c4.Run || c4.Version != 4 {
		t.Fatalf("disabled relay config: %+v", c4)
	}
}

func TestSeparateMgmtHost(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_ = h.svc.SetOptions(ctx, Options{AllowPrivateHosts: true}, nil)
	fr := hbbstest.NewFakeRelay(t, testKey)
	defer fr.Close()

	// Public host is unresolvable or external, mgmt_host points to probeable relay.
	v, _, err := h.svc.Create(ctx, CreateInput{
		Kind:       KindStatic,
		Name:       "split-relay",
		PublicHost: "desk-nl.example.com:21117",
		MgmtHost:   fr.Addr(),
		Weight:     1,
		InRotation: true,
	})
	if err != nil {
		t.Fatalf("create split relay: %v", err)
	}
	if v.MgmtHost != fr.Addr() || v.PublicHost != "desk-nl.example.com:21117" {
		t.Fatalf("unexpected fields: %+v", v)
	}

	// Probe: should succeed via MgmtHost.
	h.svc.Pass(ctx, true)
	v, err = h.svc.Get(ctx, v.ID)
	if err != nil || v.Health != HealthActive {
		t.Fatalf("expected healthy relay via mgmt_host, got: %+v (err: %v)", v, err)
	}
	// Public host was unreachable, so PublicReachable should be false.
	if v.PublicReachable == nil || *v.PublicReachable != false {
		t.Fatalf("expected PublicReachable=false, got: %v", v.PublicReachable)
	}

	// Now start a listener on public_host to make it reachable.
	pubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pubLn.Close()
	pubAddr := pubLn.Addr().String()
	v, err = h.svc.Update(ctx, v.ID, Patch{PublicHost: &pubAddr})
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Pass(ctx, true)
	v, err = h.svc.Get(ctx, v.ID)
	if err != nil || v.PublicReachable == nil || *v.PublicReachable != true {
		t.Fatalf("expected PublicReachable=true after listener started, got: %v", v.PublicReachable)
	}

	// Rotation: clients receive PublicHost, NOT MgmtHost.
	servers := h.svc.RelayServers(ctx)
	if len(servers) != 1 || servers[0] != pubAddr {
		t.Fatalf("expected public_host in rotation, got: %v", servers)
	}

	// Patch mgmt_host to empty (clearing it): defaults back to PublicHost.
	empty := ""
	v, err = h.svc.Update(ctx, v.ID, Patch{MgmtHost: &empty})
	if err != nil {
		t.Fatalf("patch clear mgmt_host: %v", err)
	}
	if v.MgmtHost != "" {
		t.Fatalf("expected empty mgmt_host, got %q", v.MgmtHost)
	}
}

func TestRelayTrafficRecordingAndQuery(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	v, _, err := h.svc.Create(ctx, CreateInput{Name: "relay-traffic-test", Kind: KindStatic, PublicHost: "127.0.0.1:21117"})
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 10, 18, 12, 15, 0, 0, time.UTC)
	// Record two samples in the same hour: should aggregate
	if err := h.st.RecordTraffic(ctx, v.ID, t0, 1000, 2, 5); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RecordTraffic(ctx, v.ID, t0.Add(10*time.Minute), 2000, 3, 8); err != nil {
		t.Fatal(err)
	}

	// Record sample in next hour
	t1 := t0.Add(time.Hour)
	if err := h.st.RecordTraffic(ctx, v.ID, t1, 5000, 1, 2); err != nil {
		t.Fatal(err)
	}

	// Query via Service
	samples, err := h.svc.Traffic(ctx, v.ID, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 hourly samples, got %d", len(samples))
	}
	// Check hour 0: 3000 bytes, 5 sessions, max 8 active
	if samples[0].Bytes != 3000 || samples[0].Sessions != 5 || samples[0].MaxActive != 8 {
		t.Errorf("sample[0] = %+v, want bytes=3000 sessions=5 max=8", samples[0])
	}
	// Check hour 1: 5000 bytes, 1 session, max 2 active
	if samples[1].Bytes != 5000 || samples[1].Sessions != 1 || samples[1].MaxActive != 2 {
		t.Errorf("sample[1] = %+v, want bytes=5000 sessions=1 max=2", samples[1])
	}

	// Purge before t1
	n, err := h.st.PurgeTrafficBefore(ctx, t1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d rows, want 1", n)
	}
	remaining, err := h.svc.Traffic(ctx, v.ID, t0.Add(-time.Hour))
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining = %v, err = %v", remaining, err)
	}
}

type fakeLocalControl struct {
	usage []control.RelayUsage
	stats *control.RelayStats
}

func (f *fakeLocalControl) RelayServers(context.Context) ([]string, error) {
	return []string{"rd.example.com"}, nil
}

func (f *fakeLocalControl) Usage(context.Context) ([]control.RelayUsage, error) {
	return f.usage, nil
}

func (f *fakeLocalControl) Stats(context.Context) (*control.RelayStats, error) {
	return f.stats, nil
}

func (f *fakeLocalControl) ReloadGeo(context.Context) (string, error) {
	return "ok", nil
}

func TestLocalRelaySampleTrafficNoDeadlock(t *testing.T) {
	ctx := context.Background()
	fakeLocal := &fakeLocalControl{
		usage: []control.RelayUsage{
			{Addr: "1.2.3.4:1234", TotalMB: 10, CurrentKbps: 500},
		},
	}
	h := newHarness(t, func(c *Config) {
		c.LocalHost = "rd.example.com"
		c.Local = fakeLocal
		c.LocalUp = func() bool { return true }
	})
	if err := h.svc.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}

	// First pass initialises trafficState
	h.svc.Pass(ctx, false)

	// Advance time by 10s
	h.clk.add(10 * time.Second)
	fakeLocal.usage[0].TotalMB = 12

	// Second pass calculates delta and records traffic without deadlocking
	done := make(chan struct{})
	go func() {
		h.svc.Pass(ctx, false)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Pass deadlocked on local relay traffic sampling")
	}

	views, _, err := h.svc.List(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("List returned err=%v views=%v", err, views)
	}
	samples, err := h.svc.Traffic(ctx, views[0].ID, h.clk.now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) == 0 || samples[0].Bytes == 0 {
		t.Fatalf("expected recorded traffic, got %+v", samples)
	}
}

// TestAutoWeightsKeepConfiguredWeight: with auto weights on, the API's weight
// stays the configured one (the drawer saves it back); only rotation_weight
// is load-balanced.
func TestAutoWeightsKeepConfiguredWeight(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, nil)
	if err := h.svc.SetOptions(ctx, Options{AutoWeights: true, AllowPrivateHosts: true}, nil); err != nil {
		t.Fatal(err)
	}
	idle, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "idle", PublicHost: "idle.example.com", Weight: 2, InRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	busy, _, err := h.svc.Create(ctx, CreateInput{Kind: KindStatic, Name: "busy", PublicHost: "busy.example.com", Weight: 3, InRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{idle.ID, busy.ID} {
		if err := h.st.SetHealthOnly(ctx, id, HealthActive); err != nil {
			t.Fatal(err)
		}
	}
	h.svc.mu.Lock()
	h.svc.lastTraffic[idle.ID] = trafficState{at: h.clk.now(), activeSessions: 0}
	h.svc.lastTraffic[busy.ID] = trafficState{at: h.clk.now(), activeSessions: 10}
	h.svc.mu.Unlock()

	check := func(what string, wantRot map[int64]int) {
		t.Helper()
		views, _, err := h.svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range views {
			want := map[int64]int{idle.ID: 2, busy.ID: 3}[v.ID]
			if v.Weight != want {
				t.Errorf("%s: List %s weight %d, want the configured %d", what, v.Name, v.Weight, want)
			}
			if v.RotationWeight != wantRot[v.ID] {
				t.Errorf("%s: List %s rotation_weight %d, want %d", what, v.Name, v.RotationWeight, wantRot[v.ID])
			}
			g, err := h.svc.Get(ctx, v.ID)
			if err != nil || g.Weight != want || g.RotationWeight != wantRot[v.ID] {
				t.Errorf("%s: Get %s = weight %d rotation %d (%v)", what, v.Name, g.Weight, g.RotationWeight, err)
			}
		}
	}
	check("loaded", map[int64]int{idle.ID: 5, busy.ID: 1})
	// The load samples age out: back to the configured weights.
	h.clk.add(liveStatsMaxAge + time.Second)
	check("stale samples", map[int64]int{idle.ID: 2, busy.ID: 3})
}
