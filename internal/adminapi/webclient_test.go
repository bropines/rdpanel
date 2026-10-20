package adminapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func TestWebClientConfig(t *testing.T) {
	e := newB2Env(t, b2opts{})
	keyFile := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte("c2VydmVyLWtleQ==\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := e.deps.Config
	cfg.HBBS.PublicKeyFile = keyFile
	cfg.WebClient.RelayURL = "wss://relay.test/ws/relay"

	code, _, _ := e.client().do(http.MethodGet, "/webclient/config", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}

	user, bob := e.as("bob", store.RoleUser)
	code, m, _ := user.do(http.MethodGet, "/webclient/config", nil)
	if code != http.StatusOK {
		t.Fatalf("user: %d %v", code, m)
	}
	if m["enabled"] != true || m["public_key"] != "c2VydmVyLWtleQ==" || m["relay_url"] != "wss://relay.test/ws/relay" ||
		m["rendezvous_url"] != "" || m["display_name"] != "bob" {
		t.Fatalf("config: %v", m)
	}
	tok, _ := m["token"].(string)
	claims, err := e.deps.JWT.Verify(tok)
	if err != nil || int64(claims.UserID) != bob.ID {
		t.Fatalf("token: %v %+v", err, claims)
	}
	exp := claims.ExpiresAt.Time.Sub(e.st.Now())
	if exp <= 0 || exp > webClientTokenLifetime {
		t.Fatalf("token lifetime %v", exp)
	}

	cfg.WebClient.AdminOnly = true
	_, m, _ = user.do(http.MethodGet, "/webclient/config", nil)
	if m["enabled"] != false || m["token"] != nil || m["public_key"] != "" {
		t.Fatalf("admin-only, user: %v", m)
	}
	admin, _ := e.as("root", store.RoleAdmin)
	if _, m, _ = admin.do(http.MethodGet, "/webclient/config", nil); m["enabled"] != true {
		t.Fatalf("admin-only, admin: %v", m)
	}

	cfg.WebClient.Enabled = false
	if _, m, _ = admin.do(http.MethodGet, "/webclient/config", nil); m["enabled"] != false {
		t.Fatalf("disabled: %v", m)
	}

}
