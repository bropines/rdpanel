package authz

import (
	"context"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
	"github.com/bropines/rdpanel/internal/store"
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// Device ids of the fixture.
const (
	devOffice  = "100000001" // in device group "office"
	devOwned   = "100000002" // owned by dave
	devServer  = "100000003" // no group, no owner
	devUnknown = "199999999" // not registered with the panel
)

// Permission bitmaps of the seeded roles.
const (
	permsFull     = "0"
	permsViewOnly = "86097" // keyboard, clipboard, file, terminal, tunnel, restart off
	permsSupport  = "20544" // file, terminal, tunnel off
)

type fixture struct {
	st     *store.Store
	svc    *Service
	users  map[string]*store.User
	tokens map[string]string
	roles  map[string]*store.ControlRole
	groups map[string]int64
	devs   map[string]*store.Device
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st := store.New(dbtest.Open(t)).WithClock(func() time.Time { return testNow })
	jwt, err := auth.NewClientJWT([]byte("authz-test-jwt-key-0123456789"), func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, svc: &Service{Store: st, JWT: jwt}, users: map[string]*store.User{},
		tokens: map[string]string{}, roles: map[string]*store.ControlRole{}, groups: map[string]int64{},
		devs: map[string]*store.Device{}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	support := &store.UserGroup{Name: "support"}
	must(st.UserGroups.Create(ctx, support))
	f.groups["support"] = support.ID
	office := &store.DeviceGroup{Name: "office"}
	must(st.DeviceGroups.Create(ctx, office))
	f.groups["office"] = office.ID

	for _, u := range []*store.User{
		{Username: "admin", Role: store.RoleAdmin},
		{Username: "alice"},
		{Username: "bob", UserGroupID: &support.ID},
		{Username: "carol", Status: store.UserDisabled},
		{Username: "dave"},
		{Username: "eve", Role: store.RoleAdmin, Status: store.UserDisabled},
	} {
		must(st.Users.Create(ctx, u))
		f.users[u.Username] = u
		tok, err := jwt.Mint(uint32(u.ID), testNow.Add(time.Hour))
		must(err)
		f.tokens[u.Username] = tok
	}
	for _, d := range []*store.Device{
		{RustdeskID: devOffice, DeviceGroupID: &office.ID},
		{RustdeskID: devOwned, OwnerUserID: &f.users["dave"].ID},
		{RustdeskID: devServer},
	} {
		must(st.Devices.Create(ctx, d))
		f.devs[d.RustdeskID] = d
	}
	roles, err := st.ControlRoles.List(ctx)
	must(err)
	for _, r := range roles {
		f.roles[r.Name] = r
	}
	if f.roles["Full Control"] == nil || !f.roles["Full Control"].IsDefault || f.roles["View Only"] == nil || f.roles["Support (No Files)"] == nil {
		t.Fatalf("seeded roles changed: %v", roles)
	}

	// bob's desktop token is recorded (as /api/login does); a second, revoked
	// one must not identify him.
	bob := f.users["bob"].ID
	must(st.AuthTokens.Create(ctx, &store.AuthToken{UserID: bob, Kind: store.TokenClient,
		TokenSHA256: crypto.HashToken(f.tokens["bob"]), ExpiresAt: testNow.Add(time.Hour)}))
	revoked, err := jwt.Mint(uint32(bob), testNow.Add(2*time.Hour))
	must(err)
	revokedAt := testNow.Add(-time.Minute)
	must(st.AuthTokens.Create(ctx, &store.AuthToken{UserID: bob, Kind: store.TokenClient,
		TokenSHA256: crypto.HashToken(revoked), ExpiresAt: testNow.Add(time.Hour), RevokedAt: &revokedAt}))
	f.tokens["bob-revoked"] = revoked
	// The web client gets an unrecorded 10-minute token.
	web, err := jwt.Mint(uint32(bob), testNow.Add(10*time.Minute))
	must(err)
	f.tokens["bob-web"] = web
	expired, err := jwt.Mint(uint32(f.users["alice"].ID), testNow.Add(-time.Minute))
	must(err)
	f.tokens["alice-expired"] = expired
	other, _ := auth.NewClientJWT([]byte("some-other-panel-key-987654321"), func() time.Time { return testNow })
	foreign, err := other.Mint(uint32(f.users["admin"].ID), testNow.Add(time.Hour))
	must(err)
	f.tokens["admin-foreign"] = foreign
	f.tokens["garbage"] = "not.a.jwt"
	return f
}

type authzCase struct {
	name   string
	token  string // fixture token name, "" = anonymous
	target string
	conn   ConnType

	allow  bool
	perms  string
	reason string
	cause  string
	user   string // expected identified user ("" = anonymous)
	rule   string
	role   string
}

func (f *fixture) run(t *testing.T, cases []authzCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok := ""
			if c.token != "" {
				var ok bool
				if tok, ok = f.tokens[c.token]; !ok {
					t.Fatalf("no token %q", c.token)
				}
			}
			d, err := f.svc.Authorize(context.Background(), Request{TargetID: c.target, Token: tok, ConnType: c.conn})
			if err != nil {
				t.Fatal(err)
			}
			perms := c.perms
			if perms == "" {
				perms = "0"
			}
			var uid int64
			if c.user != "" {
				uid = f.users[c.user].ID
			}
			if d.Allow != c.allow || d.Permissions.String() != perms || d.Reason != c.reason || d.Cause != c.cause ||
				d.UserID != uid || d.Rule != c.rule || d.Role != c.role {
				t.Fatalf("got  %+v (permissions %s)\nwant allow=%v perms=%s reason=%q cause=%s user=%d rule=%q role=%q",
					d, d.Permissions, c.allow, perms, c.reason, c.cause, uid, c.rule, c.role)
			}
		})
	}
}

func TestAuthorize(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	t.Run("no rules", func(t *testing.T) {
		f.run(t, []authzCase{
			{name: "unknown device", target: devUnknown, allow: true, cause: CauseUnknownDevice},
			{name: "unknown device with token", token: "alice", target: devUnknown, allow: true, cause: CauseUnknownDevice},
			{name: "anonymous", target: devOffice, allow: true, cause: CauseNoRules, role: "Full Control"},
			{name: "anonymous file transfer", target: devOffice, conn: ConnFileTransfer, allow: true, cause: CauseNoRules, role: "Full Control"},
			{name: "user", token: "alice", target: devOffice, allow: true, cause: CauseNoRules, user: "alice", role: "Full Control"},
			{name: "owner", token: "dave", target: devOwned, allow: true, cause: CauseOwner, user: "dave", role: "Full Control"},
			{name: "admin", token: "admin", target: devServer, allow: true, cause: CauseAdmin, user: "admin"},
			{name: "disabled user is anonymous", token: "carol", target: devOffice, allow: true, cause: CauseNoRules, role: "Full Control"},
			{name: "garbage token is anonymous", token: "garbage", target: devOffice, allow: true, cause: CauseNoRules, role: "Full Control"},
		})
	})

	// A restrictive default role applies to everyone but admins, also
	// without rules.
	if err := f.st.ControlRoles.SetDefault(ctx, f.roles["View Only"].ID); err != nil {
		t.Fatal(err)
	}
	t.Run("restrictive default role", func(t *testing.T) {
		const fileReason = "File transfer is not allowed by control role View Only"
		const tunnelReason = "TCP tunneling is not allowed by control role View Only"
		f.run(t, []authzCase{
			{name: "anonymous", target: devOffice, allow: true, perms: permsViewOnly, cause: CauseNoRules, role: "View Only"},
			{name: "anonymous file transfer", target: devOffice, conn: ConnFileTransfer, reason: fileReason, cause: CauseConnType, role: "View Only"},
			{name: "user view camera", token: "alice", target: devOffice, conn: ConnViewCamera, allow: true, perms: permsViewOnly, cause: CauseNoRules, user: "alice", role: "View Only"},
			{name: "user file transfer", token: "alice", target: devOffice, conn: ConnFileTransfer, reason: fileReason, cause: CauseConnType, user: "alice", role: "View Only"},
			{name: "user port forward", token: "alice", target: devOffice, conn: ConnPortForward, reason: tunnelReason, cause: CauseConnType, user: "alice", role: "View Only"},
			{name: "user rdp", token: "alice", target: devOffice, conn: ConnRDP, reason: tunnelReason, cause: CauseConnType, user: "alice", role: "View Only"},
			{name: "user terminal", token: "alice", target: devOffice, conn: ConnTerminal, reason: "Terminal is not allowed by control role View Only", cause: CauseConnType, user: "alice", role: "View Only"},
			{name: "owner", token: "dave", target: devOwned, allow: true, perms: permsViewOnly, cause: CauseOwner, user: "dave", role: "View Only"},
			{name: "admin file transfer", token: "admin", target: devOffice, conn: ConnFileTransfer, allow: true, cause: CauseAdmin, user: "admin"},
			{name: "disabled admin is anonymous", token: "eve", target: devOffice, conn: ConnFileTransfer, reason: fileReason, cause: CauseConnType, role: "View Only"},
		})
	})
	if err := f.st.ControlRoles.SetDefault(ctx, f.roles["Full Control"].ID); err != nil {
		t.Fatal(err)
	}

	for _, r := range []*store.ConnectionACL{
		{Name: "disabled allow-all", Action: store.ACLActionAllow, Priority: 1, Enabled: false},
		{Name: "support office", Action: store.ACLActionAllow, Priority: 10, Enabled: true,
			UserGroupID: ptr(f.groups["support"]), DeviceGroupID: ptr(f.groups["office"]), ControlRoleID: &f.roles["Support (No Files)"].ID},
		{Name: "alice no server", Action: store.ACLActionDeny, Priority: 20, Enabled: true, UserID: &f.users["alice"].ID,
			DeviceID: &f.devs[devServer].ID},
		{Name: "alice office", Action: store.ACLActionAllow, Priority: 30, Enabled: true, UserID: &f.users["alice"].ID,
			DeviceGroupID: ptr(f.groups["office"])},
	} {
		if err := f.st.ConnectionACLs.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("rules", func(t *testing.T) {
		const signIn = ReasonSignIn
		f.run(t, []authzCase{
			{name: "anonymous", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "anonymous unknown device", target: devUnknown, allow: true, cause: CauseUnknownDevice},
			{name: "disabled user", token: "carol", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "garbage token", token: "garbage", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "expired token", token: "alice-expired", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "revoked token", token: "bob-revoked", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "token of another key", token: "admin-foreign", target: devOffice, reason: signIn, cause: CauseAnonymous},
			{name: "rule role", token: "bob", target: devOffice, allow: true, perms: permsSupport, cause: CauseRule, user: "bob",
				rule: "support office", role: "Support (No Files)"},
			{name: "web client token", token: "bob-web", target: devOffice, allow: true, perms: permsSupport, cause: CauseRule, user: "bob",
				rule: "support office", role: "Support (No Files)"},
			{name: "rule role forbids file transfer", token: "bob", target: devOffice, conn: ConnFileTransfer,
				reason: "File transfer is not allowed by control role Support (No Files)", cause: CauseConnType, user: "bob",
				rule: "support office", role: "Support (No Files)"},
			{name: "no matching rule", token: "bob", target: devServer, reason: ReasonDefaultDeny, cause: CauseDefaultDeny, user: "bob"},
			{name: "deny rule", token: "alice", target: devServer, reason: "Denied by access rule alice no server", cause: CauseRule,
				user: "alice", rule: "alice no server"},
			{name: "allow rule without role uses default", token: "alice", target: devOffice, allow: true, cause: CauseRule,
				user: "alice", rule: "alice office", role: "Full Control"},
			{name: "owner", token: "dave", target: devOwned, allow: true, cause: CauseOwner, user: "dave", role: "Full Control"},
			{name: "admin", token: "admin", target: devServer, allow: true, cause: CauseAdmin, user: "admin"},
			{name: "disabled admin", token: "eve", target: devServer, reason: signIn, cause: CauseAnonymous},
		})
	})
}

// Decide is also used by the audit kick, which resolves users itself.
func TestDecideDirect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d := f.devs[devOffice]
	if err := f.st.ConnectionACLs.Create(ctx, &store.ConnectionACL{Name: "deny all", Action: store.ACLActionDeny,
		Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		user  *store.User
		allow bool
	}{
		"nil user":      {nil, false},
		"denied user":   {f.users["alice"], false},
		"disabled user": {f.users["carol"], false},
		"admin":         {f.users["admin"], true},
	} {
		got, err := Decide(ctx, f.st.ConnectionACLs, c.user, d, ConnDefault)
		if err != nil || got.Allow != c.allow {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
}

func ptr(v int64) *int64 { return &v }
