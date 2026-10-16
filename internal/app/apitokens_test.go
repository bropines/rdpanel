package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/store"
)

// TestAPITokenSurfaces: personal API tokens work on the admin API and the
// SSE stream, never on the RustDesk client API.
func TestAPITokenSurfaces(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	ctx := context.Background()
	u := &store.User{Username: "ci", Role: store.RoleAdmin}
	if err := a.Store.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	tok, err := auth.NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.APITokens.Create(ctx, &store.APIToken{UserID: u.ID, Name: "ci", Prefix: tok.Plain[:10],
		TokenSHA256: tok.Hash, Scope: store.APITokenRead}); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	bearer := map[string]string{"Authorization": "Bearer " + tok.Plain}

	if rec := do(t, h, http.MethodGet, "/api/v1/admin/devices", "127.0.0.1:1", bearer); rec.Code != http.StatusOK {
		t.Fatalf("admin API: %d %s", rec.Code, rec.Body.String())
	}
	// Client API: the token is not a client token.
	for _, p := range []struct{ method, path string }{
		{http.MethodPost, "/api/currentUser"}, {http.MethodGet, "/api/users"}, {http.MethodGet, "/api/peers"},
	} {
		if rec := do(t, h, p.method, p.path, "127.0.0.1:1", bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("client API %s %s: %d %s", p.method, p.path, rec.Code, rec.Body.String())
		}
	}

	// SSE stream.
	srv := httptest.NewServer(h)
	defer srv.Close()
	sse := func(token string) int {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := sse(tok.Plain); code != http.StatusOK {
		t.Fatalf("events with token: %d", code)
	}
	other, _ := auth.NewAPIToken()
	if code := sse(other.Plain); code != http.StatusUnauthorized {
		t.Fatalf("events with unknown token: %d", code)
	}
}
