package store

import (
	"context"
	"errors"
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

	// The templates are seeded, none of them as the default (00020): a
	// device without a strategy gets no strategy options.
	if def, err := st.ClientStrategies.GetDefault(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("seeded default strategy: %+v %v", def, err)
	}
	office, err := st.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	if office.ConfigOptions["approve-mode"] != "both" {
		t.Fatalf("expected approve-mode 'both', got %q", office.ConfigOptions["approve-mode"])
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
	def, err := st.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ClientStrategies.SetDefault(ctx, def.ID); err != nil {
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

	// 1. Device with no group, no strategy -> none, or the default once set
	d1 := &Device{RustdeskID: "dev1"}
	if err := st.Devices.Create(ctx, d1); err != nil {
		t.Fatal(err)
	}
	if s1, err := st.ClientStrategies.ResolveForDevice(ctx, d1); err != nil || s1 != nil {
		t.Fatalf("no default: %+v %v", s1, err)
	}
	def, err := st.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ClientStrategies.SetDefault(ctx, def.ID); err != nil {
		t.Fatal(err)
	}
	s1, err := st.ClientStrategies.ResolveForDevice(ctx, d1)
	if err != nil {
		t.Fatal(err)
	}
	if s1.ID != def.ID || s1.ConfigOptions["approve-mode"] != "both" {
		t.Fatalf("expected default %d, got %+v", def.ID, s1)
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

	// The heartbeat path does not count devices (a scan of the devices table
	// per heartbeat); Get still does.
	if s3.DeviceCount != 0 {
		t.Fatalf("ResolveForDevice computed device_count %d", s3.DeviceCount)
	}
	if full, err := st.ClientStrategies.Get(ctx, custom.ID); err != nil || full.DeviceCount != 1 {
		t.Fatalf("Get device_count: %+v %v", full, err)
	}
}

// A failed attempt to change the default never leaves the system without
// the current default (the old code cleared all defaults first).
func TestClientStrategyDefaultChangesAreAtomic(t *testing.T) {
	st, _ := newTestStore(t)
	ctx := context.Background()
	office, err := st.ClientStrategies.GetByName(ctx, "Standard Office")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ClientStrategies.SetDefault(ctx, office.ID); err != nil {
		t.Fatal(err)
	}
	stillDefault := func(what string) {
		t.Helper()
		def, err := st.ClientStrategies.GetDefault(ctx)
		if err != nil || def.ID != office.ID {
			t.Fatalf("%s: default is now %+v %v", what, def, err)
		}
	}

	if err := st.ClientStrategies.SetDefault(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("set default of a missing id: %v", err)
	}
	stillDefault("SetDefault(missing)")

	dup := &ClientStrategy{Name: "Strict Security", IsDefault: true}
	if err := st.ClientStrategies.Create(ctx, dup); !errors.Is(err, ErrConflict) {
		t.Fatalf("create duplicate: %v", err)
	}
	stillDefault("Create(duplicate)")

	if err := st.ClientStrategies.Update(ctx, &ClientStrategy{ID: 9999, Name: "Ghost", IsDefault: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	stillDefault("Update(missing)")

	// A successful change moves the default.
	kiosk := &ClientStrategy{Name: "Kiosk", IsDefault: true}
	if err := st.ClientStrategies.Create(ctx, kiosk); err != nil {
		t.Fatal(err)
	}
	if def, err := st.ClientStrategies.GetDefault(ctx); err != nil || def.ID != kiosk.ID {
		t.Fatalf("new default: %+v %v", def, err)
	}
	list, _ := st.ClientStrategies.List(ctx)
	n := 0
	for _, s := range list {
		if s.IsDefault {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d defaults", n)
	}
}
