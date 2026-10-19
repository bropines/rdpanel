package aclpolicy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(d)
}

func TestParserHuJSON(t *testing.T) {
	hujsonInput := []byte(`
		// RD Panel Policy
		{
			"version": 1,
			/* Custom roles */
			"roles": {
				"observer": {
					"note": "Read only role",
					"allow_keyboard_mouse": false,
					"allow_clipboard": false,
					"allow_file_transfer": false,
					"allow_audio": true,
					"allow_tcp_tunneling": false,
					"allow_terminal": false,
					"allow_restart": false, // trailing comma
				},
			},
			"acls": [
				{
					"name": "General Allow",
					"action": "allow",
					"src": ["*"],
					"dst": ["*"],
					"role": "observer",
				},
			],
			"tests": [
				{
					"src": "user:alex",
					"dst": "device:12345",
					"expect": "allow",
				},
			],
		}
	`)

	doc, err := Parse(hujsonInput)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if doc.Version != 1 {
		t.Errorf("expected version 1, got %d", doc.Version)
	}
	if len(doc.Roles) != 1 {
		t.Errorf("expected 1 role, got %d", len(doc.Roles))
	}
	if len(doc.ACLs) != 1 {
		t.Errorf("expected 1 acl, got %d", len(doc.ACLs))
	}
	if len(doc.Tests) != 1 {
		t.Errorf("expected 1 test, got %d", len(doc.Tests))
	}
}

func TestServiceValidationAndTesting(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := NewService(s)

	// Create test user and device
	adminUser := &store.User{
		Username: "alice",
		Role:     store.RoleAdmin,
		Status:   store.UserActive,
	}
	if err := s.Users.Create(ctx, adminUser); err != nil {
		t.Fatalf("create user: %v", err)
	}

	dev := &store.Device{
		RustdeskID: "111222333",
		Hostname:   "workstation-1",
	}
	if err := s.Devices.Create(ctx, dev); err != nil {
		t.Fatalf("create device: %v", err)
	}

	// 1. Valid policy with passing tests
	validPolicy := []byte(`
		{
			"version": 1,
			"roles": {
				"auditor": {
					"allow_keyboard_mouse": false,
					"allow_clipboard": true,
					"allow_file_transfer": false,
					"allow_audio": true,
					"allow_tcp_tunneling": false,
					"allow_terminal": false,
					"allow_restart": false
				}
			},
			"acls": [
				{
					"name": "Allow Alice to Workstation",
					"action": "allow",
					"src": ["user:alice"],
					"dst": ["device:111222333"],
					"role": "auditor",
					"priority": 10
				}
			],
			"tests": [
				{
					"src": "user:alice",
					"dst": "device:111222333",
					"expect": "allow",
					"role": "auditor"
				}
			]
		}
	`)

	compiled, res, err := svc.ValidateAndTest(ctx, validPolicy)
	if err != nil {
		t.Fatalf("validate error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid, got errors: %v", res.Errors)
	}
	if len(res.TestResults) != 1 || !res.TestResults[0].Passed {
		t.Fatalf("expected test to pass, got: %+v", res.TestResults)
	}
	if len(compiled.ACLs) != 1 || len(compiled.Roles) != 1 {
		t.Fatalf("expected 1 role and 1 acl compiled")
	}

	// 2. Policy with failing test assertion
	failingPolicy := []byte(`
		{
			"version": 1,
			"acls": [
				{
					"name": "Deny Alice",
					"action": "deny",
					"src": ["user:alice"],
					"dst": ["device:111222333"]
				}
			],
			"tests": [
				{
					"src": "user:alice",
					"dst": "device:111222333",
					"expect": "allow" // assertion will fail because rule is deny!
				}
			]
		}
	`)

	_, failRes, err := svc.ValidateAndTest(ctx, failingPolicy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if failRes.Valid {
		t.Fatalf("expected failing policy to be marked invalid")
	}
	if len(failRes.TestResults) != 1 || failRes.TestResults[0].Passed {
		t.Fatalf("expected test assertion to fail")
	}

	// 3. Test Apply
	applyRes, err := svc.Apply(ctx, validPolicy)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if !applyRes.Valid {
		t.Fatalf("expected apply to be valid, got: %v", applyRes.Errors)
	}

	// Verify database was populated
	roles, err := s.ControlRoles.List(ctx)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	foundAuditor := false
	for _, r := range roles {
		if r.Name == "auditor" {
			foundAuditor = true
			break
		}
	}
	if !foundAuditor {
		t.Errorf("role 'auditor' not found in db after apply")
	}

	acls, err := s.ConnectionACLs.List(ctx)
	if err != nil {
		t.Fatalf("list acls: %v", err)
	}
	if len(acls) != 1 {
		t.Fatalf("expected 1 acl in db, got %d", len(acls))
	}
	if acls[0].Name != "Allow Alice to Workstation" {
		t.Errorf("unexpected acl name: %s", acls[0].Name)
	}

	// 4. Test Export
	exported, err := svc.Export(ctx)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}
	if len(exported) == 0 {
		t.Fatalf("expected non-empty export")
	}

	// Verify exported HuJSON parses back successfully
	exportedDoc, err := Parse([]byte(exported))
	if err != nil {
		t.Fatalf("failed to parse exported policy: %v", err)
	}
	if len(exportedDoc.ACLs) != 1 {
		t.Errorf("expected 1 acl in exported doc, got %d", len(exportedDoc.ACLs))
	}
}

func TestParserErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"unclosed comment", "/* unclosed comment"},
		{"unclosed string", `{"roles": {"test": "unclosed}}`},
		{"invalid action", `{"acls": [{"name": "n", "action": "maybe", "src": ["*"], "dst": ["*"]}]}`},
		{"empty src", `{"acls": [{"name": "n", "action": "allow", "src": [], "dst": ["*"]}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.input))
			if err == nil {
				t.Fatalf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}
