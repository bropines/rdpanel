package adminapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func TestACLPolicyAdminAPI(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("policy_admin", store.RoleAdmin)
	user, userRow := e.as("policy_user", store.RoleUser)

	ctx := context.Background()

	// Create test device owned by policy_user
	dev := &store.Device{
		RustdeskID:  "dev-pol-1",
		Hostname:    "test-pol-host",
		OwnerUserID: &userRow.ID,
	}
	if err := e.st.Devices.Create(ctx, dev); err != nil {
		t.Fatalf("create device: %v", err)
	}

	// 1. Regular user gets 403 Forbidden
	code, _, _ := user.do("GET", "/acls/policy", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role on GET /acls/policy, got %d", code)
	}
	code, _, _ = user.do("PUT", "/acls/policy", map[string]any{"policy": "{}"})
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role on PUT /acls/policy, got %d", code)
	}
	code, _, _ = user.do("POST", "/acls/policy/test", map[string]any{"policy": "{}"})
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role on POST /acls/policy/test, got %d", code)
	}

	// 2. Admin exports policy (GET /acls/policy)
	code, expRes, _ := admin.do("GET", "/acls/policy", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 from GET /acls/policy, got %d: %v", code, expRes)
	}
	policyStr, ok := expRes["policy"].(string)
	if !ok || len(policyStr) == 0 {
		t.Fatalf("expected non-empty policy string, got %v", expRes)
	}

	// 3. Test dry-run with test assertions (POST /acls/policy/test)
	samplePolicy := `{
		// Policy with custom role and rule
		"version": 1,
		"roles": {
			"viewer": {
				"note": "Read-only access",
				"allow_keyboard_mouse": false,
				"allow_clipboard": true,
				"allow_file_transfer": false,
				"allow_audio": true,
				"allow_tcp_tunneling": false,
				"allow_terminal": false,
				"allow_restart": false,
				"is_default": false
			}
		},
		"acls": [
			{
				"name": "User to device rule",
				"action": "allow",
				"src": ["user:policy_user"],
				"dst": ["device:dev-pol-1"],
				"role": "viewer",
				"priority": 100
			}
		],
		"tests": [
			{
				"src": "user:policy_user",
				"dst": "device:dev-pol-1",
				"expect": "allow",
				"role": "viewer"
			}
		]
	}`

	code, testRes, _ := admin.do("POST", "/acls/policy/test", map[string]any{
		"policy": samplePolicy,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls/policy/test, got %d: %v", code, testRes)
	}
	if testRes["valid"] != true {
		t.Fatalf("expected valid=true, got %v", testRes)
	}
	testResults := testRes["test_results"].([]any)
	if len(testResults) != 1 {
		t.Fatalf("expected 1 test result, got %d", len(testResults))
	}
	tr0 := testResults[0].(map[string]any)
	if tr0["passed"] != true {
		t.Fatalf("expected test assertion to pass, got %v", tr0)
	}

	// 4. Test dry-run with failing assertion
	failingPolicy := `{
		"version": 1,
		"acls": [
			{
				"name": "Deny all",
				"action": "deny",
				"src": ["*"],
				"dst": ["*"],
				"priority": 10
			}
		],
		"tests": [
			{
				"src": "user:policy_user",
				"dst": "device:dev-pol-1",
				"expect": "allow" // expects allow, but rule denies!
			}
		]
	}`
	code, failRes, _ := admin.do("POST", "/acls/policy/test", map[string]any{
		"policy": failingPolicy,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls/policy/test, got %d", code)
	}
	if failRes["valid"] != false {
		t.Fatalf("expected valid=false for failing assertion, got %v", failRes)
	}

	// 5. Applying failing policy via PUT /acls/policy should fail with 400
	code, applyFail, _ := admin.do("PUT", "/acls/policy", map[string]any{
		"policy": failingPolicy,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for failing policy apply, got %d: %v", code, applyFail)
	}

	// 6. Applying valid policy via PUT /acls/policy should succeed
	code, applySuccess, _ := admin.do("PUT", "/acls/policy", map[string]any{
		"policy": samplePolicy,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 for valid policy apply, got %d: %v", code, applySuccess)
	}
	if applySuccess["valid"] != true {
		t.Fatalf("expected valid=true in apply response, got %v", applySuccess)
	}

	// 7. Verify that connection evaluation now uses the newly applied policy
	code, evalRes, _ := admin.do("POST", "/acls/test", map[string]any{
		"user_id":   userRow.ID,
		"device_id": dev.ID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200 from /acls/test, got %d: %v", code, evalRes)
	}
	if evalRes["decision"] != "allow" {
		t.Fatalf("expected decision=allow, got %v", evalRes["decision"])
	}
	effRole, ok := evalRes["effective_role"].(map[string]any)
	if !ok || effRole["name"] != "viewer" {
		t.Fatalf("expected effective_role=viewer, got %v", effRole)
	}
}
