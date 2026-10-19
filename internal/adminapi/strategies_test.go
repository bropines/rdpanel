package adminapi

import (
	"net/http"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

func TestStrategiesAdminAPI(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("strat_admin", store.RoleAdmin)
	user, _ := e.as("strat_user", store.RoleUser)

	// User cannot list strategies (403 forbidden)
	code, _, _ := user.do("GET", "/strategies", nil)
	if code != http.StatusForbidden {
		t.Fatalf("expected 403 for user role, got %d", code)
	}

	// Admin lists strategies -> at least 3 seeded templates
	code, m, _ := admin.do("GET", "/strategies", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, m)
	}
	items, ok := m["items"].([]any)
	if !ok || len(items) < 3 {
		t.Fatalf("expected at least 3 strategies, got %v", m)
	}

	// Create a new strategy
	createBody := map[string]any{
		"name": "Classroom Lab",
		"note": "Policy for student computer lab",
		"config_options": map[string]any{
			"approve-mode":                     "click",
			"enable-clipboard":                 "N",
			"allow-remote-config-modification": "N",
		},
		"is_default": false,
	}
	code, created, _ := admin.do("POST", "/strategies", createBody)
	if code != http.StatusCreated {
		t.Fatalf("expected 201 created, got %d: %v", code, created)
	}
	stratID := int64(created["id"].(float64))

	// Get strategy
	code, got, _ := admin.do("GET", "/strategies/"+idStr(stratID), nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, got)
	}
	if got["name"] != "Classroom Lab" {
		t.Fatalf("unexpected name: %v", got["name"])
	}

	// Update strategy
	updateBody := map[string]any{
		"note": "Updated classroom policy",
		"config_options": map[string]any{
			"approve-mode":     "password",
			"enable-clipboard": "Y",
		},
	}
	code, updated, _ := admin.do("PATCH", "/strategies/"+idStr(stratID), updateBody)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, updated)
	}
	if updated["note"] != "Updated classroom policy" {
		t.Fatalf("unexpected note: %v", updated["note"])
	}

	// Set as default
	code, def, _ := admin.do("POST", "/strategies/"+idStr(stratID)+"/default", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, def)
	}
	if def["is_default"] != true {
		t.Fatalf("expected is_default=true, got %v", def["is_default"])
	}

	// Cannot delete the default strategy
	code, _, _ = admin.do("DELETE", "/strategies/"+idStr(stratID), nil)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 when deleting default strategy, got %d", code)
	}

	// Revert default back to first strategy
	firstID := int64(items[0].(map[string]any)["id"].(float64))
	code, _, _ = admin.do("POST", "/strategies/"+idStr(firstID)+"/default", nil)
	if code != http.StatusOK {
		t.Fatalf("failed to revert default strategy: %d", code)
	}

	// Now delete custom strategy
	code, _, _ = admin.do("DELETE", "/strategies/"+idStr(stratID), nil)
	if code != http.StatusNoContent {
		t.Fatalf("expected 204 deleted, got %d", code)
	}

	// 404 for deleted strategy
	code, _, _ = admin.do("GET", "/strategies/"+idStr(stratID), nil)
	if code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestStrategyDeviceAndGroupAssignment(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("strat_assign_admin", store.RoleAdmin)

	// Create group with strategy
	groupBody := map[string]any{
		"name": "Finance Department",
		"note": "Finance group",
	}
	code, g, _ := admin.do("POST", "/device-groups", groupBody)
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %v", code, g)
	}
	groupID := int64(g["id"].(float64))

	// Update group with strategy
	listCode, listResp, _ := admin.do("GET", "/strategies", nil)
	if listCode != http.StatusOK {
		t.Fatal(listCode)
	}
	stratID := int64(listResp["items"].([]any)[0].(map[string]any)["id"].(float64))

	code, updatedGroup, _ := admin.do("PATCH", "/device-groups/"+idStr(groupID), map[string]any{
		"strategy_id": stratID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, updatedGroup)
	}
	if updatedGroup["strategy_id"] == nil || int64(updatedGroup["strategy_id"].(float64)) != stratID {
		t.Fatalf("expected strategy_id %d, got %v", stratID, updatedGroup["strategy_id"])
	}

	// Create a device directly via store
	d := &store.Device{RustdeskID: "998877665"}
	if err := e.deps.Store.Devices.Create(e.ctx, d); err != nil {
		t.Fatal(err)
	}

	// Update device with strategy override
	code, updatedDevice, _ := admin.do("PATCH", "/devices/"+idStr(d.ID), map[string]any{
		"strategy_id": stratID,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, updatedDevice)
	}
	if updatedDevice["strategy_id"] == nil || int64(updatedDevice["strategy_id"].(float64)) != stratID {
		t.Fatalf("expected strategy_id %d, got %v", stratID, updatedDevice["strategy_id"])
	}

	// Clear strategy override
	code, clearedDevice, _ := admin.do("PATCH", "/devices/"+idStr(d.ID), map[string]any{
		"strategy_id": nil,
	})
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, clearedDevice)
	}
	if clearedDevice["strategy_id"] != nil {
		t.Fatalf("expected null strategy_id, got %v", clearedDevice["strategy_id"])
	}

	// Reject non-existent strategy
	code, _, _ = admin.do("PATCH", "/devices/"+idStr(d.ID), map[string]any{
		"strategy_id": 999999,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-existent strategy, got %d", code)
	}
}
