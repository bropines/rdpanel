package aclpolicy

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

// ruleView is a stored rule as its resolved targets, comparable across
// export/apply round trips.
func ruleViews(t *testing.T, s *store.Store) []string {
	t.Helper()
	acls, err := s.ConnectionACLs.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id := func(p *int64) string {
		if p == nil {
			return "-"
		}
		return fmt.Sprint(*p)
	}
	var out []string
	for _, a := range acls {
		out = append(out, fmt.Sprintf("%s|%s|u=%s ug=%s d=%s dg=%s|role=%s|%d|%t", a.Name, a.Action,
			id(a.UserID), id(a.UserGroupID), id(a.DeviceID), id(a.DeviceGroupID), a.ControlRoleName, a.Priority, a.Enabled))
	}
	sort.Strings(out)
	return out
}

// Export names every target the way the policy compiler resolves it, so an
// exported policy applies back to the same rules: devices by RustDesk id (a
// device without alias was exported as "*", one with an alias as an
// unresolvable "device:<alias>"), and names with quotes or control
// characters stay valid JSON.
func TestExportApplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewService(s)

	alice := &store.User{Username: "alice"}
	if err := s.Users.Create(ctx, alice); err != nil {
		t.Fatal(err)
	}
	ops := &store.UserGroup{Name: `Ops "night" \ team`}
	if err := s.UserGroups.Create(ctx, ops); err != nil {
		t.Fatal(err)
	}
	bare := &store.Device{RustdeskID: "500000001"}                                           // no alias, no hostname
	named := &store.Device{RustdeskID: "500000002", Hostname: "srv-02", Alias: "db-primary"} // alias != id
	for _, d := range []*store.Device{bare, named} {
		if err := s.Devices.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	vault := &store.DeviceGroup{Name: "Vault <prod>"}
	if err := s.DeviceGroups.Create(ctx, vault); err != nil {
		t.Fatal(err)
	}
	role := &store.ControlRole{Name: "Ops \"limited\"", Note: "bell\a and\ttab", AllowKeyboardMouse: true}
	if err := s.ControlRoles.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*store.ConnectionACL{
		{Name: "alice to bare", UserID: &alice.ID, DeviceID: &bare.ID, Action: store.ACLActionDeny, Priority: 10, Enabled: true},
		{Name: "ops to named", UserGroupID: &ops.ID, DeviceID: &named.ID, ControlRoleID: &role.ID, Action: store.ACLActionAllow, Priority: 20, Enabled: true},
		{Name: "all to vault", DeviceGroupID: &vault.ID, Action: store.ACLActionDeny, Priority: 30},
	} {
		if err := s.ConnectionACLs.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	before := ruleViews(t, s)

	exported, err := svc.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(exported, `"device:500000001"`) || !strings.Contains(exported, `"device:500000002"`) {
		t.Fatalf("devices not exported by RustDesk id:\n%s", exported)
	}
	res, err := svc.Apply(ctx, []byte(exported))
	if err != nil {
		t.Fatalf("apply exported policy: %v (%+v)\n%s", err, res, exported)
	}
	after := ruleViews(t, s)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("round trip changed the rules:\nbefore %v\nafter  %v", before, after)
	}
	if got, err := s.ControlRoles.GetByName(ctx, role.Name); err != nil || got.Note != role.Note {
		t.Fatalf("role note: %+v %v", got, err)
	}
}

// A rule may name an existing role that the document does not list: Apply
// keeps that role and links the rule to it (it used to delete the role and
// leave the rule on the default "Full Control"). Unused roles missing from
// the document are still removed.
func TestApplyKeepsRolesRulesUse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewService(s)
	if err := s.Users.Create(ctx, &store.User{Username: "bob"}); err != nil {
		t.Fatal(err)
	}
	view, err := s.ControlRoles.GetByName(ctx, "View Only")
	if err != nil {
		t.Fatal(err)
	}
	policy := []byte(`{
		"version": 1,
		"acls": [{"name": "bob watches", "action": "allow", "src": ["user:bob"], "dst": ["*"], "role": "View Only"}],
		"tests": [{"src": "user:bob", "dst": "device:600000001", "expect": "allow", "role": "View Only"}]
	}`)
	if err := s.Devices.Create(ctx, &store.Device{RustdeskID: "600000001"}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Apply(ctx, policy)
	if err != nil {
		t.Fatalf("apply: %v %+v", err, res)
	}
	acls, err := s.ConnectionACLs.List(ctx)
	if err != nil || len(acls) != 1 {
		t.Fatalf("rules: %+v %v", acls, err)
	}
	if acls[0].ControlRoleID == nil || *acls[0].ControlRoleID != view.ID {
		t.Fatalf("rule role: %v, want View Only (%d)", acls[0].ControlRoleID, view.ID)
	}
	if _, err := s.ControlRoles.GetByName(ctx, "Support (No Files)"); err == nil {
		t.Fatal("unused role missing from the document was kept")
	}
	if def, err := s.ControlRoles.GetDefault(ctx); err != nil || def.Name != "Full Control" {
		t.Fatalf("default role: %+v %v", def, err)
	}
}
