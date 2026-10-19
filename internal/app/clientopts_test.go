package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

// The client options saved through the admin side reach clients in the
// heartbeat strategy, once per version (the shared service is wired into the
// client API).
func TestClientOptionsHeartbeatStrategy(t *testing.T) {
	a := newTestApp(t, nil)
	ctx := context.Background()
	if err := a.Store.Devices.Create(ctx, &store.Device{RustdeskID: "123456789", UUID: "dXVpZA=="}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	beat := func(modifiedAt int64) map[string]any {
		t.Helper()
		body := `{"id":"123456789","uuid":"dXVpZA==","ver":1005000,"modified_at":` + jsonInt(modifiedAt) + `}`
		req := httptest.NewRequest(http.MethodPost, "/api/heartbeat", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	// Fresh client (modified_at: 0) receives the default strategy.
	m0 := beat(0)
	if m0["strategy"] == nil || m0["modified_at"] == nil {
		t.Fatalf("expected default strategy, got %v", m0)
	}
	stratMod := int64(m0["modified_at"].(float64))

	// Client that applied the default strategy gets nothing more until options change.
	if m := beat(stratMod); m["strategy"] != nil {
		t.Fatalf("unexpected strategy: %v", m)
	}

	on := true
	o, err := a.ClientOptions.Set(ctx, "turn://u:p@turn.example.com:3478", &on, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := beat(stratMod)
	s, _ := m["strategy"].(map[string]any)
	opts, _ := s["config_options"].(map[string]any)
	if int64(m["modified_at"].(float64)) != o.ModifiedAt || opts["ice-servers"] != "turn://u:p@turn.example.com:3478" ||
		opts["allow-webrtc-congestion-control"] != "Y" || opts["approve-mode"] != "both" {
		t.Fatalf("strategy: %v", m)
	}
	// A client that applied this version gets nothing more.
	if m := beat(o.ModifiedAt); m["strategy"] != nil {
		t.Fatalf("re-sent: %v", m)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
