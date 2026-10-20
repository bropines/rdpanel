package store

import (
	"context"
	"errors"
	"testing"
)

func TestControlRolesSeeded(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	roles, err := st.ControlRoles.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) < 3 {
		t.Fatalf("expected at least 3 seeded control roles, got %d", len(roles))
	}

	def, err := st.ControlRoles.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "Full Control" {
		t.Fatalf("expected default role 'Full Control', got %q", def.Name)
	}
	if !def.AllowKeyboardMouse || !def.AllowClipboard || !def.AllowFileTransfer {
		t.Fatalf("expected Full Control to have all permissions enabled, got: %+v", def)
	}
}

func TestControlRolesCRUD(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	cr := &ControlRole{
		Name:               "Custom Support",
		Note:               "Support without terminal",
		AllowKeyboardMouse: true,
		AllowClipboard:     true,
		AllowFileTransfer:  false,
		AllowAudio:         true,
		AllowTCPTunneling:  false,
		AllowTerminal:      false,
		AllowRestart:       false,
		IsDefault:          false,
	}

	if err := st.ControlRoles.Create(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if cr.ID == 0 {
		t.Fatal("expected positive ID")
	}

	got, err := st.ControlRoles.Get(ctx, cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Custom Support" || got.AllowTerminal {
		t.Fatalf("unexpected got role: %+v", got)
	}

	// Update
	got.Note = "Updated note"
	got.AllowRestart = true
	if err := st.ControlRoles.Update(ctx, got); err != nil {
		t.Fatal(err)
	}

	updated, err := st.ControlRoles.GetByName(ctx, "Custom Support")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.AllowRestart || updated.Note != "Updated note" {
		t.Fatalf("unexpected updated role: %+v", updated)
	}

	// Set default
	if err := st.ControlRoles.SetDefault(ctx, cr.ID); err != nil {
		t.Fatal(err)
	}
	newDef, err := st.ControlRoles.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newDef.ID != cr.ID {
		t.Fatalf("expected new default ID %d, got %d", cr.ID, newDef.ID)
	}

	// Cannot delete default
	if err := st.ControlRoles.Delete(ctx, cr.ID); err == nil {
		t.Fatal("expected error deleting default role")
	}

	// Reset default back to 1
	if err := st.ControlRoles.SetDefault(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// Delete custom role now
	if err := st.ControlRoles.Delete(ctx, cr.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionACLsCRUDAndEvaluation(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	// 1. Create User Groups
	ugSupport := &UserGroup{Name: "Support Team"}
	if err := st.UserGroups.Create(ctx, ugSupport); err != nil {
		t.Fatal(err)
	}
	ugSales := &UserGroup{Name: "Sales Team"}
	if err := st.UserGroups.Create(ctx, ugSales); err != nil {
		t.Fatal(err)
	}

	// 2. Create Users
	uAlice := &User{Username: "alice", DisplayName: "Alice", UserGroupID: &ugSupport.ID}
	if err := st.Users.Create(ctx, uAlice); err != nil {
		t.Fatal(err)
	}
	uBob := &User{Username: "bob", DisplayName: "Bob", UserGroupID: &ugSales.ID}
	if err := st.Users.Create(ctx, uBob); err != nil {
		t.Fatal(err)
	}

	// 3. Create Device Groups
	dgWorkstations := &DeviceGroup{Name: "Office Workstations"}
	if err := st.DeviceGroups.Create(ctx, dgWorkstations); err != nil {
		t.Fatal(err)
	}
	dgServers := &DeviceGroup{Name: "Production Servers"}
	if err := st.DeviceGroups.Create(ctx, dgServers); err != nil {
		t.Fatal(err)
	}

	// 4. Create Devices
	d1 := &Device{
		RustdeskID:    "111222333",
		DeviceGroupID: &dgWorkstations.ID,
		OwnerUserID:   &uBob.ID, // Bob owns Workstation 1
	}
	if err := st.Devices.Create(ctx, d1); err != nil {
		t.Fatal(err)
	}
	d2 := &Device{
		RustdeskID:    "444555666",
		DeviceGroupID: &dgServers.ID,
	}
	if err := st.Devices.Create(ctx, d2); err != nil {
		t.Fatal(err)
	}

	// Phase 1: No rules configured yet
	dec, _, _, err := st.ConnectionACLs.Evaluate(ctx, uBob.ID, d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionOwner {
		t.Fatalf("expected ACLDecisionOwner when Bob owns d1, got %v", dec)
	}

	dec, _, _, err = st.ConnectionACLs.Evaluate(ctx, uAlice.ID, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionNoRules {
		t.Fatalf("expected ACLDecisionNoRules when no rules exist, got %v", dec)
	}

	// Phase 2: Add rules
	// Rule 1: Support group -> Workstations = ALLOW, priority 10
	viewRole, err := st.ControlRoles.GetByName(ctx, "View Only")
	if err != nil {
		t.Fatal(err)
	}

	rule1 := &ConnectionACL{
		Name:          "Support can view Workstations",
		UserGroupID:   &ugSupport.ID,
		DeviceGroupID: &dgWorkstations.ID,
		ControlRoleID: &viewRole.ID,
		Action:        ACLActionAllow,
		Priority:      10,
		Enabled:       true,
	}
	if err := st.ConnectionACLs.Create(ctx, rule1); err != nil {
		t.Fatal(err)
	}
	if rule1.ID == 0 {
		t.Fatal("expected positive rule ID")
	}

	// Rule 2: Support group -> Servers = DENY, priority 5 (higher priority)
	rule2 := &ConnectionACL{
		Name:          "Deny Support to Production Servers",
		UserGroupID:   &ugSupport.ID,
		DeviceGroupID: &dgServers.ID,
		Action:        ACLActionDeny,
		Priority:      5,
		Enabled:       true,
	}
	if err := st.ConnectionACLs.Create(ctx, rule2); err != nil {
		t.Fatal(err)
	}

	// Check rule listing with joins
	list, err := st.ConnectionACLs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(list))
	}
	if list[0].Priority != 5 || list[0].UserGroupName != "Support Team" || list[0].DeviceGroupName != "Production Servers" {
		t.Fatalf("unexpected first rule: %+v", list[0])
	}
	if list[1].Priority != 10 || list[1].ControlRoleName != "View Only" {
		t.Fatalf("unexpected second rule: %+v", list[1])
	}

	// Phase 3: Evaluate with rules active
	// Alice on d1 (Workstation): matches rule1 -> ALLOW with View Only role
	dec, matchedRule, role, err := st.ConnectionACLs.Evaluate(ctx, uAlice.ID, d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionAllow || matchedRule == nil || matchedRule.ID != rule1.ID || role == nil || role.Name != "View Only" {
		t.Fatalf("expected ALLOW with View Only, got dec=%v rule=%+v role=%+v", dec, matchedRule, role)
	}

	// Alice on d2 (Server): matches rule2 -> DENY
	dec, matchedRule, _, err = st.ConnectionACLs.Evaluate(ctx, uAlice.ID, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionDeny || matchedRule == nil || matchedRule.ID != rule2.ID {
		t.Fatalf("expected DENY from rule2, got dec=%v rule=%+v", dec, matchedRule)
	}

	// Bob on d1 (Workstation): Bob owns d1, but has no explicit rule -> should be ACLDecisionOwner
	dec, _, _, err = st.ConnectionACLs.Evaluate(ctx, uBob.ID, d1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionOwner {
		t.Fatalf("expected ACLDecisionOwner for Bob on d1, got %v", dec)
	}

	// Bob on d2 (Server): Bob does not own d2 and matches no rule -> should be ACLDecisionDeny
	dec, _, _, err = st.ConnectionACLs.Evaluate(ctx, uBob.ID, d2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dec != ACLDecisionDeny {
		t.Fatalf("expected ACLDecisionDeny for Bob on d2, got %v", dec)
	}

	// Test FilterAccessibleDevices for Alice: d1 should be present, d2 should be filtered out
	filtered, err := st.ConnectionACLs.FilterAccessibleDevices(ctx, uAlice.ID, []*Device{d1, d2})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != d1.ID {
		t.Fatalf("expected only d1 in filtered devices, got: %+v", filtered)
	}

	// Clean up rule
	if err := st.ConnectionACLs.Delete(ctx, rule1.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectionACLs.Delete(ctx, rule2.ID); err != nil {
		t.Fatal(err)
	}
}

// docs/acl-policy.md: an operator who owns the device, or shares a "shared"
// user group with its owner, keeps access unless a rule denies it.
func TestEvaluateSharedGroupAccess(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	shared := &UserGroup{Name: "Team", Visibility: VisibilityShared}
	solo := &UserGroup{Name: "Solo"}
	for _, g := range []*UserGroup{shared, solo} {
		if err := st.UserGroups.Create(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string, g *UserGroup) *User {
		u := &User{Username: name, UserGroupID: &g.ID}
		if err := st.Users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	ann, ben := mk("ann", shared), mk("ben", shared)
	cat, dan := mk("cat", solo), mk("dan", solo)
	bensPC := &Device{RustdeskID: "400000001", OwnerUserID: &ben.ID}
	dansPC := &Device{RustdeskID: "400000002", OwnerUserID: &dan.ID}
	for _, d := range []*Device{bensPC, dansPC} {
		if err := st.Devices.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// Some rule exists, so unmatched access falls to default deny.
	other := &Device{RustdeskID: "400000003"}
	if err := st.Devices.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := st.ConnectionACLs.Create(ctx, &ConnectionACL{Name: "x", DeviceID: &other.ID, Action: ACLActionAllow,
		Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	check := func(u *User, d *Device, want ACLDecision) {
		t.Helper()
		dec, _, _, err := st.ConnectionACLs.Evaluate(ctx, u.ID, d.ID)
		if err != nil || dec != want {
			t.Fatalf("%s -> %s: %v %v, want %v", u.Username, d.RustdeskID, dec, err, want)
		}
	}
	check(ann, bensPC, ACLDecisionOwner) // same shared group
	check(cat, dansPC, ACLDecisionDeny)  // same group, but not shared
	check(ann, dansPC, ACLDecisionDeny)

	// An explicit deny still wins.
	if err := st.ConnectionACLs.Create(ctx, &ConnectionACL{Name: "deny ann", UserID: &ann.ID, DeviceID: &bensPC.ID,
		Action: ACLActionDeny, Priority: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	check(ann, bensPC, ACLDecisionDeny)
	check(ben, bensPC, ACLDecisionOwner)
}

// A role referenced by any rule cannot be deleted (its rules would silently
// fall back to the default role); a failed default change keeps the default.
func TestControlRoleDeleteReferencedAndAtomicDefault(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	view, err := st.ControlRoles.GetByName(ctx, "View Only")
	if err != nil {
		t.Fatal(err)
	}
	rule := &ConnectionACL{Name: "viewers", ControlRoleID: &view.ID, Action: ACLActionAllow, Priority: 10}
	if err := st.ConnectionACLs.Create(ctx, rule); err != nil {
		t.Fatal(err)
	}
	if err := st.ControlRoles.Delete(ctx, view.ID); !errors.Is(err, ErrRoleInUse) || !errors.Is(err, ErrReferenced) {
		t.Fatalf("delete referenced role: %v", err)
	}
	if _, err := st.ControlRoles.Get(ctx, view.ID); err != nil {
		t.Fatalf("role gone: %v", err)
	}
	if err := st.ConnectionACLs.Delete(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ControlRoles.Delete(ctx, view.ID); err != nil {
		t.Fatalf("delete unreferenced role: %v", err)
	}

	def, err := st.ControlRoles.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ControlRoles.SetDefault(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set default of a missing role: %v", err)
	}
	if err := st.ControlRoles.Create(ctx, &ControlRole{Name: "Full Control", IsDefault: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create duplicate default: %v", err)
	}
	if err := st.ControlRoles.Update(ctx, &ControlRole{ID: 9999, Name: "Ghost", IsDefault: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing role: %v", err)
	}
	var n int
	if err := st.Querier().QueryRowContext(ctx, `SELECT count(*) FROM control_roles WHERE is_default = 1 AND id = ?`, def.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("default %q lost: %d %v", def.Name, n, err)
	}
}
