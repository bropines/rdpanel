package adminapi

import (
	"net/http"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func TestWebClientShells(t *testing.T) {
	e := newB2Env(t, b2opts{})
	if code, _, _ := e.client().do(http.MethodGet, "/webclient/shells/123", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	bob, _ := e.as("bob", store.RoleUser)
	eve, _ := e.as("eve", store.RoleUser)

	if code, m, _ := bob.do(http.MethodPut, "/webclient/shells/123", map[string]any{"service_id": "svc-1", "hostname": "pc", "tabs": 2}); code != http.StatusNoContent {
		t.Fatalf("save: %d %v", code, m)
	}
	if code, m, _ := bob.do(http.MethodPut, "/webclient/shells/123", map[string]any{"service_id": "bad id", "tabs": 1}); code != http.StatusBadRequest {
		t.Fatalf("invalid id: %d %v", code, m)
	}
	code, m, _ := bob.do(http.MethodGet, "/webclient/shells/123", nil)
	items, _ := m["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["service_id"] != "svc-1" || items[0].(map[string]any)["tabs"] != float64(2) {
		t.Fatalf("list: %d %v", code, m)
	}
	// private to the caller
	if _, m, _ := eve.do(http.MethodGet, "/webclient/shells/123", nil); len(m["items"].([]any)) != 0 {
		t.Fatalf("eve sees bob's shells: %v", m)
	}
	if code, _, _ := bob.do(http.MethodPost, "/webclient/shells/123/forget", map[string]any{"service_id": "svc-1"}); code != http.StatusNoContent {
		t.Fatalf("forget: %d", code)
	}
	if _, m, _ := bob.do(http.MethodGet, "/webclient/shells/123", nil); len(m["items"].([]any)) != 0 {
		t.Fatalf("after forget: %v", m)
	}

	e.deps.Config.WebClient.Enabled = false
	if code, _, _ := bob.do(http.MethodGet, "/webclient/shells/123", nil); code != http.StatusNotFound {
		t.Fatalf("web client off: %d", code)
	}
}
