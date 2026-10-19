package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/federation"
	"github.com/bropines/rdpanel/internal/store"
)

func TestFederationAdminAPI(t *testing.T) {
	// When Federation is nil, endpoints return 503
	eNil := newB2Env(t, b2opts{})
	adminNil, _ := eNil.as("fedadmin0", store.RoleAdmin)
	code, m, _ := adminNil.do("GET", "/federation/peers", nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when federation is nil, got %d: %v", code, m)
	}

	// 2. Set up a fake remote peer RD Panel
	peerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/v1/federation/health" {
			_ = json.NewEncoder(w).Encode(federation.HealthDTO{
				Status:       "ok",
				Version:      "v0.6.0",
				ActiveRelays: 1,
			})
			return
		}
		if r.URL.Path == "/api/v1/federation/relays" {
			_ = json.NewEncoder(w).Encode([]federation.PublicRelayDTO{
				{
					Name:       "remote-relay-1",
					PublicHost: "198.51.100.10:21117",
					Region:     "us-east",
					Health:     "healthy",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(peerSrv.Close)

	// Create environment with federation service enabled
	e := newB2Env(t, b2opts{})
	fedStore := &federation.Store{DB: e.deps.Schema.(*db.DB), Now: func() time.Time { return time.Now().UTC() }}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fedSvc := federation.NewService(federation.Config{
		Store:      fedStore,
		Log:        log,
		HTTPClient: http.DefaultClient,
	})

	// Re-initialize env with federation
	eFed := newB2Env(t, b2opts{federation: fedSvc})
	admin, _ := eFed.as("fedadmin", store.RoleAdmin)

	// 1. Initially empty list of peers
	code, _, _ = admin.do("GET", "/federation/peers", nil)
	if code != http.StatusOK {
		t.Fatalf("list peers: status %d, want 200", code)
	}

	// 3. Create peer with invalid URL -> 400
	code, _, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name":       "invalid-peer",
		"api_url":    "ftp://invalid",
		"auth_token": "token",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("create invalid peer: status %d, want 400", code)
	}

	// 4. Create peer successfully -> 201
	code, m, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name":        "peer-one",
		"api_url":     peerSrv.URL,
		"auth_token":  "secret-token",
		"sync_relays": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("create peer: status %d, want 201: %v", code, m)
	}
	peerID := int64(m["id"].(float64))

	// 5. Create duplicate peer -> 409
	code, _, _ = admin.do("POST", "/federation/peers", map[string]any{
		"name":       "peer-one",
		"api_url":    peerSrv.URL,
		"auth_token": "secret-token",
	})
	if code != http.StatusConflict {
		t.Fatalf("duplicate peer: status %d, want 409", code)
	}

	// 6. Explicitly sync peer -> 200
	code, m, _ = admin.do("POST", fmt.Sprintf("/federation/peers/%d/sync", peerID), nil)
	if code != http.StatusOK {
		t.Fatalf("sync peer: status %d, want 200: %v", code, m)
	}
	if status, _ := m["status"].(string); status != string(federation.StatusActive) {
		t.Fatalf("expected peer status active, got %s", status)
	}

	// 7. List federated relays -> 200 with 1 relay
	code, m, _ = admin.do("GET", "/federation/relays", nil)
	if code != http.StatusOK {
		t.Fatalf("list federated relays: status %d, want 200: %v", code, m)
	}

	// Check directly with store to verify the relay was saved
	relays, err := eFed.deps.Federation.Store().ListFederatedRelays(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(relays) != 1 || relays[0].RemoteName != "remote-relay-1" {
		t.Fatalf("expected 1 federated relay 'remote-relay-1', got %+v", relays)
	}

	// 8. Delete peer -> 204
	code, _, _ = admin.do("DELETE", fmt.Sprintf("/federation/peers/%d", peerID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete peer: status %d, want 204", code)
	}

	// 9. Relays are cascaded and deleted
	relays, err = eFed.deps.Federation.Store().ListFederatedRelays(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(relays) != 0 {
		t.Fatalf("expected 0 federated relays after peer delete, got %+v", relays)
	}
}
