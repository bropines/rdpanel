package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebWSProxy(t *testing.T) {
	var gotPath, gotRealIP, gotForwarded string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRealIP = r.Header.Get("X-Real-IP")
		gotForwarded = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ws-backend-ok"))
	}))
	defer backend.Close()

	a := newTestApp(t, map[string]string{
		"RDPANEL_HBBS_WS_ID_SERVER":    backend.Listener.Addr().String(),
		"RDPANEL_HBBS_WS_RELAY_SERVER": backend.Listener.Addr().String(),
		"RDPANEL_HBBS_WEB_PROXY":       "true",
	})

	h := a.Handler()
	// Test /ws/id
	req := httptest.NewRequest(http.MethodGet, "/ws/id", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/ws/id code %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/ws/id" {
		t.Errorf("gotPath = %q, want /ws/id", gotPath)
	}
	if gotRealIP != "192.0.2.10" {
		t.Errorf("gotRealIP = %q, want 192.0.2.10", gotRealIP)
	}
	if gotForwarded != "192.0.2.10" {
		t.Errorf("gotForwarded = %q, want 192.0.2.10", gotForwarded)
	}
}
