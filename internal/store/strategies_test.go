package store

import (
	"context"
	"testing"
)

func TestClientStrategiesSeeded(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	list, err := st.ClientStrategies.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 3 {
		t.Fatalf("expected at least 3 seeded strategies, got %d", len(list))
	}

	def, err := st.ClientStrategies.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "Standard Office" {
		t.Fatalf("expected default strategy 'Standard Office', got %q", def.Name)
	}
	if def.ConfigOptions["approve-mode"] != "both" {
		t.Fatalf("expected approve-mode 'both', got %q", def.ConfigOptions["approve-mode"])
	}
}

func TestClientStrategiesCRUD(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	strat := &ClientStrategy{
		Name: "Kiosk Mode",
		Note: "Kiosk configuration",
		ConfigOptions: map[string]string{
			"approve-mode":                     "password",
			"allow-remote-config-modification": "N",
			"direct-server":                    "N",
		},
		IsDefault: false,
	}

	if err := st.ClientStrategies.Create(ctx, strat); err != nil {
		t.Fatal(err)
	}
	if strat.ID == 0 {
		t.Fatal("expected positive ID")
	}

	got, err := st.ClientStrategies.Get(ctx, strat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Kiosk Mode" || got.ConfigOptions["approve-mode"] != "password" {
		t.Fatalf("unexpected got: %+v", got)
	}

	gotByName, err := st.ClientStrategies.GetByName(ctx, "kiosk mode")
	if err != nil {
		t.Fatal(err)
	}
	if gotByName.ID != strat.ID {
		t.Fatalf("GetByName ID mismatch: %d vs %d", gotByName.ID, strat.ID)
	}

	// Update
	strat.Note = "Updated note"
	strat.ConfigOptions["enable-audio"] = "N"
	if err := st.ClientStrategies.Update(ctx, strat); err != nil {
		t.Fatal(err)
	}

	updated, err := st.ClientStrategies.Get(ctx, strat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Note != "Updated note" || updated.ConfigOptions["enable-audio"] != "N" {
		t.Fatalf("unexpected updated: %+v", updated)
	}

	// Cannot delete default strategy
	def, err := st.ClientStrategies.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ClientStrategies.Delete(ctx, def.ID); err == nil {
		t.Fatal("expected error deleting default strategy")
	}

	// SetDefault
	if err := st.ClientStrategies.SetDefault(ctx, strat.ID); err != nil {
		t.Fatal(err)
	}
	newDef, err := st.ClientStrategies.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if newDef.ID != strat.ID {
		t.Fatalf("expected new default ID %d, got %d", strat.ID, newDef.ID)
	}

	// Reset default back to original and delete custom
	if err := st.ClientStrategies.SetDefault(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ClientStrategies.Delete(ctx, strat.ID); err != nil {
		t.Fatal(err)
	}
}

func TestResolveForDevice(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()

	def, err := st.ClientStrategies.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}

	custom := &ClientStrategy{
		Name:          "Custom Override",
		ConfigOptions: map[string]string{"approve-mode": "password"},
	}
	if err := st.ClientStrategies.Create(ctx, custom); err != nil {
		t.Fatal(err)
	}

	groupCustom := &ClientStrategy{
		Name:          "Group Policy",
		ConfigOptions: map[string]string{"approve-mode": "click"},
	}
	if err := st.ClientStrategies.Create(ctx, groupCustom); err != nil {
		t.Fatal(err)
	}

	group := &DeviceGroup{Name: "Engineering", StrategyID: &groupCustom.ID}
	if err := st.DeviceGroups.Create(ctx, group); err != nil {
		t.Fatal(err)
	}

	// 1. Device with no group, no strategy -> default
	d1 := &Device{RustdeskID: "dev1"}
	if err := st.Devices.Create(ctx, d1); err != nil {
		t.Fatal(err)
	}
	s1, err := st.ClientStrategies.ResolveForDevice(ctx, d1)
	if err != nil {
		t.Fatal(err)
	}
	if s1.ID != def.ID {
		t.Fatalf("expected default %d, got %d", def.ID, s1.ID)
	}

	// 2. Device in group with strategy -> group strategy
	d2 := &Device{RustdeskID: "dev2", DeviceGroupID: &group.ID}
	if err := st.Devices.Create(ctx, d2); err != nil {
		t.Fatal(err)
	}
	s2, err := st.ClientStrategies.ResolveForDevice(ctx, d2)
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID != groupCustom.ID {
		t.Fatalf("expected group strategy %d, got %d", groupCustom.ID, s2.ID)
	}

	// 3. Device in group with strategy, but has its own explicit strategy -> device strategy wins
	d3 := &Device{RustdeskID: "dev3", DeviceGroupID: &group.ID, StrategyID: &custom.ID}
	if err := st.Devices.Create(ctx, d3); err != nil {
		t.Fatal(err)
	}
	s3, err := st.ClientStrategies.ResolveForDevice(ctx, d3)
	if err != nil {
		t.Fatal(err)
	}
	if s3.ID != custom.ID {
		t.Fatalf("expected device override %d, got %d", custom.ID, s3.ID)
	}
}
