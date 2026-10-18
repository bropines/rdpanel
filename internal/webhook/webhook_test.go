package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/events"
)

func TestWebhookDispatch(t *testing.T) {
	var (
		mu       sync.Mutex
		received []Payload
		headers  []http.Header
	)

	secret := "super-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p Payload
		_ = json.Unmarshal(body, &p)
		mu.Lock()
		received = append(received, p)
		headers = append(headers, r.Header.Clone())
		mu.Unlock()

		// Verify signature
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if got := r.Header.Get("X-Signature-256"); got != wantSig {
			t.Errorf("got sig %s, want %s", got, wantSig)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bus := events.NewBus(nil)
	svc := New(Config{
		URL:    srv.URL,
		Secret: secret,
		Bus:    bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go svc.Run(ctx)

	// Wait for subscription to register
	for bus.Subscribers() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// Publish an active relay event (should be filtered out)
	bus.Publish(events.RelayHealth, events.RelayState{
		ID:     1,
		Name:   "relay-1",
		Health: "active",
	}, events.Admins)

	// Publish an offline relay event (should be sent)
	bus.Publish(events.RelayHealth, events.RelayState{
		ID:     2,
		Name:   "relay-2",
		Health: "offline",
		Error:  "connection refused",
	}, events.Admins)

	// Publish HBBS up event (should be filtered out)
	bus.Publish(events.HBBSHealth, events.Health{
		Component: "hbbs",
		Up:        true,
	}, events.Admins)

	// Publish HBBS down event (should be sent)
	bus.Publish(events.HBBSHealth, events.Health{
		Component: "hbbs",
		Up:        false,
		Error:     "timeout",
	}, events.Admins)

	// Wait for events to arrive
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 2 {
		t.Fatalf("received %d webhooks, want 2: %+v", len(received), received)
	}

	if received[0].Event != string(events.RelayHealth) {
		t.Errorf("first event = %s, want relay.health", received[0].Event)
	}
	if received[1].Event != string(events.HBBSHealth) {
		t.Errorf("second event = %s, want hbbs.health", received[1].Event)
	}
}
