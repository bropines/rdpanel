package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

		// Verify the signature the way a receiver would: HMAC over
		// "<timestamp>.<body>".
		ts := r.Header.Get("X-RDPanel-Timestamp")
		if ts != "1792152000" {
			t.Errorf("timestamp header %q", ts)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
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
		Now:    func() time.Time { return time.Unix(1792152000, 0) },
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

// TestWebhookLogsOnlySchemeAndHost: webhook URLs carry tokens in the path or
// query; send errors must not log them (url.Error repeats the full URL).
func TestWebhookLogsOnlySchemeAndHost(t *testing.T) {
	var buf bytes.Buffer
	svc := New(Config{
		URL: "http://127.0.0.1:1/services/T0/B0/pathtoken123?token=querytoken456",
		Log: slog.New(slog.NewTextHandler(&buf, nil)),
	})
	svc.dispatch(context.Background(), events.Event{Type: events.HBBSHealth, Time: time.Now(), Data: events.Health{Component: "hbbs"}})
	out := buf.String()
	if !strings.Contains(out, "webhook: send error") || !strings.Contains(out, "http://127.0.0.1:1") {
		t.Fatalf("expected a send error naming scheme://host, got %s", out)
	}
	for _, leak := range []string{"pathtoken123", "querytoken456", "/services"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %s", leak, out)
		}
	}
}

func TestSign(t *testing.T) {
	a := Sign("k", 100, []byte("{}"))
	if a == Sign("k", 101, []byte("{}")) || a == Sign("k", 100, []byte("{} ")) || !strings.HasPrefix(a, "sha256=") {
		t.Fatalf("signature does not bind timestamp and body: %s", a)
	}
}
