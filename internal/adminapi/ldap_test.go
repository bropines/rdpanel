package adminapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	ldapv3 "github.com/go-ldap/ldap/v3"

	"github.com/bropines/rdpanel/internal/ldap"
	"github.com/bropines/rdpanel/internal/store"
)

type mockLDAPClient struct {
	bindFn   func(username, password string) error
	searchFn func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error)
	closeFn  func() error
}

func (m *mockLDAPClient) Bind(username, password string) error {
	if m.bindFn != nil {
		return m.bindFn(username, password)
	}
	return nil
}

func (m *mockLDAPClient) Search(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
	if m.searchFn != nil {
		return m.searchFn(req)
	}
	return &ldapv3.SearchResult{}, nil
}

func (m *mockLDAPClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func TestLDAPAdminAPI(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("ldap_admin", store.RoleAdmin)
	user, _ := e.as("ldap_user", store.RoleUser)

	// User cannot access LDAP settings (403 forbidden)
	code, _, _ := user.do("GET", "/settings/ldap", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role on GET /settings/ldap, got %d", code)
	}
	code, _, _ = user.do("PUT", "/settings/ldap", map[string]any{"enabled": true})
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role on PUT /settings/ldap, got %d", code)
	}

	// Admin GET initial LDAP settings
	code, m, _ := admin.do("GET", "/settings/ldap", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, m)
	}
	if m["enabled"] != false || m["server_url"] != "" {
		t.Fatalf("unexpected initial settings: %v", m)
	}

	// Update with invalid URL scheme -> 400
	code, _, _ = admin.do("PUT", "/settings/ldap", map[string]any{
		"server_url": "http://example.com",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for http scheme, got %d", code)
	}

	// Update with valid settings
	updateBody := map[string]any{
		"enabled":              true,
		"server_url":           "ldaps://ad.example.corp:636",
		"start_tls":            false,
		"insecure_skip_verify": true,
		"bind_dn":              "cn=admin,dc=example,dc=corp",
		"bind_password":        "SecretPassword123",
		"base_dn":              "dc=example,dc=corp",
		"user_search_base":     "ou=users,dc=example,dc=corp",
		"user_filter":          "(&(objectClass=user)(sAMAccountName=%s))",
		"auto_register":        true,
		"sync_interval_mins":   60,
	}
	code, updated, _ := admin.do("PUT", "/settings/ldap", updateBody)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on PUT /settings/ldap, got %d: %v", code, updated)
	}
	if updated["enabled"] != true || updated["server_url"] != "ldaps://ad.example.corp:636" {
		t.Fatalf("settings not updated: %v", updated)
	}
	if updated["has_bind_password"] != true {
		t.Fatalf("expected has_bind_password=true, got %v", updated["has_bind_password"])
	}

	// Configure mock dialer on e.deps.LDAP
	e.deps.LDAP.SetDialer(func(cfg *store.LDAPSettings, bindPassword string) (ldap.Client, error) {
		return &mockLDAPClient{
			bindFn: func(username, password string) error {
				if username == "cn=admin,dc=example,dc=corp" && password != "SecretPassword123" {
					return errors.New("admin bind failed")
				}
				if username == "uid=corpuser,ou=users,dc=example,dc=corp" {
					if password != "userpass" {
						return errors.New("user password invalid")
					}
					return nil
				}
				return nil
			},
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				if strings.Contains(req.Filter, "corpuser") {
					return &ldapv3.SearchResult{
						Entries: []*ldapv3.Entry{
							{
								DN: "uid=corpuser,ou=users,dc=example,dc=corp",
								Attributes: []*ldapv3.EntryAttribute{
									{Name: "sAMAccountName", Values: []string{"corpuser"}},
									{Name: "displayName", Values: []string{"Corporate User"}},
									{Name: "mail", Values: []string{"corpuser@example.corp"}},
								},
							},
						},
					}, nil
				}
				return &ldapv3.SearchResult{}, nil
			},
		}, nil
	})

	// Test connection endpoint
	code, testRes, _ := admin.do("POST", "/settings/ldap/test", map[string]any{
		"test_username": "corpuser",
		"test_password": "userpass",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 on /settings/ldap/test, got %d: %v", code, testRes)
	}
	if testRes["ok"] != true || testRes["connected"] != true || testRes["user_bound"] != true {
		t.Fatalf("test failed: %v", testRes)
	}

	// Trigger manual sync
	code, syncRes, _ := admin.do("POST", "/settings/ldap/sync", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on /settings/ldap/sync, got %d: %v", code, syncRes)
	}
	if syncRes["status"] != "success" {
		t.Fatalf("sync not successful: %v", syncRes)
	}

	// Login with corporate user via /auth/login
	loginClient := e.client()
	code, loginRes := loginClient.login("corpuser", "userpass")
	if code != http.StatusOK {
		t.Fatalf("corporate user login failed: %d %v", code, loginRes)
	}
	me, ok := loginRes["me"].(map[string]any)
	if !ok {
		t.Fatalf("missing me in login response: %v", loginRes)
	}
	userObj, ok := me["user"].(map[string]any)
	if !ok {
		t.Fatalf("missing user in me response: %v", me)
	}
	if userObj["username"] != "corpuser" || userObj["is_ldap"] != true {
		t.Fatalf("expected is_ldap=true, got %v", userObj)
	}
}

// enableTestLDAP turns LDAP on with a directory that knows every user in
// entries (username -> sAMAccountName) and accepts any non-empty password.
func enableTestLDAP(t *testing.T, e *b2env, entries map[string]string) {
	t.Helper()
	cfg, err := e.st.LDAPSettings.Get(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.ServerURL, cfg.BaseDN, cfg.AutoRegister = true, "ldap://ad.example.corp", "dc=example,dc=corp", true
	if err := e.st.LDAPSettings.Update(e.ctx, cfg); err != nil {
		t.Fatal(err)
	}
	e.deps.LDAP.SetDialer(func(*store.LDAPSettings, string) (ldap.Client, error) {
		return &mockLDAPClient{
			bindFn: func(_, password string) error {
				if password == "" {
					return errors.New("empty password")
				}
				return nil
			},
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				for typed, account := range entries {
					if strings.Contains(req.Filter, "="+typed+")") {
						return &ldapv3.SearchResult{Entries: []*ldapv3.Entry{{
							DN:         "uid=" + account + ",ou=users,dc=example,dc=corp",
							Attributes: []*ldapv3.EntryAttribute{{Name: "sAMAccountName", Values: []string{account}}},
						}}}, nil
					}
				}
				return &ldapv3.SearchResult{}, nil
			},
		}, nil
	})
}

func TestLDAPLoginCannotTakeOverLocalAccount(t *testing.T) {
	e := newB2Env(t, b2opts{})
	root := e.user("root", testPassword, store.RoleAdmin)
	// "root" exists in the directory too; "corp-root" maps to the
	// sAMAccountName "ROOT" (a different spelling of the local username).
	enableTestLDAP(t, e, map[string]string{"root": "root", "corp-root": "ROOT"})

	for _, name := range []string{"root", "corp-root"} {
		if code, m := e.client().login(name, "directory password"); code != http.StatusUnauthorized {
			t.Fatalf("login %s with the directory password: %d %v", name, code, m)
		}
	}
	got, err := e.st.Users.GetByID(e.ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsLDAP || got.LDAPDN != "" || got.Role != store.RoleAdmin {
		t.Fatalf("local admin linked to the directory: %+v", got)
	}
	// The local password keeps working.
	if code, m := e.client().login("root", testPassword); code != http.StatusOK {
		t.Fatalf("local login: %d %v", code, m)
	}
}

// A password verified by the directory must never be stored as a local hash
// (the "rehash on login" upgrade saw the empty hash of LDAP users as stale).
func TestLDAPLoginStoresNoLocalPassword(t *testing.T) {
	e := newB2Env(t, b2opts{})
	enableTestLDAP(t, e, map[string]string{"corpuser": "corpuser"})
	for range 2 { // provisioning login, then a login of the existing LDAP user
		if code, m := e.client().login("corpuser", "directory password"); code != http.StatusOK {
			t.Fatalf("ldap login: %d %v", code, m)
		}
		u, err := e.st.Users.GetByUsername(e.ctx, "corpuser")
		if err != nil {
			t.Fatal(err)
		}
		if !u.IsLDAP || u.HasPassword() || u.PasswordAlgo != "" {
			t.Fatalf("LDAP password stored locally: is_ldap=%v hash=%q", u.IsLDAP, u.PasswordHash)
		}
	}
}
