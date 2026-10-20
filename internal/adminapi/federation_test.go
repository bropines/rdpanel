package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/federation"
	"github.com/bropines/rdpanel/internal/store"
)

const fedPeerToken = "secret-token-0123456789"

func TestFederationAdminAPI(t *testing.T) {
	// Federation off (the default): 503 federation_disabled.
	eNil := newB2Env(t, b2opts{})
	adminNil, _ := eNil.as("fedadmin0", store.RoleAdmin)
	code, m, _ := adminNil.do("GET", "/federation/peers", nil)
	wantErr(t, "federation disabled", code, http.StatusServiceUnavailable, m, "federation_disabled")

	// A fake remote peer RD Panel (accepts fedPeerToken only).
	var hits atomic.Int64
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fedPeerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/federation/health":
			_ = json.NewEncoder(w).Encode(federation.HealthDTO{Status: "ok", Version: "v0.6.0", ActiveRelays: 1})
		case "/api/v1/federation/relays":
			_ = json.NewEncoder(w).Encode([]federation.PublicRelayDTO{
				{Name: "remote-relay-1", PublicHost: "198.51.100.10:21117", Region: "us-east", Health: "active"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(peerSrv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var allowPrivate atomic.Bool
	eFed := newB2Env(t, b2opts{federation: &federation.Config{Log: log,
		AllowPrivate: func(context.Context) bool { return allowPrivate.Load() }}})
	admin, _ := eFed.as("fedadmin", store.RoleAdmin)
	st := eFed.deps.Federation.Store()

	code, m, _ = admin.do("GET", "/federation/peers", nil)
	want(t, "list peers", code, http.StatusOK, m)

	for _, bad := range []map[string]any{
		{"name": "invalid-peer", "api_url": "ftp://invalid", "auth_token": fedPeerToken},
		{"name": "short-token", "api_url": "https://peer.example.com", "auth_token": "token"},
		// The httptest peer is on loopback: refused until private hosts are allowed.
		{"name": "loopback", "api_url": peerSrv.URL, "auth_token": fedPeerToken},
	} {
		code, m, _ = admin.do("POST", "/federation/peers", bad)
		wantErr(t, fmt.Sprintf("create %v", bad["name"]), code, http.StatusBadRequest, m, "validation_failed")
	}
	if hits.Load() != 0 {
		t.Fatal("a refused peer was contacted")
	}

	allowPrivate.Store(true)
	code, m, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name": "peer-one", "api_url": peerSrv.URL + "/", "auth_token": fedPeerToken, "sync_relays": true,
	})
	want(t, "create peer", code, http.StatusCreated, m)
	peerID := int64(m["id"].(float64))
	if _, leaked := m["auth_token"]; leaked || strings.Contains(fmt.Sprint(m), fedPeerToken) {
		t.Fatalf("token returned: %v", m)
	}

	// The background first sync outlives the create request.
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, err := st.Get(context.Background(), peerID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status == federation.StatusActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background sync did not complete: %+v", p)
		}
		time.Sleep(10 * time.Millisecond)
	}

	code, _, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name": "peer-one", "api_url": peerSrv.URL, "auth_token": fedPeerToken,
	})
	if code != http.StatusConflict {
		t.Fatalf("duplicate peer: status %d, want 409", code)
	}

	code, m, _ = admin.do("POST", fmt.Sprintf("/federation/peers/%d/sync", peerID), nil)
	want(t, "sync peer", code, http.StatusOK, m)
	if m["status"] != string(federation.StatusActive) {
		t.Fatalf("expected peer status active, got %v", m)
	}
	code, m, _ = admin.do("GET", "/federation/relays", nil)
	want(t, "list federated relays", code, http.StatusOK, m)
	if raw, _ := m["_raw"].(string); !strings.Contains(raw, "remote-relay-1") {
		t.Fatalf("federated relays: %v", m)
	}

	// A peer with the wrong token: 502 with a short reason, peer marked failed.
	code, m, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name": "peer-two", "api_url": peerSrv.URL, "auth_token": "wrong-token-0123456789",
	})
	want(t, "create peer two", code, http.StatusCreated, m)
	twoID := int64(m["id"].(float64))
	code, m, _ = admin.do("POST", fmt.Sprintf("/federation/peers/%d/sync", twoID), nil)
	wantErr(t, "sync wrong token", code, http.StatusBadGateway, m, "sync_failed")
	if p, _ := st.Get(context.Background(), twoID); p.Status != federation.StatusUnreachable || p.LastError == nil ||
		!strings.Contains(*p.LastError, "rejected the token") {
		t.Fatalf("peer two: %+v", p)
	}
	code, m, _ = admin.do("POST", "/federation/peers/999999/sync", nil)
	wantErr(t, "sync missing peer", code, http.StatusNotFound, m, "not_found")

	code, _, _ = admin.do("DELETE", fmt.Sprintf("/federation/peers/%d", peerID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete peer: status %d, want 204", code)
	}
	relays, err := st.ListFederatedRelays(context.Background())
	if err != nil || len(relays) != 0 {
		t.Fatalf("expected 0 federated relays after peer delete, got %+v %v", relays, err)
	}
}
