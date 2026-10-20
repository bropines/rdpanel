// Package clientopts holds the RustDesk client options the panel pushes to
// every device through the heartbeat "strategy" (config_options), currently
// the WebRTC ones: ice-servers (STUN/TURN for the WebRTC transport of RustDesk
// 1.5+) and allow-webrtc-congestion-control. See docs/webrtc.md and
// docs/research/client-api.md §7.4.
//
// The value is one JSON document in the settings table; the ICE server list is
// AEAD-sealed because TURN URLs carry credentials. Nothing is pushed until an
// admin saves the options once; from then on the panel owns these keys on the
// clients ("" removes a key, which restores the client default). Client
// strategies assigned to a device, its group or marked default are merged in
// by StrategyFor.
package clientopts

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bropines/rdpanel/internal/clientapi"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/store"
)

// SettingKey is the settings row holding the options.
const SettingKey = "client.options"

// RustDesk option names pushed in config_options.
const (
	OptionICEServers       = "ice-servers"
	OptionWebRTCCongestion = "allow-webrtc-congestion-control"
)

// Limits of the ICE server list.
const (
	MaxICEServers   = 16
	MaxICEServerLen = 512
)

// Options are the client options as the admin API sees them.
type Options struct {
	// ICEServers is a comma-separated list of stun:/stuns:/turn:/turns: URLs;
	// TURN entries must carry credentials (turn://user:pass@host:port). ""
	// removes the option from clients (they use RustDesk's default STUN).
	ICEServers string
	// WebRTCCongestion sets allow-webrtc-congestion-control; nil removes it.
	WebRTCCongestion *bool
	// ModifiedAt (unix ms) is when the options were last saved (the heartbeat
	// version is a hash of the pushed options, see Version).
	ModifiedAt int64
	UpdatedAt  time.Time
}

type stored struct {
	ICEServersEnc    []byte `json:"ice_servers_enc,omitempty"`
	WebRTCCongestion *bool  `json:"webrtc_congestion_control"`
	ModifiedAt       int64  `json:"modified_at"`
}

// Service reads and writes the options and serves them as a heartbeat
// strategy (clientapi.StrategyProvider). The decoded value is cached; Set
// refreshes it, so the heartbeat path costs no database read.
type Service struct {
	st   *store.Store
	aead *crypto.AEAD
	now  func() time.Time

	mu     sync.Mutex
	loaded bool
	cached *Options
}

// New returns a Service. now may be nil (time.Now).
func New(st *store.Store, aead *crypto.AEAD, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, aead: aead, now: now}
}

// Get returns the saved options, or nil when they were never saved.
func (s *Service) Get(ctx context.Context) (*Options, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return copyOpts(s.cached), nil
	}
	o, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	s.cached, s.loaded = o, true
	return copyOpts(o), nil
}

func (s *Service) load(ctx context.Context) (*Options, error) {
	var v stored
	err := s.st.Settings.Get(ctx, SettingKey, &v)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o := &Options{WebRTCCongestion: v.WebRTCCongestion, ModifiedAt: v.ModifiedAt, UpdatedAt: time.UnixMilli(v.ModifiedAt)}
	if len(v.ICEServersEnc) > 0 {
		if s.aead == nil {
			return nil, errors.New("clientopts: no AEAD key for the stored ICE servers")
		}
		if o.ICEServers, err = s.aead.OpenString(v.ICEServersEnc, crypto.ICEServersAD()); err != nil {
			return nil, fmt.Errorf("clientopts: open ICE servers: %w", err)
		}
	}
	return o, nil
}

// Set validates and stores the options (a new strategy version) and returns
// them as saved. actor may be nil.
func (s *Service) Set(ctx context.Context, iceServers string, congestion *bool, actor *int64) (*Options, error) {
	ice, err := NormalizeICEServers(iceServers)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	modified := s.now().UnixMilli()
	if s.loaded && s.cached != nil && modified <= s.cached.ModifiedAt {
		modified = s.cached.ModifiedAt + 1 // a new version even within the same millisecond
	}
	v := stored{WebRTCCongestion: congestion, ModifiedAt: modified}
	if ice != "" {
		if s.aead == nil {
			return nil, errors.New("clientopts: no AEAD key")
		}
		if v.ICEServersEnc, err = s.aead.SealString(ice, crypto.ICEServersAD()); err != nil {
			return nil, err
		}
	}
	if err := s.st.Settings.Set(ctx, SettingKey, v, actor); err != nil {
		return nil, err
	}
	o := &Options{ICEServers: ice, WebRTCCongestion: congestion, ModifiedAt: modified, UpdatedAt: time.UnixMilli(modified)}
	s.cached, s.loaded = o, true
	return copyOpts(o), nil
}

// StrategyFor implements clientapi.StrategyProvider: the effective MDM
// strategy of d (device -> group -> default) merged with the global client
// options (such as ICE servers).
//
// Without a strategy only the admin-saved client options are pushed (nothing
// at all until they are saved), as before strategies existed. Under a
// strategy the panel owns every key any strategy can set: keys the effective
// strategy does not set are sent as "" so a switch from another strategy does
// not leave its options behind.
//
// The version (ModifiedAt) is a hash of the pushed map: the client stores
// whatever differs from what it sent (docs/research/client-api.md §7.4), so any
// change of the effective options, and only that, reaches the device.
func (s *Service) StrategyFor(ctx context.Context, d *store.Device) (*clientapi.Strategy, error) {
	o, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	var strat *store.ClientStrategy
	if s.st != nil && s.st.ClientStrategies != nil {
		if strat, err = s.st.ClientStrategies.ResolveForDevice(ctx, d); err != nil {
			return nil, err
		}
	}
	if strat == nil && o == nil {
		return nil, nil
	}

	cfg := make(map[string]string)
	if strat != nil {
		keys, err := s.st.ClientStrategies.ManagedKeys(ctx)
		if err != nil {
			return nil, err
		}
		for k := range keys {
			cfg[k] = ""
		}
	}
	if o != nil {
		for k, v := range ConfigOptions(o) { // "" when cleared: removed on clients
			cfg[k] = v
		}
	}
	if strat != nil {
		for k, v := range strat.ConfigOptions { // the strategy wins over global options
			cfg[k] = v
		}
	}
	return &clientapi.Strategy{ModifiedAt: Version(cfg), ConfigOptions: cfg}, nil
}

// Version is the strategy version of a config_options map: the first 52 bits
// of a SHA-256 over the sorted key/value pairs. 52 bits stay exact in JSON
// tooling that reads numbers as doubles; it is never 0, which clients send
// when they applied nothing yet.
func Version(cfg map[string]string) int64 {
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(cfg[k]))
		h.Write([]byte{0})
	}
	v := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]) >> 12)
	if v == 0 {
		v = 1
	}
	return v
}

// ConfigOptions is the config_options map pushed to clients. Every managed key
// is present; "" removes it on the client.
func ConfigOptions(o *Options) map[string]string {
	cc := ""
	if o.WebRTCCongestion != nil {
		cc = "N"
		if *o.WebRTCCongestion {
			cc = "Y"
		}
	}
	return map[string]string{OptionICEServers: o.ICEServers, OptionWebRTCCongestion: cc}
}

// NormalizeICEServers validates a comma- or newline-separated ICE server list
// the way RustDesk parses it (hbb_common webrtc.rs get_ice_server_from_url) and
// returns it comma-joined. TURN URLs without credentials are rejected because
// the client drops them silently.
func NormalizeICEServers(in string) (string, error) {
	var out []string
	for _, part := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		e := strings.TrimSpace(part)
		if e == "" {
			continue
		}
		if err := checkICEServer(e); err != nil {
			return "", fmt.Errorf("%q: %w", e, err)
		}
		out = append(out, e)
	}
	if len(out) > MaxICEServers {
		return "", fmt.Errorf("at most %d ICE servers", MaxICEServers)
	}
	return strings.Join(out, ","), nil
}

// ErrInvalidICEServer is wrapped by NormalizeICEServers' errors.
var ErrInvalidICEServer = errors.New("invalid ICE server")

func checkICEServer(e string) error {
	bad := func(why string) error { return fmt.Errorf("%w: %s", ErrInvalidICEServer, why) }
	if len(e) > MaxICEServerLen {
		return bad("too long")
	}
	if strings.ContainsAny(e, " \t") {
		return bad("contains spaces")
	}
	u, err := url.Parse(e)
	if err != nil {
		return bad("not a URL")
	}
	switch u.Scheme {
	case "stun", "stuns", "turn", "turns":
	default:
		return bad("scheme must be stun, stuns, turn or turns")
	}
	host := u.Hostname()
	if u.Host == "" {
		// RFC 7065 form scheme:host[:port] (no credentials possible).
		host = u.Opaque
		if i := strings.LastIndex(host, ":"); i > 0 && !strings.HasSuffix(host, "]") {
			host = host[:i]
		}
		host = strings.Trim(host, "[]")
	}
	if host == "" {
		return bad("no host")
	}
	if u.Scheme == "turn" || u.Scheme == "turns" {
		pw, _ := u.User.Password()
		if u.User == nil || u.User.Username() == "" || pw == "" {
			return bad("TURN needs credentials: turn://user:pass@host:port")
		}
	}
	return nil
}

func copyOpts(o *Options) *Options {
	if o == nil {
		return nil
	}
	c := *o
	if o.WebRTCCongestion != nil {
		v := *o.WebRTCCongestion
		c.WebRTCCongestion = &v
	}
	return &c
}
