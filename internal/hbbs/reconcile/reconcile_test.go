package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/events"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
	"github.com/bropines/rustdesk-panel/internal/hbbs/hbbstest"
)

type rig struct {
	r      *Reconciler
	hs, hr *hbbstest.FakeAdmin
	store  *Store
	bus    *events.Bus
	sub    *events.Subscription
	dir    string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(dir, "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hs, hr := hbbstest.NewFakeAdmin(t), hbbstest.NewFakeAdmin(t)
	ctl, err := control.New(control.Config{HBBSAddr: hs.Addr(), HBBRAddr: hr.Addr(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(nil)
	st := &Store{DB: d}
	listDir := filepath.Join(dir, "hbbr")
	_ = os.Mkdir(listDir, 0o755)
	r, err := New(Config{Control: ctl, Store: st, Bus: bus, ListDir: listDir, Registerer: prometheus.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	sub := bus.Subscribe(nil, 64)
	t.Cleanup(sub.Close)
	return &rig{r: r, hs: hs, hr: hr, store: st, bus: bus, sub: sub, dir: listDir}
}

func bp(b bool) *bool                   { return &b }
func fp(f float64) *float64             { return &f }
func ip64(i int64) *int64               { return &i }
func cmds(f *hbbstest.FakeAdmin) string { return strings.Join(f.Commands(), "\n") }

func fullSettings() Settings {
	return Settings{
		RelayServers:            []string{"r1.example.com:21117", "r2.example.com"},
		AlwaysUseRelay:          bp(true),
		MustLogin:               bp(true),
		TCPKeepalive:            bp(true),
		Blacklist:               []string{"1.1.1.1", "::ffff:2.2.2.2"},
		Blocklist:               []string{},
		LimitSpeedMbps:          fp(8),
		TotalBandwidthMbps:      fp(512.5),
		SingleBandwidthMbps:     fp(0.1), // not exactly representable after hbbr's integer rounding
		DowngradeThreshold:      fp(0.666),
		DowngradeStartCheckSecs: ip64(600),
	}
}

func checkApplied(t *testing.T, g *rig) {
	t.Helper()
	relays, aur, ml, _, _, _, _, _ := g.hs.State()
	if strings.Join(relays, ",") != "r1.example.com:21117,r2.example.com" || !aur || !ml || !g.hs.TCPKeepalive() {
		t.Fatalf("hbbs state relays=%v aur=%v ml=%v tk=%v", relays, aur, ml, g.hs.TCPKeepalive())
	}
	_, _, _, black, block, rates, dt, tt := g.hr.State()
	if strings.Join(black, ",") != "1.1.1.1,2.2.2.2" || len(block) != 0 {
		t.Fatalf("hbbr lists %v %v", black, block)
	}
	if rates["limit-speed"] != 8 || rates["total-bandwidth"] != 512.5 || rates["single-bandwidth"] != 0.1 || dt != 0.66 || tt != 600 {
		t.Fatalf("hbbr values %v dt=%v t=%v", rates, dt, tt)
	}
}

func TestApplyStoresAndPushes(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if err := g.r.Apply(ctx, fullSettings(), nil); err != nil {
		t.Fatal(err)
	}
	checkApplied(t, g)

	// aur is always followed by re-sending the relay list.
	c := cmds(g.hs)
	i := strings.Index(c, "always-use-relay Y")
	if i < 0 || !strings.Contains(c[i:], "relay-servers r1.example.com:21117,r2.example.com") {
		t.Fatalf("aur not followed by rs:\n%s", c)
	}

	// Desired state round-trips through the DB, normalised.
	got, err := g.r.Desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Blacklist, ",") != "1.1.1.1,2.2.2.2" || got.Blocklist == nil || len(got.Blocklist) != 0 ||
		*got.DowngradeStartCheckSecs != 600 || !*got.MustLogin {
		t.Fatalf("desired %+v", got)
	}
	ks, err := g.r.KeyStatus(ctx)
	if err != nil || len(ks) != 11 {
		t.Fatalf("key status %v %v", ks, err)
	}
	for _, k := range ks {
		if k.AppliedAt == nil || k.LastError != "" {
			t.Fatalf("not applied: %+v", k)
		}
	}

	// Files for the next hbbr start.
	b, err := os.ReadFile(filepath.Join(g.dir, BlacklistFile))
	if err != nil || string(b) != "1.1.1.1\n2.2.2.2" {
		t.Fatalf("blacklist.txt %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(g.dir, BlocklistFile)); err != nil || len(b) != 0 {
		t.Fatalf("blocklist.txt %q %v", b, err)
	}

	// Current reads everything back.
	obs, err := g.r.Current(ctx)
	if err != nil || obs.Errors != nil {
		t.Fatalf("current %+v %v", obs, err)
	}
	if !*obs.Settings.AlwaysUseRelay || !*obs.Settings.TCPKeepalive || *obs.Settings.DowngradeThreshold != 0.66 || len(obs.Settings.RelayServers) != 2 {
		t.Fatalf("observed %+v", obs.Settings)
	}

	// Unmanaging a key deletes its row and leaves the server value alone.
	s := fullSettings()
	s.MustLogin = nil
	s.Blacklist = nil
	if err := g.r.Apply(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = g.r.Desired(ctx)
	if got.MustLogin != nil || got.Blacklist != nil {
		t.Fatal("unmanaged keys must be removed")
	}
	if _, _, ml, _, _, _, _, _ := g.hs.State(); !ml {
		t.Fatal("unmanaged value must not be touched")
	}
}

func TestValidation(t *testing.T) {
	g := newRig(t)
	for _, s := range []Settings{
		{RelayServers: []string{}},
		{RelayServers: []string{"bad host"}},
		{Blacklist: []string{"10.0.0.0/8"}},
		{LimitSpeedMbps: fp(0)},
		{TotalBandwidthMbps: fp(-3)},
		{DowngradeThreshold: fp(0.001)},
		{DowngradeStartCheckSecs: ip64(0)},
	} {
		if err := g.r.Apply(context.Background(), s, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", s, err)
		}
	}
	if len(g.hs.Commands())+len(g.hr.Commands()) != 0 {
		t.Fatal("invalid settings must not be pushed")
	}
}

func TestRestartDetectionReapplies(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if err := g.store.Save(ctx, mustNormalize(t, fullSettings()), nil); err != nil {
		t.Fatal(err)
	}
	// Startup: first sighting of both channels -> full apply.
	g.r.Tick(ctx)
	checkApplied(t, g)
	st := g.r.Status()
	if !st.HBBS.Reachable || !st.HBBR.Reachable || st.HBBS.Applies != 1 || st.HBBR.Applies != 1 {
		t.Fatalf("status %+v", st)
	}
	evs, _ := g.sub.Drain(nil)
	if len(evs) != 2 || evs[0].Data.(events.Health).Component != "hbbs-admin" || !evs[0].Audience.AdminOnly {
		t.Fatalf("health events %+v", evs)
	}

	// Steady state: read-backs only, no setters.
	g.hs.ResetCommands()
	g.hr.ResetCommands()
	g.r.Tick(ctx)
	for _, c := range append(g.hs.Commands(), g.hr.Commands()...) {
		if strings.Contains(c, " ") && !strings.HasPrefix(c, "blacklist") && !strings.HasPrefix(c, "blocklist") {
			t.Fatalf("unexpected setter in steady state: %q", c)
		}
	}

	// hbbs goes down (answers nothing) and comes back with CLI defaults.
	g.hs.Drop.Store(true)
	g.r.Tick(ctx)
	if g.r.Status().HBBS.Reachable {
		t.Fatal("hbbs should be unreachable")
	}
	evs, _ = g.sub.Drain(nil)
	if len(evs) != 1 || evs[0].Data.(events.Health).Up {
		t.Fatalf("down event %+v", evs)
	}
	g.hs.Restart()
	g.hs.Drop.Store(false)
	g.r.Tick(ctx)
	checkApplied(t, g)
	if st := g.r.Status(); st.HBBS.Applies != 2 || !st.HBBS.Reachable {
		t.Fatalf("status after restart %+v", st)
	}
}

func TestDriftCorrection(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if err := g.r.Apply(ctx, fullSettings(), nil); err != nil {
		t.Fatal(err)
	}
	g.r.Tick(ctx) // first sighting (force)
	// hbbr restarted between two probes: never observed down, values reset.
	g.hr.Restart()
	g.hs.Restart()
	g.r.Tick(ctx)
	checkApplied(t, g)
	if st := g.r.Status(); st.HBBR.Drifts == 0 || st.HBBS.Drifts == 0 {
		t.Fatalf("drifts not counted %+v", st)
	}
}

func TestLegacyAURBugRelayListPreserved(t *testing.T) {
	g := newRig(t)
	g.hs.SetLegacyAURBug(true)
	ctx := context.Background()
	// Relay list unmanaged: the reconciler re-sends what hbbs had before "aur".
	if err := g.r.Apply(ctx, Settings{AlwaysUseRelay: bp(true)}, nil); err != nil {
		t.Fatal(err)
	}
	relays, aur, _, _, _, _, _, _ := g.hs.State()
	if !aur || strings.Join(relays, ",") != "relay.example.com" {
		t.Fatalf("relays wiped: %v aur=%v", relays, aur)
	}
	// Managed list.
	if err := g.r.Apply(ctx, Settings{AlwaysUseRelay: bp(false), RelayServers: []string{"x.example.com"}}, nil); err != nil {
		t.Fatal(err)
	}
	relays, aur, _, _, _, _, _, _ = g.hs.State()
	if aur || strings.Join(relays, ",") != "x.example.com" {
		t.Fatalf("relays %v aur=%v", relays, aur)
	}
}

func TestApplyWhileUnreachableIsSavedAndRetried(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	g.hr.Drop.Store(true)
	err := g.r.Apply(ctx, Settings{MustLogin: bp(true), LimitSpeedMbps: fp(4)}, nil)
	if err == nil || !strings.Contains(err.Error(), "hbbr") {
		t.Fatalf("expected hbbr error, got %v", err)
	}
	ks, _ := g.r.KeyStatus(ctx)
	var ls KeyStatus
	for _, k := range ks {
		if k.Key == KeyLimitSpeed {
			ls = k
		}
	}
	if ls.AppliedAt != nil || ls.LastError == "" {
		t.Fatalf("limit_speed status %+v", ls)
	}
	if _, _, ml, _, _, _, _, _ := g.hs.State(); !ml {
		t.Fatal("hbbs part must still be applied")
	}
	g.r.Tick(ctx) // hbbr down
	g.hr.Drop.Store(false)
	g.r.Tick(ctx) // hbbr up -> apply
	if _, _, _, _, _, rates, _, _ := g.hr.State(); rates["limit-speed"] != 4 {
		t.Fatalf("not retried: %v", rates)
	}
	ks, _ = g.r.KeyStatus(ctx)
	for _, k := range ks {
		if k.AppliedAt == nil || k.LastError != "" {
			t.Fatalf("after retry %+v", k)
		}
	}
}

func TestTCPKeepalive(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	if err := g.r.Apply(ctx, Settings{TCPKeepalive: bp(true)}, nil); err != nil {
		t.Fatal(err)
	}
	if !g.hs.TCPKeepalive() || !strings.Contains(cmds(g.hs), "tcp-keepalive Y") {
		t.Fatalf("not applied:\n%s", cmds(g.hs))
	}
	desired, _ := g.r.Desired(ctx)
	obs, err := g.r.Current(ctx)
	if err != nil || obs.Settings.TCPKeepalive == nil || !*obs.Settings.TCPKeepalive {
		t.Fatalf("current %+v %v", obs, err)
	}
	if m, ok := KeyInSync(control.HBBS, KeyTCPKeepalive, desired, obs.Settings); !m || !ok {
		t.Fatalf("in sync: managed=%v in_sync=%v", m, ok)
	}

	// hbbs restarted with TCP_KEEPALIVE=N between probes: drift corrected.
	g.r.Tick(ctx)
	g.hs.Restart()
	if obs, _ := g.r.Current(ctx); *obs.Settings.TCPKeepalive {
		t.Fatal("fake restart must reset the flag")
	} else if _, ok := KeyInSync(control.HBBS, KeyTCPKeepalive, desired, obs.Settings); ok {
		t.Fatal("drift must be reported")
	}
	g.r.Tick(ctx)
	if !g.hs.TCPKeepalive() || g.r.Status().HBBS.Drifts == 0 {
		t.Fatalf("drift not corrected: tk=%v %+v", g.hs.TCPKeepalive(), g.r.Status())
	}

	// Unmanaged: row removed, server value left alone.
	if err := g.r.Apply(ctx, Settings{}, nil); err != nil {
		t.Fatal(err)
	}
	if d, _ := g.r.Desired(ctx); d.TCPKeepalive != nil || !g.hs.TCPKeepalive() {
		t.Fatal("unmanaged tcp_keepalive must not be touched")
	}

	// An hbbs without the command (upstream) answers with nothing: observed
	// is nil, the rest of the hbbs read-back still works, and applying it
	// reports a read-back error.
	g.hs.SetOverride(func(cmd string) (string, bool) { return "", strings.HasPrefix(cmd, "tcp-keepalive") })
	obs, err = g.r.Current(ctx)
	if err != nil || obs.Errors != nil || obs.Settings.TCPKeepalive != nil || obs.Settings.MustLogin == nil {
		t.Fatalf("legacy hbbs current %+v %v", obs, err)
	}
	if err := g.r.Apply(ctx, Settings{TCPKeepalive: bp(false)}, nil); err == nil || !strings.Contains(err.Error(), "tcp_keepalive") {
		t.Fatalf("legacy hbbs apply: %v", err)
	}
	if m, ok := KeyInSync(control.HBBS, KeyTCPKeepalive, Settings{TCPKeepalive: bp(false)}, obs.Settings); !m || ok {
		t.Fatal("managed key with no observed value is not in sync")
	}
}

func TestRelayDrift(t *testing.T) {
	d := []string{"a", "b"}
	for _, tc := range []struct {
		cur  []string
		want bool
	}{
		{nil, true},
		{[]string{"a"}, false}, // hbbs dropped an unresolvable entry: not fixable
		{[]string{"a", "b"}, false},
		{[]string{"a", "c"}, true},
	} {
		if relayDrift(tc.cur, d) != tc.want {
			t.Errorf("%v", tc.cur)
		}
	}
}

func TestSettingsJSON(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"blacklist":[],"blocklist":null,"must_login":false,"tcp_keepalive":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Blacklist == nil || s.Blocklist != nil || s.MustLogin == nil || *s.MustLogin || s.TCPKeepalive == nil || !*s.TCPKeepalive {
		t.Fatalf("%+v", s)
	}
	b, _ := json.Marshal(Settings{Blacklist: []string{}})
	if !strings.Contains(string(b), `"blacklist":[]`) || !strings.Contains(string(b), `"relay_servers":null`) {
		t.Fatal(string(b))
	}
}

func TestWriteListFileAtomicAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, BlacklistFile)
	if err := writeListFile(p, []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	st1, _ := os.Stat(p)
	time.Sleep(20 * time.Millisecond)
	if err := writeListFile(p, []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(p)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("unchanged content must not be rewritten")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func mustNormalize(t *testing.T, s Settings) Settings {
	t.Helper()
	n, err := Normalize(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunStops(t *testing.T) {
	g := newRig(t)
	g.r.cfg.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.r.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}
