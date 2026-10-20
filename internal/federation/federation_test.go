package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

const peerToken = "peer-token-0123456789abcdef"

func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	key, _ := crypto.RandomBytes(32)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{DB: newTestDB(t), AEAD: aead}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func allowAll(context.Context) bool { return true }

// fakePeer is a remote panel's federation API (token peerToken).
type fakePeer struct {
	*httptest.Server
	relays atomic.Value // body: JSON of the relay list, or a raw string
	health atomic.Value // string status
	code   atomic.Int64 // forced status code (0 = normal)
}

func newFakePeer(t *testing.T) *fakePeer {
	t.Helper()
	p := &fakePeer{}
	p.setRelays([]PublicRelayDTO{{Name: "remote-nl", PublicHost: "desk-nl.example.com:21117", Region: "NL", Health: "active"}})
	p.health.Store("ok")
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+peerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c := p.code.Load(); c != 0 {
			if c == http.StatusFound {
				http.Redirect(w, r, "http://169.254.169.254/latest", int(c))
				return
			}
			http.Error(w, "forced", int(c))
			return
		}
		switch r.URL.Path {
		case "/api/v1/federation/health":
			_ = json.NewEncoder(w).Encode(HealthDTO{Status: p.health.Load().(string), Version: "0.5.9", ActiveRelays: 1})
		case "/api/v1/federation/relays":
			_, _ = io.WriteString(w, p.relays.Load().(string))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePeer) setRelays(list []PublicRelayDTO) {
	b, _ := json.Marshal(list)
	p.relays.Store(string(b))
}

func TestFederationStore(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	p, err := st.Create(ctx, CreatePeerInput{Name: "ams-cluster", APIURL: "https://desk-ams.example.com/", AuthToken: peerToken, SyncRelays: true})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	if p.ID <= 0 || p.Name != "ams-cluster" || p.Status != StatusUnknown || p.APIURL != "https://desk-ams.example.com" {
		t.Fatalf("unexpected peer: %+v", p)
	}
	if _, err := st.Create(ctx, CreatePeerInput{Name: "ams-cluster", APIURL: "https://other.example.com", AuthToken: peerToken}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	for _, bad := range []CreatePeerInput{
		{Name: "x", APIURL: "ftp://x.example.com", AuthToken: peerToken},
		{Name: "x", APIURL: "https://user:pw@x.example.com", AuthToken: peerToken},
		{Name: "x", APIURL: "https://x.example.com/?token=1", AuthToken: peerToken},
		{Name: "x", APIURL: "https://x.example.com", AuthToken: "short"},
		{Name: "x", APIURL: "https://x.example.com", AuthToken: "has a space in it 0123"},
		{Name: "", APIURL: "https://x.example.com", AuthToken: peerToken},
	} {
		if _, err := st.Create(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%+v: err %v, want ErrInvalid", bad, err)
		}
	}

	// The outgoing token survives restarts, sealed and bound to the peer id.
	if tok, err := st.Token(ctx, p.ID); err != nil || tok != peerToken {
		t.Fatalf("token: %q %v", tok, err)
	}
	var raw string
	if err := st.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, peerTokenKey(p.ID)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, peerToken) || !strings.Contains(raw, "token_enc") {
		t.Fatalf("token not sealed: %s", raw)
	}
	if _, err := st.DB.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, 0)`, peerTokenKey(p.ID+100), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Token(ctx, p.ID+100); err == nil {
		t.Fatal("a sealed token copied to another peer must not open")
	}

	if err := st.UpdateStatus(ctx, p.ID, StatusActive, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceFederatedRelays(ctx, p.ID, []FederatedRelay{
		{PeerID: p.ID, RemoteName: "nl-relay-1", PublicHost: "185.183.33.210:21117", Region: "NL", Health: "active"},
		{PeerID: p.ID, RemoteName: "nl-relay-2", PublicHost: "185.183.33.211:21117", Region: "NL", Health: "active"},
	}); err != nil {
		t.Fatal(err)
	}
	if rs, err := st.ListFederatedRelays(ctx); err != nil || len(rs) != 2 {
		t.Fatalf("federated relays: %v %d", err, len(rs))
	}

	// Delete cascades to relays and removes the sealed token.
	if err := st.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := st.ListFederatedRelays(ctx); len(rs) != 0 {
		t.Fatalf("relays left after delete: %d", len(rs))
	}
	if _, err := st.Token(ctx, p.ID); !errors.Is(err, ErrNoToken) {
		t.Fatalf("token left after delete: %v", err)
	}
	if err := st.Delete(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// TestFederationSyncAfterRestart: a fresh Service (as after a restart) syncs
// with the stored token.
func TestFederationSyncAfterRestart(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	peer := newFakePeer(t)
	first := NewService(Config{Store: st, Log: quietLog(), AllowPrivate: allowAll})
	p, err := first.CreatePeer(ctx, CreatePeerInput{Name: "remote-peer", APIURL: peer.URL, AuthToken: peerToken, SyncRelays: true})
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(Config{Store: st, Version: "0.6.0", Log: quietLog(), AllowPrivate: allowAll})
	if err := svc.SyncPeer(ctx, p.ID); err != nil {
		t.Fatalf("sync peer: %v", err)
	}
	updated, _ := st.Get(ctx, p.ID)
	if updated.Status != StatusActive || updated.LastError != nil {
		t.Fatalf("expected active, got %+v", updated)
	}
	rs, _ := st.ListFederatedRelays(ctx)
	if len(rs) != 1 || rs[0].PublicHost != "desk-nl.example.com:21117" {
		t.Fatalf("expected 1 federated relay, got %+v", rs)
	}

	// A legacy peer (no stored token) fails clearly instead of syncing
	// without a token.
	if _, err := st.DB.Exec(`DELETE FROM settings WHERE key = ?`, peerTokenKey(p.ID)); err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncPeer(ctx, p.ID); err == nil || !strings.Contains(err.Error(), "no stored token") {
		t.Fatalf("legacy peer: %v", err)
	}
	if got, _ := st.Get(ctx, p.ID); got.Status != StatusUnreachable {
		t.Fatalf("legacy peer status %s", got.Status)
	}
}

// TestFederationSyncErrors: every failed sync marks the peer unreachable with
// a short reason that does not echo URLs or dial errors.
func TestFederationSyncErrors(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	peer := newFakePeer(t)
	svc := NewService(Config{Store: st, Log: quietLog(), AllowPrivate: allowAll})
	p, err := svc.CreatePeer(ctx, CreatePeerInput{Name: "remote", APIURL: peer.URL, AuthToken: peerToken, SyncRelays: true})
	if err != nil {
		t.Fatal(err)
	}
	many := make([]PublicRelayDTO, maxAnnouncedRelays+1)
	for i := range many {
		many[i] = PublicRelayDTO{Name: fmt.Sprintf("r%d", i), PublicHost: "r.example.com", Health: "active"}
	}
	for _, tc := range []struct {
		name   string
		setup  func()
		reason string
	}{
		{"http 500", func() { peer.code.Store(500) }, "HTTP 500"},
		{"redirect", func() { peer.code.Store(http.StatusFound) }, "HTTP 302"},
		{"health not ok", func() { peer.health.Store("degraded") }, `status "degraded"`},
		{"invalid json", func() { peer.relays.Store("{not json") }, "invalid response"},
		{"duplicate names", func() {
			peer.setRelays([]PublicRelayDTO{{Name: "a", PublicHost: "a.example.com"}, {Name: "a", PublicHost: "b.example.com"}})
		}, `relay "a" twice`},
		{"injected host", func() {
			peer.setRelays([]PublicRelayDTO{{Name: "a", PublicHost: "a.example.com\n0.0.0.0/0 evil.example.com"}})
		}, "invalid host"},
		{"too many", func() { peer.setRelays(many) }, "more than"},
		{"too large", func() { peer.relays.Store(`"` + strings.Repeat("x", maxResponseBytes) + `"`) }, "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer.code.Store(0)
			peer.health.Store("ok")
			peer.setRelays([]PublicRelayDTO{{Name: "ok", PublicHost: "ok.example.com"}})
			if err := svc.SyncPeer(ctx, p.ID); err != nil {
				t.Fatalf("baseline sync: %v", err)
			}
			tc.setup()
			err := svc.SyncPeer(ctx, p.ID)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("err %v, want %q", err, tc.reason)
			}
			got, _ := st.Get(ctx, p.ID)
			if got.Status != StatusUnreachable || got.LastError == nil || !strings.Contains(*got.LastError, tc.reason) {
				t.Fatalf("peer %+v", got)
			}
			if rs, _ := st.ListFederatedRelays(ctx); len(rs) != 0 {
				t.Fatalf("relays of a failing peer still listed: %+v", rs)
			}
		})
	}

	// Wrong token on the peer side, then an unreachable peer.
	other, err := svc.CreatePeer(ctx, CreatePeerInput{Name: "wrong-token", APIURL: peer.URL, AuthToken: "another-token-0123456789", SyncRelays: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncPeer(ctx, other.ID); err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Fatalf("wrong token: %v", err)
	}
	peer.Close()
	err = svc.SyncPeer(ctx, p.ID)
	got, _ := st.Get(ctx, p.ID)
	host := strings.TrimPrefix(peer.URL, "http://")
	if err == nil || got.LastError == nil || *got.LastError != "peer unreachable" || strings.Contains(err.Error(), host) {
		t.Fatalf("unreachable: err %v last_error %v", err, got.LastError)
	}
}

// TestFederationSSRFGuard: private api_urls are refused at registration and
// at dial time unless private hosts are allowed.
func TestFederationSSRFGuard(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	var allow atomic.Bool
	svc := NewService(Config{Store: st, Log: quietLog(),
		AllowPrivate: func(context.Context) bool { return allow.Load() },
		Lookup: func(_ context.Context, _, host string) ([]netip.Addr, error) {
			switch host {
			case "internal.example.com":
				return []netip.Addr{netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("10.1.2.3")}, nil
			case "public.example.com":
				return []netip.Addr{netip.MustParseAddr("203.0.113.5")}, nil
			}
			return nil, errors.New("no such host")
		}})
	for _, u := range []string{
		"http://127.0.0.1:9", "http://10.0.0.1", "http://[::1]:8080", "http://169.254.169.254",
		"http://localhost:21114", "http://api.localhost", "http://internal.example.com",
	} {
		if _, err := svc.CreatePeer(ctx, CreatePeerInput{Name: "p", APIURL: u, AuthToken: peerToken}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: err %v, want ErrInvalid", u, err)
		}
	}
	if _, err := svc.CreatePeer(ctx, CreatePeerInput{Name: "public", APIURL: "https://public.example.com", AuthToken: peerToken}); err != nil {
		t.Fatalf("public peer: %v", err)
	}

	// Registered while private hosts were allowed, synced after they no
	// longer are: the guarded client refuses to dial.
	peer := newFakePeer(t)
	allow.Store(true)
	p, err := svc.CreatePeer(ctx, CreatePeerInput{Name: "lan", APIURL: peer.URL, AuthToken: peerToken, SyncRelays: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SyncPeer(ctx, p.ID); err != nil {
		t.Fatalf("allowed private peer: %v", err)
	}
	allow.Store(false)
	if err := svc.SyncPeer(ctx, p.ID); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private peer after the option was turned off: %v", err)
	}
}

func TestFederationPublicEndpoints(t *testing.T) {
	const inbound = "inbound-token-0123456789"
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	limiter := auth.NewBackoff(auth.BackoffConfig{Free: 2, Base: time.Minute, Max: time.Hour}, func() time.Time { return now })
	svc := NewService(Config{Store: newTestStore(t), Log: quietLog(), InboundToken: inbound, Version: "test"})
	r := chi.NewRouter()
	r.Route("/api/v1/federation", func(r chi.Router) { svc.RegisterPublic(r, limiter) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	get := func(path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	for _, path := range []string{"/api/v1/federation/health", "/api/v1/federation/relays"} {
		if resp := get(path, inbound); resp.StatusCode != http.StatusOK {
			t.Fatalf("%s with token: %d", path, resp.StatusCode)
		}
	}
	if resp := get("/api/v1/federation/health", ""); resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp := get("/api/v1/federation/relays", inbound+"x"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
	// Free attempts used up: back off, even with the right token.
	get("/api/v1/federation/health", "wrong-token-0123456789")
	if resp := get("/api/v1/federation/health", inbound); resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after failures: %d", resp.StatusCode)
	}
	now = now.Add(2 * time.Hour)
	if resp := get("/api/v1/federation/health", inbound); resp.StatusCode != http.StatusOK {
		t.Fatalf("after the backoff: %d", resp.StatusCode)
	}

	// No inbound token configured: nothing is accepted.
	closed := NewService(Config{Store: newTestStore(t), Log: quietLog()})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Authorization", "Bearer ")
	if closed.Authorized(req) {
		t.Fatal("empty inbound token accepted a request")
	}
}

func TestFederationRunStops(t *testing.T) {
	svc := NewService(Config{Store: newTestStore(t), Log: quietLog(), SyncInterval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
