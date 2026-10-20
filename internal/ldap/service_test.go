package ldap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	ldapv3 "github.com/go-ldap/ldap/v3"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

type mockClient struct {
	bindFn   func(username, password string) error
	searchFn func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error)
	closeFn  func() error
}

func (m *mockClient) Bind(username, password string) error {
	if m.bindFn != nil {
		return m.bindFn(username, password)
	}
	return nil
}

func (m *mockClient) Search(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
	if m.searchFn != nil {
		return m.searchFn(req)
	}
	return &ldapv3.SearchResult{}, nil
}

func (m *mockClient) Close() error {
	if m.closeFn != nil {
		return m.closeFn()
	}
	return nil
}

func setupTestService(t *testing.T) (*Service, *store.Store, *crypto.AEAD) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.RandomBytes(32)
	aead, err := crypto.NewAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(d, store.WithSecrets(aead))
	svc := NewService(st, aead, nil)
	return svc, st, aead
}

func TestHelpers(t *testing.T) {
	// extractCN
	if cn := extractCN("CN=Engineers,OU=Groups,DC=example,DC=com"); cn != "Engineers" {
		t.Errorf("extractCN: got %q, want 'Engineers'", cn)
	}
	if cn := extractCN("cn=admins"); cn != "admins" {
		t.Errorf("extractCN: got %q, want 'admins'", cn)
	}
	if cn := extractCN("ou=Users,dc=example,dc=com"); cn != "" {
		t.Errorf("extractCN on OU: got %q, want empty", cn)
	}

	// resolveGroupID
	mappings := map[string]int64{
		"CN=Admins,OU=Groups,DC=example,DC=com": 10,
		"DevOps":                                20,
	}
	var defID int64 = 5

	// Match full DN
	id1 := resolveGroupID([]string{"CN=Admins,OU=Groups,DC=example,DC=com"}, mappings, &defID)
	if id1 == nil || *id1 != 10 {
		t.Errorf("resolveGroupID full DN: got %v, want 10", id1)
	}

	// Match CN
	id2 := resolveGroupID([]string{"CN=DevOps,OU=Groups,DC=corp"}, mappings, &defID)
	if id2 == nil || *id2 != 20 {
		t.Errorf("resolveGroupID CN: got %v, want 20", id2)
	}

	// Fallback to default
	id3 := resolveGroupID([]string{"CN=OtherGroup"}, mappings, &defID)
	if id3 == nil || *id3 != 5 {
		t.Errorf("resolveGroupID default fallback: got %v, want 5", id3)
	}

	// Filter formatters
	uf := formatUserFilter("(&(objectClass=user)(sAMAccountName=%s))", "john.doe")
	if uf != "(&(objectClass=user)(sAMAccountName=john.doe))" {
		t.Errorf("formatUserFilter: got %q", uf)
	}
	sf := formatSyncFilter("(&(objectClass=user)(sAMAccountName=%s))")
	if sf != "(&(objectClass=user)(sAMAccountName=*))" {
		t.Errorf("formatSyncFilter: got %q", sf)
	}
}

func TestAuthenticate(t *testing.T) {
	svc, st, _ := setupTestService(t)
	ctx := context.Background()

	// 1. When disabled
	_, err := svc.Authenticate(ctx, "alice", "secret")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}

	// Configure and enable LDAP
	grp := &store.UserGroup{Name: "Developers"}
	if err := st.UserGroups.Create(ctx, grp); err != nil {
		t.Fatal(err)
	}

	cfg, err := st.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.ServerURL = "ldap://127.0.0.1:389"
	cfg.BaseDN = "dc=example,dc=com"
	cfg.GroupMappings = map[string]int64{
		"Developers": grp.ID,
	}
	rawPw := "adminpass"
	if err := svc.UpdateSettings(ctx, cfg, &rawPw); err != nil {
		t.Fatal(err)
	}

	// Setup mock LDAP dialer
	svc.SetDialer(func(cfg *store.LDAPSettings, bindPassword string) (Client, error) {
		return &mockClient{
			bindFn: func(username, password string) error {
				if username == "cn=admin,dc=example,dc=com" && password != "adminpass" {
					return errors.New("admin bind failed")
				}
				if username == "uid=alice,ou=users,dc=example,dc=com" {
					if password != "correctpass" {
						return errors.New("invalid credentials")
					}
					return nil
				}
				return nil
			},
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				if strings.Contains(req.Filter, "alice") {
					return &ldapv3.SearchResult{
						Entries: []*ldapv3.Entry{
							{
								DN: "uid=alice,ou=users,dc=example,dc=com",
								Attributes: []*ldapv3.EntryAttribute{
									{Name: "sAMAccountName", Values: []string{"alice"}},
									{Name: "displayName", Values: []string{"Alice Wonderland"}},
									{Name: "mail", Values: []string{"alice@example.com"}},
									{Name: "memberOf", Values: []string{"CN=Developers,OU=Groups,DC=example,DC=com"}},
								},
							},
						},
					}, nil
				}
				return &ldapv3.SearchResult{}, nil
			},
		}, nil
	})

	// 2. Invalid password
	_, err = svc.Authenticate(ctx, "alice", "wrongpass")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	// 3. User not found
	_, err = svc.Authenticate(ctx, "bob", "anypass")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}

	// 4. Success authentication & auto-provision
	u, err := svc.Authenticate(ctx, "alice", "correctpass")
	if err != nil {
		t.Fatalf("Authenticate success failed: %v", err)
	}
	if u.Username != "alice" || u.DisplayName != "Alice Wonderland" || !u.IsLDAP {
		t.Fatalf("unexpected user: %+v", u)
	}
	if u.UserGroupID == nil || *u.UserGroupID != grp.ID {
		t.Fatalf("user group not mapped: %+v", u.UserGroupID)
	}

	// 5. Subsequent authentication updates existing user
	u2, err := svc.Authenticate(ctx, "alice", "correctpass")
	if err != nil {
		t.Fatalf("re-authentication failed: %v", err)
	}
	if u2.ID != u.ID {
		t.Fatalf("expected same user ID %d, got %d", u.ID, u2.ID)
	}
}

func TestTestEndpoint(t *testing.T) {
	svc, st, _ := setupTestService(t)
	ctx := context.Background()

	cfg, _ := st.LDAPSettings.Get(ctx)
	cfg.ServerURL = "ldap://127.0.0.1:389"

	svc.SetDialer(func(cfg *store.LDAPSettings, bindPassword string) (Client, error) {
		return &mockClient{
			bindFn: func(username, password string) error {
				if username == "uid=testuser,dc=example,dc=com" && password != "testpass" {
					return errors.New("bad password")
				}
				return nil
			},
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				if strings.Contains(req.Filter, "testuser") {
					return &ldapv3.SearchResult{
						Entries: []*ldapv3.Entry{
							{
								DN: "uid=testuser,dc=example,dc=com",
								Attributes: []*ldapv3.EntryAttribute{
									{Name: "sAMAccountName", Values: []string{"testuser"}},
									{Name: "displayName", Values: []string{"Test User"}},
								},
							},
						},
					}, nil
				}
				return &ldapv3.SearchResult{}, nil
			},
		}, nil
	})

	// Test connection only
	res1, err := svc.Test(ctx, TestRequest{
		Settings: cfg,
	})
	if err != nil || !res1.OK || !res1.Connected {
		t.Fatalf("test connection failed: %+v, err: %v", res1, err)
	}

	// Test user with valid pass
	res2, err := svc.Test(ctx, TestRequest{
		Settings:     cfg,
		TestUsername: "testuser",
		TestPassword: "testpass",
	})
	if err != nil || !res2.OK || !res2.UserFound || !res2.UserBound {
		t.Fatalf("test user valid pass failed: %+v, err: %v", res2, err)
	}

	// Test user with invalid pass
	res3, err := svc.Test(ctx, TestRequest{
		Settings:     cfg,
		TestUsername: "testuser",
		TestPassword: "wrongpassword",
	})
	if err != nil || res3.OK || !res3.UserFound || res3.UserBound {
		t.Fatalf("test user wrong pass expected failure: %+v", res3)
	}
}

func TestSync(t *testing.T) {
	svc, st, _ := setupTestService(t)
	ctx := context.Background()

	cfg, _ := st.LDAPSettings.Get(ctx)
	cfg.Enabled = true
	cfg.ServerURL = "ldap://127.0.0.1:389"
	_ = st.LDAPSettings.Update(ctx, cfg)

	svc.SetDialer(func(cfg *store.LDAPSettings, bindPassword string) (Client, error) {
		return &mockClient{
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				return &ldapv3.SearchResult{
					Entries: []*ldapv3.Entry{
						{
							DN: "uid=u1,dc=example,dc=com",
							Attributes: []*ldapv3.EntryAttribute{
								{Name: "sAMAccountName", Values: []string{"u1"}},
								{Name: "displayName", Values: []string{"User One"}},
								{Name: "mail", Values: []string{"u1@example.com"}},
							},
						},
						{
							DN: "uid=u2,dc=example,dc=com",
							Attributes: []*ldapv3.EntryAttribute{
								{Name: "sAMAccountName", Values: []string{"u2"}},
								{Name: "displayName", Values: []string{"User Two"}},
								{Name: "mail", Values: []string{"u2@example.com"}},
							},
						},
					},
				}, nil
			},
		}, nil
	})

	res, err := svc.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if res.SyncedCount != 2 || res.Status != "success" {
		t.Fatalf("unexpected sync result: %+v", res)
	}

	// Verify users exist in store
	u1, err := st.Users.GetByUsername(ctx, "u1")
	if err != nil || !u1.IsLDAP || u1.DisplayName != "User One" {
		t.Fatalf("u1 not synced properly: %+v, err: %v", u1, err)
	}
	u2, err := st.Users.GetByUsername(ctx, "u2")
	if err != nil || !u2.IsLDAP || u2.DisplayName != "User Two" {
		t.Fatalf("u2 not synced properly: %+v, err: %v", u2, err)
	}

	// Verify sync result in settings
	updatedCfg, _ := st.LDAPSettings.Get(ctx)
	if updatedCfg.LastSyncCount != 2 || updatedCfg.LastSyncStatus != "success" || updatedCfg.LastSyncAt == nil {
		t.Fatalf("settings sync result not updated: %+v", updatedCfg)
	}
}

// With a service account (BindDN) configured, the user's own bind must run on
// a connection without the service bind: the dialer, like defaultDialer,
// binds BindDN when it is set and go-ldap refuses empty-password binds.
func TestAuthenticateWithServiceAccount(t *testing.T) {
	svc, st, _ := setupTestService(t)
	ctx := context.Background()
	const (
		svcDN   = "cn=svc,dc=example,dc=com"
		aliceDN = "uid=alice,ou=users,dc=example,dc=com"
	)
	cfg, err := st.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.ServerURL, cfg.BaseDN, cfg.BindDN, cfg.AutoRegister = true, "ldap://127.0.0.1:389", "dc=example,dc=com", svcDN, true
	svcPass := "svc-pass"
	if err := svc.UpdateSettings(ctx, cfg, &svcPass); err != nil {
		t.Fatal(err)
	}

	var conns [][]string // binds per connection
	svc.SetDialer(func(cfg *store.LDAPSettings, bindPassword string) (Client, error) {
		idx := len(conns)
		conns = append(conns, nil)
		bind := func(dn, password string) error {
			conns[idx] = append(conns[idx], dn)
			if password == "" {
				return errors.New("ldap: empty password not allowed by the client")
			}
			if (dn == svcDN && password == "svc-pass") || (dn == aliceDN && password == "alice-pass") {
				return nil
			}
			return errors.New("invalid credentials")
		}
		if cfg.BindDN != "" { // what defaultDialer does
			if err := bind(cfg.BindDN, bindPassword); err != nil {
				return nil, fmt.Errorf("ldap bind failed: %w", err)
			}
		}
		return &mockClient{
			bindFn: bind,
			searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
				if len(conns[idx]) == 0 || conns[idx][0] != svcDN {
					return nil, errors.New("insufficient access: anonymous search")
				}
				return &ldapv3.SearchResult{Entries: []*ldapv3.Entry{{DN: aliceDN, Attributes: []*ldapv3.EntryAttribute{
					{Name: "sAMAccountName", Values: []string{"alice"}}}}}}, nil
			},
		}, nil
	})

	u, err := svc.Authenticate(ctx, "alice", "alice-pass")
	if err != nil || u.Username != "alice" || !u.IsLDAP {
		t.Fatalf("authenticate with a service account: %+v %v", u, err)
	}
	if len(conns) != 2 || len(conns[0]) != 1 || conns[0][0] != svcDN || len(conns[1]) != 1 || conns[1][0] != aliceDN {
		t.Fatalf("binds per connection: %v (want [[service] [user]])", conns)
	}
	if _, err := svc.Authenticate(ctx, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}

	// The settings test does the same user bind.
	res, err := svc.Test(ctx, TestRequest{TestUsername: "alice", TestPassword: "alice-pass"})
	if err != nil || !res.OK || !res.UserBound {
		t.Fatalf("test with a service account: %+v %v", res, err)
	}
}

// directory is a fake LDAP server: entries by sAMAccountName, user passwords
// by DN. It accepts every bind that has the right password.
type directory struct {
	entries   map[string]*ldapv3.Entry
	passwords map[string]string
}

func newDirectory() *directory {
	return &directory{entries: map[string]*ldapv3.Entry{}, passwords: map[string]string{}}
}

func (d *directory) add(username, dn, password string) {
	d.entries[strings.ToLower(username)] = &ldapv3.Entry{DN: dn, Attributes: []*ldapv3.EntryAttribute{
		{Name: "sAMAccountName", Values: []string{username}},
		{Name: "displayName", Values: []string{"Directory " + username}},
	}}
	d.passwords[dn] = password
}

func (d *directory) dialer(*store.LDAPSettings, string) (Client, error) {
	return &mockClient{
		bindFn: func(dn, password string) error {
			if want, ok := d.passwords[dn]; ok && want != password {
				return errors.New("invalid credentials")
			}
			return nil
		},
		searchFn: func(req *ldapv3.SearchRequest) (*ldapv3.SearchResult, error) {
			res := &ldapv3.SearchResult{}
			for name, e := range d.entries {
				if strings.Contains(req.Filter, "=*)") || strings.Contains(strings.ToLower(req.Filter), "="+name+")") {
					res.Entries = append(res.Entries, e)
				}
			}
			return res, nil
		},
	}, nil
}

func enableLDAP(t *testing.T, svc *Service, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	cfg, err := st.LDAPSettings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled, cfg.ServerURL, cfg.BaseDN, cfg.AutoRegister = true, "ldap://127.0.0.1:389", "dc=example,dc=com", true
	if err := st.LDAPSettings.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

// A directory entry whose username matches a local (non-LDAP) account must
// never authenticate as, or be linked to, that account.
func TestAuthenticateNeverLinksLocalAccount(t *testing.T) {
	svc, st, _ := setupTestService(t)
	ctx := context.Background()
	enableLDAP(t, svc, st)
	dir := newDirectory()
	svc.SetDialer(dir.dialer)

	admin := &store.User{Username: "admin", Role: store.RoleAdmin, PasswordHash: "x", PasswordAlgo: "argon2id"}
	if err := st.Users.Create(ctx, admin); err != nil {
		t.Fatal(err)
	}
	dir.add("Admin", "uid=admin,ou=users,dc=example,dc=com", "directory-pass")

	if _, err := svc.Authenticate(ctx, "admin", "directory-pass"); !errors.Is(err, ErrLocalAccount) {
		t.Fatalf("login as local admin through LDAP: %v", err)
	}
	got, err := st.Users.GetByID(ctx, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsLDAP || got.LDAPDN != "" || got.DisplayName != "" || got.Role != store.RoleAdmin {
		t.Fatalf("local admin was modified: %+v", got)
	}

	// A sync skips the entry and leaves the account alone too.
	dir.add("eve", "uid=eve,ou=users,dc=example,dc=com", "eve-pass")
	res, err := svc.Sync(ctx)
	if err != nil || res.Status != "success" || res.SyncedCount != 1 {
		t.Fatalf("sync: %+v %v", res, err)
	}
	if got, _ := st.Users.GetByID(ctx, admin.ID); got.IsLDAP || got.LDAPDN != "" {
		t.Fatalf("sync linked the local admin: %+v", got)
	}
	if eve, err := st.Users.GetByUsername(ctx, "eve"); err != nil || !eve.IsLDAP {
		t.Fatalf("eve not provisioned: %+v %v", eve, err)
	}

	// An LDAP user that moved in the directory (new DN) is still matched by
	// username and relinked.
	dir.add("eve", "uid=eve,ou=moved,dc=example,dc=com", "eve-pass")
	u, err := svc.Authenticate(ctx, "eve", "eve-pass")
	if err != nil || u.LDAPDN != "uid=eve,ou=moved,dc=example,dc=com" {
		t.Fatalf("relink moved LDAP user: %+v %v", u, err)
	}
}
