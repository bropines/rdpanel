package clientopts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
	"github.com/bropines/rdpanel/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, *crypto.AEAD, *time.Time) {
	t.Helper()
	d := dbtest.Open(t)
	key, _ := crypto.RandomBytes(32)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	st := store.New(d, store.WithSecrets(aead))
	return New(st, aead, func() time.Time { return now }), st, aead, &now
}

func TestNormalizeICEServers(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{" \n ", ""},
		{"stun:stun.example.com:3478", "stun:stun.example.com:3478"},
		{"stun:a.example.com:3478, turn://u:p@b.example.com:3478\nturns://u:p%40ss@c.example.com:5349",
			"stun:a.example.com:3478,turn://u:p@b.example.com:3478,turns://u:p%40ss@c.example.com:5349"},
		{"stun://stun.example.com", "stun://stun.example.com"},
		{"stun:[2001:db8::1]:3478", "stun:[2001:db8::1]:3478"},
	} {
		got, err := NormalizeICEServers(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%q: got %q %v, want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"http://example.com",
		"turn:turn.example.com:3478",        // RFC 7065 form cannot carry credentials
		"turn://user@turn.example.com:3478", // no password
		"turns://turn.example.com",
		"stun:",
		"stun://",
		"stun:a b",
		strings.Repeat("stun:a.example.com,", MaxICEServers+1),
		"stun:" + strings.Repeat("a", MaxICEServerLen),
	} {
		if _, err := NormalizeICEServers(bad); !errors.Is(err, ErrInvalidICEServer) && (err == nil || !strings.Contains(err.Error(), "at most")) {
			t.Errorf("%q: want error, got %v", bad, err)
		}
	}
}

func TestSetGetAndStrategy(t *testing.T) {
	s, st, aead, now := newService(t)
	ctx := context.Background()

	// Never saved: nothing is pushed.
	if o, err := s.Get(ctx); err != nil || o != nil {
		t.Fatalf("get unset: %+v %v", o, err)
	}
	if strat, err := s.StrategyFor(ctx, nil); err != nil || strat != nil {
		t.Fatalf("strategy unset: %+v %v", strat, err)
	}

	on := true
	o, err := s.Set(ctx, "stun:a.example.com:3478\nturn://u:secret@b.example.com:3478", &on, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.ICEServers != "stun:a.example.com:3478,turn://u:secret@b.example.com:3478" || o.ModifiedAt != now.UnixMilli() {
		t.Fatalf("set: %+v", o)
	}

	// At rest the list is sealed: no credentials in the settings row.
	raw, err := st.Settings.GetRaw(ctx, SettingKey)
	if err != nil || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "example.com") {
		t.Fatalf("stored: %s %v", raw, err)
	}

	// A fresh service (process restart) reads it back.
	s2 := New(st, aead, nil)
	got, err := s2.Get(ctx)
	if err != nil || got == nil || got.ICEServers != o.ICEServers || got.WebRTCCongestion == nil || !*got.WebRTCCongestion {
		t.Fatalf("reload: %+v %v", got, err)
	}
	strat, err := s2.StrategyFor(ctx, nil)
	if err != nil || strat.ModifiedAt != Version(strat.ConfigOptions) ||
		strat.ConfigOptions[OptionICEServers] != o.ICEServers || strat.ConfigOptions[OptionWebRTCCongestion] != "Y" {
		t.Fatalf("strategy: %+v %v", strat, err)
	}

	// Clearing still pushes the keys, empty, so clients drop them, under a
	// new version (also within the same millisecond).
	o2, err := s.Set(ctx, "", nil, nil)
	if err != nil || o2.ModifiedAt <= o.ModifiedAt {
		t.Fatalf("clear: %+v %v", o2, err)
	}
	cleared, _ := s.StrategyFor(ctx, nil)
	if len(cleared.ConfigOptions) != 2 || cleared.ConfigOptions[OptionICEServers] != "" ||
		cleared.ConfigOptions[OptionWebRTCCongestion] != "" || cleared.ModifiedAt == strat.ModifiedAt {
		t.Fatalf("cleared strategy: %+v", cleared)
	}
	off := false
	if o3, _ := s.Set(ctx, "", &off, nil); ConfigOptions(o3)[OptionWebRTCCongestion] != "N" {
		t.Fatalf("off: %+v", ConfigOptions(o3))
	}

	// Invalid input is rejected and nothing changes.
	if _, err := s.Set(ctx, "turn:nocreds.example.com", nil, nil); !errors.Is(err, ErrInvalidICEServer) {
		t.Fatalf("invalid: %v", err)
	}

	// A wrong key cannot open the stored list.
	if _, err := s.Set(ctx, "stun:a.example.com", nil, nil); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.RandomBytes(32)
	other, _ := crypto.NewAEAD(key)
	if _, err := New(st, other, nil).Get(ctx); err == nil {
		t.Fatal("foreign key must fail")
	}
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	if _, ok := v["ice_servers_enc"]; !ok {
		t.Fatalf("stored shape: %v", v)
	}
}

func TestStrategyForDeviceMDMMerge(t *testing.T) {
	s, st, _, _ := newService(t)
	ctx := context.Background()

	// 1. A device without a strategy, on a fresh install (no default strategy,
	// no saved client options): nothing is pushed.
	d := &store.Device{RustdeskID: "dev-mdm-1"}
	if err := st.Devices.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if strat, err := s.StrategyFor(ctx, d); err != nil || strat != nil {
		t.Fatalf("no strategy, no options: %+v %v", strat, err)
	}

	// 2. Saved global options alone: only they are pushed.
	on := true
	if _, err := s.Set(ctx, "stun:stun.l.google.com:19302", &on, nil); err != nil {
		t.Fatal(err)
	}
	global, err := s.StrategyFor(ctx, d)
	if err != nil || len(global.ConfigOptions) != 2 || global.ConfigOptions[OptionICEServers] != "stun:stun.l.google.com:19302" {
		t.Fatalf("global options only: %+v %v", global, err)
	}

	// 3. Under a strategy the global options are merged in.
	office, err := st.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	d.StrategyID = &office.ID
	merged, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if merged.ConfigOptions["approve-mode"] != "both" || merged.ConfigOptions[OptionICEServers] != "stun:stun.l.google.com:19302" ||
		merged.ConfigOptions[OptionWebRTCCongestion] != "Y" || merged.ModifiedAt == global.ModifiedAt {
		t.Fatalf("merged: %+v", merged)
	}
}

func TestVersion(t *testing.T) {
	a := map[string]string{"approve-mode": "both", "ice-servers": ""}
	b := map[string]string{"ice-servers": "", "approve-mode": "both"}
	if Version(a) != Version(b) {
		t.Fatal("version depends on map order")
	}
	for _, other := range []map[string]string{
		{"approve-mode": "both"}, // a key dropped
		{"approve-mode": "both", "ice-servers": "stun:a.example.com"}, // a value changed
		{"approve-mode": "both", "ice-servers": "", "whitelist": ""},  // a key cleared
		{"approve-modeboth": "", "ice-servers": ""},                   // no key/value confusion
	} {
		if Version(other) == Version(a) {
			t.Errorf("same version for %v and %v", other, a)
		}
	}
	for _, m := range []map[string]string{a, {}, {"x": "y"}} {
		if v := Version(m); v <= 0 || v >= 1<<52 {
			t.Errorf("version %d out of (0, 2^52)", v)
		}
	}
}

// The seeded strategies share updated_at, so a timestamp version did not
// change when a device switched between them; and keys only the previous
// strategy set were never removed.
func TestStrategySwitchClearsKeysAndChangesVersion(t *testing.T) {
	s, st, _, _ := newService(t)
	ctx := context.Background()
	strict, err := st.ClientStrategies.GetByName(ctx, "Strict Security")
	if err != nil {
		t.Fatal(err)
	}
	server, err := st.ClientStrategies.GetByName(ctx, "Unattended Server")
	if err != nil {
		t.Fatal(err)
	}
	if !strict.UpdatedAt.Equal(server.UpdatedAt) {
		t.Fatal("test assumes seeds with equal updated_at")
	}
	kiosk := &store.ClientStrategy{Name: "Kiosk", ConfigOptions: map[string]string{"whitelist": "10.0.0.0/8"}}
	if err := st.ClientStrategies.Create(ctx, kiosk); err != nil {
		t.Fatal(err)
	}

	d := &store.Device{RustdeskID: "dev-switch", StrategyID: &kiosk.ID}
	if err := st.Devices.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	first, err := s.StrategyFor(ctx, d)
	if err != nil || first.ConfigOptions["whitelist"] != "10.0.0.0/8" || first.ConfigOptions["approve-mode"] != "" {
		t.Fatalf("kiosk: %+v %v", first, err)
	}
	if _, ok := first.ConfigOptions[OptionICEServers]; ok {
		t.Fatalf("unsaved global options pushed: %+v", first.ConfigOptions)
	}

	d.StrategyID = &strict.ID
	a, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := a.ConfigOptions["whitelist"]; !ok || v != "" {
		t.Fatalf("key of the previous strategy not cleared: %+v", a.ConfigOptions)
	}
	d.StrategyID = &server.ID
	b, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if a.ModifiedAt == b.ModifiedAt || a.ModifiedAt == first.ModifiedAt || b.ConfigOptions["approve-mode"] != "password" {
		t.Fatalf("switch: strict %d server %d kiosk %d", a.ModifiedAt, b.ModifiedAt, first.ModifiedAt)
	}
	// Same effective options, same version: no re-push.
	if again, _ := s.StrategyFor(ctx, d); again.ModifiedAt != b.ModifiedAt || again.ModifiedAt <= 0 {
		t.Fatalf("unstable version: %d vs %d", again.ModifiedAt, b.ModifiedAt)
	}

	// Clearing the global options under a strategy removes them on clients.
	on := true
	if _, err := s.Set(ctx, "stun:stun.example.com", &on, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	c, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := c.ConfigOptions[OptionICEServers]; !ok || v != "" {
		t.Fatalf("cleared ICE servers not pushed: %+v", c.ConfigOptions)
	}
	if v, ok := c.ConfigOptions[OptionWebRTCCongestion]; !ok || v != "" {
		t.Fatalf("cleared congestion control not pushed: %+v", c.ConfigOptions)
	}
}
