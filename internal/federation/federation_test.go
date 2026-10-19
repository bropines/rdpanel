package federation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bropines/rdpanel/internal/db"
)

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

func TestFederationStore(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	st := &Store{DB: d}

	// 1. Create peer
	p, err := st.Create(ctx, CreatePeerInput{
		Name:       "ams-cluster",
		APIURL:     "https://desk-ams.example.com",
		AuthToken:  "secret-tok-123",
		SyncRelays: true,
	})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	if p.ID <= 0 || p.Name != "ams-cluster" || p.Status != StatusUnknown {
		t.Fatalf("unexpected peer: %+v", p)
	}

	// 2. Duplicate name should fail
	_, err = st.Create(ctx, CreatePeerInput{
		Name:      "ams-cluster",
		APIURL:    "https://other.example.com",
		AuthToken: "tok",
	})
	if err == nil {
		t.Fatal("expected conflict on duplicate name")
	}

	// 3. List
	peers, err := st.List(ctx)
	if err != nil || len(peers) != 1 {
		t.Fatalf("list peers: %v, count %d", err, len(peers))
	}

	// 4. Update status
	err = st.UpdateStatus(ctx, p.ID, StatusActive, nil)
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	p2, _ := st.Get(ctx, p.ID)
	if p2.Status != StatusActive || p2.LastSyncAt == nil {
		t.Fatalf("expected active and lastSyncAt set, got %+v", p2)
	}

	// 5. Replace federated relays
	relays := []FederatedRelay{
		{PeerID: p.ID, RemoteName: "nl-relay-1", PublicHost: "185.183.33.210:21117", Region: "NL", Health: "active"},
		{PeerID: p.ID, RemoteName: "nl-relay-2", PublicHost: "185.183.33.211:21117", Region: "NL", Health: "active"},
	}
	if err := st.ReplaceFederatedRelays(ctx, p.ID, relays); err != nil {
		t.Fatalf("replace relays: %v", err)
	}

	listRelays, err := st.ListFederatedRelays(ctx)
	if err != nil || len(listRelays) != 2 {
		t.Fatalf("list federated relays: %v, count %d", err, len(listRelays))
	}

	// 6. Delete peer cascades
	if err := st.Delete(ctx, p.ID); err != nil {
		t.Fatalf("delete peer: %v", err)
	}
	listRelaysAfter, _ := st.ListFederatedRelays(ctx)
	if len(listRelaysAfter) != 0 {
		t.Fatalf("expected 0 federated relays after peer deleted, got %d", len(listRelaysAfter))
	}
}

func TestFederationSync(t *testing.T) {
	ctx := context.Background()
	d := newTestDB(t)
	st := &Store{DB: d}

	// Mock remote peer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/federation/health":
			_ = json.NewEncoder(w).Encode(HealthDTO{Status: "ok", Version: "0.5.9", ActiveRelays: 1})
		case "/api/v1/federation/relays":
			_ = json.NewEncoder(w).Encode([]PublicRelayDTO{
				{Name: "remote-nl", PublicHost: "desk-nl.example.com:21117", Region: "NL", Health: "active"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svc := NewService(Config{
		Store:      st,
		Version:    "0.6.0",
		HTTPClient: server.Client(),
	})

	peer, err := st.Create(ctx, CreatePeerInput{
		Name:       "remote-peer",
		APIURL:     server.URL,
		AuthToken:  "test-token",
		SyncRelays: true,
	})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	svc.RegisterPeerToken(peer.ID, "test-token")

	if err := svc.SyncPeer(ctx, peer.ID); err != nil {
		t.Fatalf("sync peer: %v", err)
	}

	updated, _ := st.Get(ctx, peer.ID)
	if updated.Status != StatusActive {
		t.Fatalf("expected status active, got %s", updated.Status)
	}

	relays, _ := st.ListFederatedRelays(ctx)
	if len(relays) != 1 || relays[0].PublicHost != "desk-nl.example.com:21117" {
		t.Fatalf("expected 1 federated relay, got %+v", relays)
	}
}
