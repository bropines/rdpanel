package clientapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
	"github.com/bropines/rdpanel/internal/websessions"
)

// The ACL check on an authorized connection: admins are never kicked, and
// when rules exist a controller whose user cannot be determined is denied.
func TestAuditConnACLCheck(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.device("300000001", "dGFyZ2V0")
	admin := e.user("root", "pw-root-12345", func(u *store.User) { u.Role = store.RoleAdmin })
	adminDev := &store.Device{RustdeskID: "300000002", OwnerUserID: &admin.ID}
	if err := e.st.Devices.Create(ctx, adminDev); err != nil {
		t.Fatal(err)
	}
	authorized := func(connID int, controller string) []int32 {
		t.Helper()
		body := fmt.Sprintf(`{"id":"300000001","uuid":"dGFyZ2V0","conn_id":%d,"session_id":"1","peer":[%q,"name"],"type":0}`,
			connID, controller)
		e.empty("POST", "/api/audit/conn", body, "")
		return e.kicks.Take(target.RustdeskID, []int32{int32(connID)})
	}

	// No rules: nothing changes, unknown controllers included.
	if k := authorized(1, "399999999"); len(k) != 0 {
		t.Fatalf("kicked without rules: %v", k)
	}

	if err := e.st.ConnectionACLs.Create(ctx, &store.ConnectionACL{Name: "deny all", Action: store.ACLActionDeny,
		Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if k := authorized(2, "300000002"); len(k) != 0 {
		t.Fatalf("admin kicked: %v", k)
	}
	if k := authorized(3, "399999999"); len(k) != 1 || k[0] != 3 {
		t.Fatalf("unknown controller not denied: %v", k)
	}
}

// Web client sessions announce a panel-issued id; the check resolves it to
// the panel user that opened the session.
func TestAuditConnACLWebSession(t *testing.T) {
	reg := websessions.New(nil)
	e := newEnv(t, func(d *Deps) { d.WebSessions = reg })
	ctx := context.Background()
	target := e.device("300000011", "dGFyZ2V0")
	admin := e.user("root", "pw-root-12345", func(u *store.User) { u.Role = store.RoleAdmin })
	bob := e.user("bob", "pw-bob-123456", nil)
	adminWeb, _ := reg.Issue(admin.ID)
	bobWeb, _ := reg.Issue(bob.ID)
	authorized := func(connID int, controller string) []int32 {
		t.Helper()
		body := fmt.Sprintf(`{"id":"300000011","uuid":"dGFyZ2V0","conn_id":%d,"session_id":"1","peer":[%q,"name"],"type":0}`,
			connID, controller)
		e.empty("POST", "/api/audit/conn", body, "")
		return e.kicks.Take(target.RustdeskID, []int32{int32(connID)})
	}
	if err := e.st.ConnectionACLs.Create(ctx, &store.ConnectionACL{Name: "deny all", Action: store.ACLActionDeny,
		Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if k := authorized(1, adminWeb); len(k) != 0 {
		t.Fatalf("admin web session kicked: %v", k)
	}
	if k := authorized(2, bobWeb); len(k) != 1 {
		t.Fatalf("denied user's web session not kicked: %v", k)
	}
	if k := authorized(3, "web-0000000000"); len(k) != 1 {
		t.Fatalf("unknown web id not denied: %v", k)
	}
}

// The kick follows the effective control role like hbbs' authorization
// does: connection types the role forbids are kicked, rules or not.
func TestAuditConnACLRoleConnType(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	target := e.device("300000021", "dGFyZ2V0")
	bob := e.user("bob", "pw-bob-123456", nil)
	admin := e.user("root", "pw-root-12345", func(u *store.User) { u.Role = store.RoleAdmin })
	for id, owner := range map[string]int64{"300000022": bob.ID, "300000023": admin.ID} {
		if err := e.st.Devices.Create(ctx, &store.Device{RustdeskID: id, OwnerUserID: &owner}); err != nil {
			t.Fatal(err)
		}
	}
	authorized := func(connID, connType int, controller string) bool {
		t.Helper()
		body := fmt.Sprintf(`{"id":"300000021","uuid":"dGFyZ2V0","conn_id":%d,"session_id":"1","peer":[%q,"name"],"type":%d}`,
			connID, controller, connType)
		e.empty("POST", "/api/audit/conn", body, "")
		return len(e.kicks.Take(target.RustdeskID, []int32{int32(connID)})) == 1
	}
	viewOnly, err := e.st.ControlRoles.GetByName(ctx, "View Only")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.ControlRoles.SetDefault(ctx, viewOnly.ID); err != nil {
		t.Fatal(err)
	}
	// Audit types: 0 remote, 1 file transfer, 2 port forward, 3 camera, 4 terminal.
	for i, c := range []struct {
		connType   int
		controller string
		kicked     bool
	}{
		{0, "300000022", false},
		{1, "300000022", true},
		{2, "300000022", true},
		{3, "300000022", false},
		{4, "300000022", true},
		{1, "399999999", true},  // unknown controller, still under the default role
		{1, "300000023", false}, // admins are never restricted
	} {
		if got := authorized(10+i, c.connType, c.controller); got != c.kicked {
			t.Errorf("type %d from %s: kicked=%v, want %v", c.connType, c.controller, got, c.kicked)
		}
	}
}
