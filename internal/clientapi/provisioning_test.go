package clientapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/store"
)

func TestDeploy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.user("admin", "pw-admin-123", func(u *store.User) { u.Role = store.RoleAdmin })
	e.user("plain", "pw-plain-123", nil)
	admin := e.login("admin", "pw-admin-123", "", "")
	plain := e.login("plain", "pw-plain-123", "", "")
	pub, _, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{1}, 64)))
	pk := base64.StdEncoding.EncodeToString(pub)
	body := fmt.Sprintf(`{"id":"123456789","uuid":"dXVpZA==","pk":%q}`, pk)

	e.golden("unauthorized", e.do("POST", "/api/devices/deploy", body, plain), 401)
	e.golden("unauthorized", e.do("POST", "/api/devices/deploy", body, ""), 401)
	e.golden("deploy_ok", e.do("POST", "/api/devices/deploy", body, admin), 200)
	e.golden("deploy_ok", e.do("POST", "/api/devices/deploy", body, admin), 200) // idempotent
	d, _ := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if !bytes.Equal(d.PublicKey, pub) || d.UUID != "dXVpZA==" {
		t.Fatalf("deployed %+v", d)
	}
	e.golden("deploy_id_taken", e.do("POST", "/api/devices/deploy", fmt.Sprintf(`{"id":"123456789","uuid":"b3RoZXI=","pk":%q}`, pk), admin), 200)
	e.golden("deploy_invalid", e.do("POST", "/api/devices/deploy", `{"id":"1","uuid":"dQ==","pk":"short"}`, admin), 200)
	e.golden("deploy_invalid", e.do("POST", "/api/devices/deploy", "", admin), 200)
	_ = e.st.Settings.Set(ctx, SettingDeployEnabled, false, nil)
	e.golden("deploy_not_enabled", e.do("POST", "/api/devices/deploy", body, admin), 200)
	if entries, _ := e.st.AdminAudit.List(ctx, store.AuditFilter{ActionPrefix: "device."}); len(entries) != 2 {
		t.Fatalf("admin audit entries %d", len(entries))
	}
}

func TestSwitchGrant(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{2}, 64)))
	_ = e.st.Devices.Create(ctx, &store.Device{RustdeskID: "123456789", UUID: "dQ==", PublicKey: pub})
	grant := func(id string, ts int64, key ed25519.PrivateKey) string {
		tsS := strconv.FormatInt(ts, 10)
		sig := ed25519.Sign(key, []byte("switch-grant\x00"+id+"\x00verif\x00"+tsS))
		b, _ := json.Marshal(map[string]string{"id": id, "switch_code_verifier": "verif", "timestamp": tsS,
			"signature": base64.StdEncoding.EncodeToString(sig)})
		return string(b)
	}
	now := e.clk.now().Unix()
	e.golden("switch_grant_accepted", e.do("POST", "/api/switch-grant", grant("123456789", now, priv), ""), 200)
	e.golden("switch_grant_skew", e.do("POST", "/api/switch-grant", grant("123456789", now-3600, priv), ""), 200)
	_, other, _ := ed25519.GenerateKey(bytes.NewReader(bytes.Repeat([]byte{3}, 64)))
	e.golden("switch_grant_rejected", e.do("POST", "/api/switch-grant", grant("123456789", now, other), ""), 200)
	e.golden("switch_grant_rejected", e.do("POST", "/api/switch-grant", grant("555", now, priv), ""), 200)
}

func TestDevicesCLI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.user("admin", "pw-admin-123", func(u *store.User) { u.Role = store.RoleAdmin })
	bob := e.user("bob", "pw-bob-12345", nil)
	tok := e.login("admin", "pw-admin-123", "", "")
	g := &store.DeviceGroup{Name: "Office"}
	_ = e.st.DeviceGroups.Create(ctx, g)
	book := &store.AddressBook{OwnerUserID: admin.ID, Name: "Fleet"}
	_ = e.st.AddressBooks.Create(ctx, book)
	e.device("123456789", "dXVpZA==")

	e.empty("POST", "/api/devices/cli", `{"id":"123456789","uuid":"dXVpZA==","user_name":"bob","device_group_name":"office",
		"note":"desk 4","device_name":"PC-4","address_book_name":"fleet","address_book_tag":"floor2","address_book_alias":"Desk 4",
		"address_book_password":"pw"}`, tok)
	d, _ := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if *d.OwnerUserID != bob.ID || *d.DeviceGroupID != g.ID || d.Note != "desk 4" || d.Hostname != "PC-4" {
		t.Fatalf("assigned %+v", d)
	}
	en, err := e.st.AddressBooks.GetEntry(ctx, book.ID, "123456789")
	if err != nil || en.Alias != "Desk 4" || en.Password != "pw" || len(en.Tags) != 1 || en.Tags[0] != "floor2" {
		t.Fatalf("entry %+v %v", en, err)
	}
	for _, c := range []struct{ body, want string }{
		{`{"id":"000","uuid":"x","note":"n"}`, "Device not found"},
		{`{"id":"123456789","uuid":"dXVpZA==","user_name":"ghost"}`, "User not found"},
		{`{"id":"123456789","uuid":"dXVpZA==","device_group_name":"none"}`, "Device group not found"},
		{`{"id":"123456789","uuid":"dXVpZA==","address_book_name":"none"}`, "Address book not found"},
		{`{"id":"123456789","uuid":"dXVpZA==","strategy_name":"s"}`, "Strategies are not supported by this server"},
	} {
		r := e.do("POST", "/api/devices/cli", c.body, tok)
		if r.code != 400 || r.body != c.want {
			t.Fatalf("%s: %d %q", c.body, r.code, r.body)
		}
	}
	e.mustDo("POST", "/api/devices/cli", `{"id":"123456789","uuid":"dXVpZA==","note":"x"}`, "", 401)
}

func TestRecord(t *testing.T) {
	e := newEnv(t)
	e.golden("record", e.do("POST", "/api/record?type=new&file=a.webm", "", ""), 200)
}

func TestAccessible(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	team := &store.UserGroup{Name: "team", Visibility: store.VisibilityShared}
	solo := &store.UserGroup{Name: "solo"}
	_ = e.st.UserGroups.Create(ctx, team)
	_ = e.st.UserGroups.Create(ctx, solo)
	e.user("root", "pw-root-12345", func(u *store.User) { u.Role = store.RoleAdmin })
	ann := e.user("ann", "pw-ann-12345", func(u *store.User) { u.UserGroupID = &team.ID })
	ben := e.user("ben", "pw-ben-12345", func(u *store.User) { u.UserGroupID = &team.ID })
	cat := e.user("cat", "pw-cat-12345", func(u *store.User) { u.UserGroupID = &solo.ID })
	e.user("dis", "pw-dis-12345", func(u *store.User) { u.Status = store.UserDisabled; u.UserGroupID = &team.ID })
	office := &store.DeviceGroup{Name: "Office"}
	_ = e.st.DeviceGroups.Create(ctx, office)
	_ = e.st.DeviceGroups.Create(ctx, &store.DeviceGroup{Name: "Empty"})
	for i, owner := range []*store.User{ann, ben, cat} {
		id := fmt.Sprintf("10000000%d", i)
		e.device(id, "dQ==")
		d, _ := e.st.Devices.GetByRustdeskID(ctx, id)
		d.OwnerUserID = &owner.ID
		if i == 0 {
			d.DeviceGroupID = &office.ID
			d.Note = "ann's"
		}
		_ = e.st.Devices.Update(ctx, d)
	}
	root := e.login("root", "pw-root-12345", "", "")
	annTok := e.login("ann", "pw-ann-12345", "", "")
	catTok := e.login("cat", "pw-cat-12345", "", "")

	q := "?current=1&pageSize=100&accessible=&status=1"
	e.golden("users_admin", e.do("GET", "/api/users"+q, "", root), 200)
	e.golden("users_shared_group", e.do("GET", "/api/users"+q, "", annTok), 200)
	e.golden("users_self", e.do("GET", "/api/users"+q, "", catTok), 200)
	e.golden("peers_admin", e.do("GET", "/api/peers"+q, "", root), 200)
	e.golden("peers_shared_group", e.do("GET", "/api/peers"+q, "", annTok), 200)
	e.golden("peers_self", e.do("GET", "/api/peers"+q, "", catTok), 200)
	e.golden("device_groups_admin", e.do("GET", "/api/device-group/accessible?current=1&pageSize=100", "", root), 200)
	e.golden("device_groups_shared_group", e.do("GET", "/api/device-group/accessible?current=1&pageSize=100", "", annTok), 200)
	e.golden("device_groups_self", e.do("GET", "/api/device-group/accessible?current=1&pageSize=100", "", catTok), 200)
	// Paging uses current (1-based) and pageSize; total covers everything.
	e.golden("peers_admin_page2", e.do("GET", "/api/peers?current=2&pageSize=2&accessible=&status=1", "", root), 200)
	e.golden("unauthorized", e.do("GET", "/api/peers"+q, "", ""), 401)
	_ = time.Second
}
