package clientopts

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, *crypto.AEAD, *time.Time) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || strat.ModifiedAt != o.ModifiedAt ||
		strat.ConfigOptions[OptionICEServers] != o.ICEServers || strat.ConfigOptions[OptionWebRTCCongestion] != "Y" {
		t.Fatalf("strategy: %+v %v", strat, err)
	}

	// Clearing still pushes the keys, empty, so clients drop them; a save in
	// the same millisecond is still a new version.
	o2, err := s.Set(ctx, "", nil, nil)
	if err != nil || o2.ModifiedAt <= o.ModifiedAt {
		t.Fatalf("clear: %+v %v", o2, err)
	}
	strat, _ = s.StrategyFor(ctx, nil)
	if len(strat.ConfigOptions) != 2 || strat.ConfigOptions[OptionICEServers] != "" || strat.ConfigOptions[OptionWebRTCCongestion] != "" {
		t.Fatalf("cleared strategy: %+v", strat.ConfigOptions)
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

	// 1. Create a device (falls back to default strategy "Standard Office")
	d := &store.Device{RustdeskID: "dev-mdm-1"}
	if err := st.Devices.Create(ctx, d); err != nil {
		t.Fatal(err)
	}

	strat, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if strat == nil {
		t.Fatal("expected non-nil strategy for device")
	}
	if strat.ConfigOptions["approve-mode"] != "both" {
		t.Fatalf("expected approve-mode both, got %q", strat.ConfigOptions["approve-mode"])
	}

	// 2. Set global ICE servers and ensure they merge
	on := true
	if _, err := s.Set(ctx, "stun:stun.l.google.com:19302", &on, nil); err != nil {
		t.Fatal(err)
	}

	merged, err := s.StrategyFor(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if merged.ConfigOptions["approve-mode"] != "both" {
		t.Fatalf("expected approve-mode both, got %q", merged.ConfigOptions["approve-mode"])
	}
	if merged.ConfigOptions[OptionICEServers] != "stun:stun.l.google.com:19302" {
		t.Fatalf("expected merged ICE server, got %q", merged.ConfigOptions[OptionICEServers])
	}
	if merged.ConfigOptions[OptionWebRTCCongestion] != "Y" {
		t.Fatalf("expected merged congestion control Y, got %q", merged.ConfigOptions[OptionWebRTCCongestion])
	}
}
