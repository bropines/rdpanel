package adminapi

import (
	"context"
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

func TestWebClientCredentials(t *testing.T) {
	e := newB2Env(t, b2opts{})
	ctx := context.Background()
	books := e.st.AddressBooks
	bobC, bob := e.as("bob", store.RoleUser)
	carolC, carol := e.as("carol", store.RoleUser)
	str := func(s string) *string { return &s }

	// nothing saved
	_, m, _ := bobC.do(http.MethodGet, "/webclient/credentials/123456789", nil)
	if m["hash"] != nil || m["password"] != nil {
		t.Fatalf("empty: %v", m)
	}

	// personal book: remembered hash
	personal, err := books.EnsurePersonal(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := books.UpsertEntry(ctx, personal.ID, "123456789", store.ABEntryPatch{Hash: str("aGFzaA==")}); err != nil {
		t.Fatal(err)
	}
	_, m, _ = bobC.do(http.MethodGet, "/webclient/credentials/123%20456%20789", nil)
	if m["hash"] != "aGFzaA==" || m["password"] != nil {
		t.Fatalf("personal: %v", m)
	}

	// shared book with a default password, shared with carol only
	shared := &store.AddressBook{OwnerUserID: bob.ID, Name: "Office", DefaultPassword: "office-default"}
	if err := books.CreateShared(ctx, shared); err != nil {
		t.Fatal(err)
	}
	if _, err := books.UpsertEntry(ctx, shared.ID, "987654321", store.ABEntryPatch{}); err != nil {
		t.Fatal(err)
	}
	if _, m, _ = carolC.do(http.MethodGet, "/webclient/credentials/987654321", nil); m["password"] != nil {
		t.Fatalf("not shared yet, got %v", m)
	}
	if _, err := books.CreateShare(ctx, shared.ID, &carol.ID, nil, store.ABRead); err != nil {
		t.Fatal(err)
	}
	if _, m, _ = carolC.do(http.MethodGet, "/webclient/credentials/987654321", nil); m["password"] != "office-default" || m["book"] != "Office" {
		t.Fatalf("shared default password: %v", m)
	}
	if _, err := books.UpsertEntry(ctx, shared.ID, "987654321", store.ABEntryPatch{Password: str("entry-pw")}); err != nil {
		t.Fatal(err)
	}
	if _, m, _ = carolC.do(http.MethodGet, "/webclient/credentials/987654321", nil); m["password"] != "entry-pw" {
		t.Fatalf("shared entry password: %v", m)
	}
	// carol never sees bob's personal book
	if _, m, _ = carolC.do(http.MethodGet, "/webclient/credentials/123456789", nil); m["hash"] != nil {
		t.Fatalf("personal leaked: %v", m)
	}
	if code, _, _ := e.client().do(http.MethodGet, "/webclient/credentials/123456789", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestWebClientRememberInAccount(t *testing.T) {
	e := newB2Env(t, b2opts{})
	ctx := context.Background()
	bobC, bob := e.as("bob", store.RoleUser)
	hash := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // 32 bytes

	if code, _, _ := bobC.do(http.MethodPut, "/webclient/credentials/123456789", map[string]any{"hash": "c2hvcnQ="}); code != http.StatusBadRequest {
		t.Fatalf("short hash: %d", code)
	}
	if code, _, _ := bobC.do(http.MethodPut, "/webclient/credentials/123456789", map[string]any{"hash": hash}); code != http.StatusNoContent {
		t.Fatalf("remember: %d", code)
	}
	// stored in the personal book, where desktop clients read it too
	book, err := e.st.AddressBooks.GetPersonal(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if en, err := e.st.AddressBooks.GetEntry(ctx, book.ID, "123456789"); err != nil || en.Hash != hash {
		t.Fatalf("entry: %+v %v", en, err)
	}
	if _, m, _ := bobC.do(http.MethodGet, "/webclient/credentials/123456789", nil); m["hash"] != hash {
		t.Fatalf("read back: %v", m)
	}
	if code, _, _ := bobC.do(http.MethodDelete, "/webclient/credentials/123456789", nil); code != http.StatusNoContent {
		t.Fatalf("forget: %d", code)
	}
	if _, m, _ := bobC.do(http.MethodGet, "/webclient/credentials/123456789", nil); m["hash"] != nil {
		t.Fatalf("after forget: %v", m)
	}
}
