package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func TestControlRolesAndACLAdminAPI(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("acl_admin", store.RoleAdmin)
	user, userRow := e.as("acl_user", store.RoleUser)

	ctx := context.Background()

	// Create a test device owned by acl_user
	dev := &store.Device{
		RustdeskID:  "dev-acl-1",
		Hostname:    "test-acl-host",
		OwnerUserID: &userRow.ID,
	}
	if err := e.st.Devices.Create(ctx, dev); err != nil {
		t.Fatalf("upsert device: %v", err)
	}

	// 1. Regular user forbidden from control-roles and acls
	code, _, _ := user.do("GET", "/control-roles", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role, got %d", code)
	}
	code, _, _ = user.do("GET", "/acls", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role, got %d", code)
	}

	// 2. Admin lists control roles -> 3 seeded roles
	code, m, _ := admin.do("GET", "/control-roles", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, m)
	}
	roles, ok := m["items"].([]any)
	if !ok || len(roles) < 3 {
		t.Fatalf("expected at least 3 seeded control roles, got %v", m)
	}

	// Find the default role ID
	var defaultRoleID int64
	for _, r := range roles {
		rm := r.(map[string]any)
		if rm["is_default"] == true {
			defaultRoleID = int64(rm["id"].(float64))
			break
		}
	}
	if defaultRoleID == 0 {
		t.Fatalf("no default role found in seed")
	}

	// Admin cannot delete the default role
	code, _, _ = admin.do("DELETE", "/control-roles/"+idStr(defaultRoleID), nil)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 when deleting default role, got %d", code)
	}

	// Create custom control role
	createRoleBody := map[string]any{
		"name":                 "Remote Observer",
		"note":                 "Observer without keyboard/mouse or file transfer",
		"allow_keyboard_mouse": false,
		"allow_clipboard":      false,
		"allow_file_transfer":  false,
		"allow_audio":          true,
		"allow_tcp_tunneling":  false,
		"allow_terminal":       false,
		"allow_restart":        false,
		"is_default":           false,
	}
	code, createdRole, _ := admin.do("POST", "/control-roles", createRoleBody)
	if code != http.StatusCreated {
		t.Fatalf("expected 201 created, got %d: %v", code, createdRole)
	}
	roleID := int64(createdRole["id"].(float64))

	// Get control role
	code, gotRole, _ := admin.do("GET", "/control-roles/"+idStr(roleID), nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, gotRole)
	}
	if gotRole["name"] != "Remote Observer" {
		t.Fatalf("unexpected name: %v", gotRole["name"])
	}

	// Update control role
	code, updatedRole, _ := admin.do("PATCH", "/control-roles/"+idStr(roleID), map[string]any{
		"allow_clipboard": true,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, updatedRole)
	}
	if updatedRole["allow_clipboard"] != true {
		t.Fatalf("expected allow_clipboard=true, got %v", updatedRole["allow_clipboard"])
	}

	// 3. Test ACL matrix
	// Initially no rules -> test evaluation for owner
	code, evalRes, _ := admin.do("POST", "/acls/test", map[string]any{
		"user_id":   userRow.ID,
		"device_id": dev.ID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls/test, got %d: %v", code, evalRes)
	}
	if evalRes["decision"] != "owner" {
		t.Fatalf("expected decision=owner, got %v", evalRes["decision"])
	}

	// Create ACL deny rule for the user
	createACLBody := map[string]any{
		"name":            "Deny User from Device",
		"note":            "Security restriction",
		"user_id":         userRow.ID,
		"device_id":       dev.ID,
		"control_role_id": roleID,
		"action":          "deny",
		"priority":        10,
		"enabled":         true,
	}
	code, createdACL, _ := admin.do("POST", "/acls", createACLBody)
	if code != http.StatusCreated {
		t.Fatalf("expected 201 created acl, got %d: %v", code, createdACL)
	}
	aclID := int64(createdACL["id"].(float64))

	// Test evaluation again -> should be deny
	code, evalRes, _ = admin.do("POST", "/acls/test", map[string]any{
		"user_id":   userRow.ID,
		"device_id": dev.ID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls/test, got %d: %v", code, evalRes)
	}
	if evalRes["decision"] != "deny" {
		t.Fatalf("expected decision=deny, got %v", evalRes["decision"])
	}

	// List ACLs
	code, aclsList, _ := admin.do("GET", "/acls", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls, got %d", code)
	}
	aclItems := aclsList["items"].([]any)
	if len(aclItems) != 1 {
		t.Fatalf("expected 1 acl rule, got %d", len(aclItems))
	}

	// Update ACL to allow with custom role
	code, updatedACL, _ := admin.do("PATCH", "/acls/"+idStr(aclID), map[string]any{
		"action": "allow",
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, updatedACL)
	}
	if updatedACL["action"] != "allow" {
		t.Fatalf("expected action=allow, got %v", updatedACL["action"])
	}

	// Test evaluation -> should be allow with roleID
	code, evalRes, _ = admin.do("POST", "/acls/test", map[string]any{
		"user_id":   userRow.ID,
		"device_id": dev.ID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, evalRes)
	}
	if evalRes["decision"] != "allow" {
		t.Fatalf("expected decision=allow, got %v", evalRes["decision"])
	}
	effRole := evalRes["effective_role"].(map[string]any)
	if int64(effRole["id"].(float64)) != roleID {
		t.Fatalf("expected role id %d, got %v", roleID, effRole["id"])
	}

	// Delete ACL rule
	code, _, _ = admin.do("DELETE", "/acls/"+idStr(aclID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("expected 204 deleted, got %d", code)
	}

	// Delete custom role
	code, _, _ = admin.do("DELETE", "/control-roles/"+idStr(roleID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("expected 204 deleted, got %d", code)
	}
}
