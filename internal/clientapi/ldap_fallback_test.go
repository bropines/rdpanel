package clientapi

import (
	"context"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

// fakeLDAP records calls and accepts every password as user (a directory that
// would hand out, or link, whatever account it is asked about).
type fakeLDAP struct {
	calls int
	user  *store.User
}

func (f *fakeLDAP) Authenticate(_ context.Context, _, _ string) (*store.User, error) {
	f.calls++
	return f.user, nil
}

func TestLoginLDAPFallbackOnlyForLDAPUsers(t *testing.T) {
	fl := &fakeLDAP{}
	e := newEnv(t, func(d *Deps) { d.LDAP = fl })
	local := e.user("alice", "correct horse", nil)
	oidcOnly := e.user("bob", "", nil)
	ldapUser := e.user("carol", "", func(u *store.User) { u.IsLDAP, u.LDAPDN = true, "uid=carol,dc=example,dc=com" })

	// A local account with a wrong password never reaches the directory.
	fl.user = local
	e.mustDo("POST", "/api/login", loginBody("alice", "directory password"), "", 400)
	// Nor does a local account without a password (e.g. OIDC-only).
	fl.user = oidcOnly
	e.mustDo("POST", "/api/login", loginBody("bob", "directory password"), "", 400)
	if fl.calls != 0 {
		t.Fatalf("LDAP asked %d times about local accounts", fl.calls)
	}

	// LDAP-provisioned users and unknown usernames still use the directory.
	fl.user = ldapUser
	e.mustDo("POST", "/api/login", loginBody("carol", "directory password"), "", 200)
	if fl.calls != 1 {
		t.Fatalf("LDAP calls for an LDAP user: %d", fl.calls)
	}
	e.mustDo("POST", "/api/login", loginBody("dave", "directory password"), "", 200)
	if fl.calls != 2 {
		t.Fatalf("LDAP calls for an unknown user: %d", fl.calls)
	}
}
