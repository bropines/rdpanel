package app

import (
	"net/http"
	"strings"
	"testing"
)

// TestAuthRoutesWired checks that stream B1 routes are mounted by the app.
func TestAuthRoutesWired(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_PUBLIC_URL": "https://desk.example.test"})
	h := a.Handler()
	if rec := do(t, h, http.MethodGet, "/api/login-options", "", nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("login-options: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/oidc/auth-query?code=x&id=1&uuid=u", "", nil); !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("auth-query: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/admin/auth/csrf", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "csrf_token") {
		t.Fatalf("csrf: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/admin/auth/me", "", nil); rec.Code != 401 {
		t.Fatalf("me: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, "/api/v1/admin/auth/login", "", nil); rec.Code != 403 {
		t.Fatalf("login without csrf: %d", rec.Code)
	}
	if a.TOTP == nil {
		t.Fatal("TOTP service not built")
	}
}
