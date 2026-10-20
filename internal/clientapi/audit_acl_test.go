package clientapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
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
