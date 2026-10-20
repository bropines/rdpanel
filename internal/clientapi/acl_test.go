package clientapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/bropines/rdpanel/internal/store"
)

// With connection rules, a shared-group member keeps access to the group's
// devices (docs/acl-policy.md), and paging covers the filtered list: every
// page reports the same total and the pages add up to it.
func TestAccessiblePeersWithRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	team := &store.UserGroup{Name: "team", Visibility: store.VisibilityShared}
	if err := e.st.UserGroups.Create(ctx, team); err != nil {
		t.Fatal(err)
	}
	ann := e.user("ann", "pw-ann-12345", func(u *store.User) { u.UserGroupID = &team.ID })
	ben := e.user("ben", "pw-ben-12345", func(u *store.User) { u.UserGroupID = &team.ID })
	vault := &store.DeviceGroup{Name: "Vault"}
	if err := e.st.DeviceGroups.Create(ctx, vault); err != nil {
		t.Fatal(err)
	}
	// ann owns 2 devices, ben 4 (two of them in the denied "Vault" group).
	for i := range 6 {
		d := &store.Device{RustdeskID: fmt.Sprintf("20000000%d", i), OwnerUserID: &ben.ID}
		if i < 2 {
			d.OwnerUserID = &ann.ID
		}
		if i == 2 || i == 4 {
			d.DeviceGroupID = &vault.ID
		}
		if err := e.st.Devices.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// One rule exists, so unmatched access is denied unless owner/shared.
	if err := e.st.ConnectionACLs.Create(ctx, &store.ConnectionACL{Name: "no vault", DeviceGroupID: &vault.ID,
		Action: store.ACLActionDeny, Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tok := e.login("ann", "pw-ann-12345", "", "")

	var seen []string
	for current := 1; current <= 3; current++ {
		r := e.mustDo("GET", fmt.Sprintf("/api/peers?current=%d&pageSize=2&accessible=&status=1", current), "", tok, 200)
		var out struct {
			Total int `json:"total"`
			Data  []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.body), &out); err != nil {
			t.Fatal(err)
		}
		if out.Total != 4 {
			t.Fatalf("page %d: total %d, want 4: %s", current, out.Total, r.body)
		}
		for _, p := range out.Data {
			seen = append(seen, p.ID)
		}
	}
	sort.Strings(seen)
	if fmt.Sprint(seen) != "[200000000 200000001 200000003 200000005]" {
		t.Fatalf("peers over all pages: %v", seen)
	}
}
