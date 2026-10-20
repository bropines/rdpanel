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

	// A fresh install (no default strategy, no saved client options) pushes
	// nothing: an upgrade must not reconfigure every device.
	if m := beat(0); m["strategy"] != nil || m["modified_at"] != nil {
		t.Fatalf("strategy without options or default strategy: %v", m)
	}

	on := true
	if _, err := a.ClientOptions.Set(ctx, "turn://u:p@turn.example.com:3478", &on, nil); err != nil {
		t.Fatal(err)
	}
	m := beat(0)
	s, _ := m["strategy"].(map[string]any)
	opts, _ := s["config_options"].(map[string]any)
	if m["modified_at"] == nil || opts["ice-servers"] != "turn://u:p@turn.example.com:3478" ||
		opts["allow-webrtc-congestion-control"] != "Y" || len(opts) != 2 {
		t.Fatalf("strategy: %v", m)
	}
	// A client that applied this version gets nothing more.
	version := int64(m["modified_at"].(float64))
	if m := beat(version); m["strategy"] != nil {
		t.Fatalf("re-sent: %v", m)
	}

	// A default strategy is merged in under a new version.
	office, err := a.Store.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.ClientStrategies.SetDefault(ctx, office.ID); err != nil {
		t.Fatal(err)
	}
	m = beat(version)
	s, _ = m["strategy"].(map[string]any)
	opts, _ = s["config_options"].(map[string]any)
	if m["modified_at"] == nil || int64(m["modified_at"].(float64)) == version || opts["approve-mode"] != "both" ||
		opts["ice-servers"] != "turn://u:p@turn.example.com:3478" {
		t.Fatalf("default strategy: %v", m)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
