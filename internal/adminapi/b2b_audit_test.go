package adminapi

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/store"
)

func TestAuditConnectionsAndFiles(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("root", store.RoleAdmin)
	bob, bu := e.as("bob", store.RoleUser)
	mine := &store.Device{RustdeskID: "111111", Alias: "bob-pc", OwnerUserID: &bu.ID}
	other := &store.Device{RustdeskID: "222222"}
	for _, d := range []*store.Device{mine, other} {
		if err := e.st.Devices.Create(e.ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ended := t0.Add(time.Hour)
	for i, a := range []*store.ConnectionAudit{
		{DeviceID: "111111", ConnType: 0, ControllerName: "Carol", ControllerIP: "10.0.0.9", SessionID: "18446744073709551615", StartedAt: t0, EndedAt: &ended},
		{DeviceID: "111111", ConnType: 1, StartedAt: t0.Add(2 * time.Hour)},
		{DeviceID: "222222", ConnType: 0, StartedAt: t0.Add(3 * time.Hour)},
		{DeviceID: "999999", ConnType: 4, StartedAt: t0.Add(4 * time.Hour)},
	} {
		if err := e.st.ConnectionAudits.Create(e.ctx, a); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	code, m, _ := admin.do(http.MethodGet, "/audit/connections", nil)
	want(t, "admin connections", code, 200, m)
	l := b2bItems(m)
	if b2bNum(m["total"]) != 4 || l[0]["device_rustdesk_id"] != "999999" || l[0]["device_id"] != nil {
		t.Fatalf("admin connections: %v", m)
	}
	code, m, _ = bob.do(http.MethodGet, "/audit/connections?sort=started_at", nil)
	l = b2bItems(m)
	if code != 200 || b2bNum(m["total"]) != 2 || l[0]["controller_name"] != "Carol" || l[0]["device_alias"] != "bob-pc" ||
		b2bNum(l[0]["device_id"]) != mine.ID || l[0]["session_id"] != "18446744073709551615" || l[0]["ended_at"] == nil {
		t.Fatalf("bob connections: %v", m)
	}
	if l[1]["controller_name"] != nil || l[1]["ended_at"] != nil {
		t.Fatalf("empty strings should be null: %v", l[1])
	}
	for q, wantTotal := range map[string]int64{
		"?conn_type=1":  1,
		"?active=true":  3,
		"?active=false": 1,
		"?device_id=" + strconv.FormatInt(other.ID, 10): 1,
		"?q=carol": 1,
		"?from=" + url.QueryEscape(t0.Add(2*time.Hour).Format(time.RFC3339)): 3,
		"?to=" + url.QueryEscape(t0.Add(2*time.Hour).Format(time.RFC3339)):   1,
		"?sort=-ended_at&page_size=1":                                        4,
	} {
		code, m, _ := admin.do(http.MethodGet, "/audit/connections"+q, nil)
		if code != 200 || b2bNum(m["total"]) != wantTotal {
			t.Fatalf("filter %s: %d %v", q, code, m)
		}
	}
	for _, q := range []string{"?conn_type=9", "?active=maybe", "?from=yesterday", "?sort=controller", "?page=0"} {
		code, m, _ := admin.do(http.MethodGet, "/audit/connections"+q, nil)
		wantErr(t, "invalid "+q, code, 400, m, "validation_failed")
	}

	for _, f := range []*store.FileTransferAudit{
		{DeviceID: "111111", Direction: 1, Path: "/tmp/a.txt", IsFile: true, FileCount: 1, Info: `{"files":[["a.txt",3]]}`},
		{DeviceID: "222222", Direction: 0, Path: "C:\\b", Info: `[1,2]`},
	} {
		if err := e.st.FileTransferAudits.Create(e.ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	code, m, _ = bob.do(http.MethodGet, "/audit/files", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["path"] != "/tmp/a.txt" || l[0]["is_file"] != true {
		t.Fatalf("bob files: %v", m)
	}
	if info, _ := l[0]["info"].(map[string]any); info["files"] == nil {
		t.Fatalf("info: %v", l[0])
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/files?direction=0", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 {
		t.Fatalf("admin files: %v", m)
	}
	if info, _ := l[0]["info"].(map[string]any); info["value"] == nil {
		t.Fatalf("non-object info must be wrapped: %v", l[0])
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/files?direction=2", nil)
	wantErr(t, "direction", code, 400, m, "validation_failed")
}

func TestAuditAlarmsLoginsAdmin(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, au := e.as("root", store.RoleAdmin)
	bob, bu := e.as("bob", store.RoleUser)
	for _, a := range []*store.AlarmAudit{
		{DeviceID: "111111", AlarmType: 1, Info: `{"ip":"10.1.1.1","id":"x"}`},
		{DeviceID: "222222", AlarmType: 2},
	} {
		if err := e.st.AlarmAudits.Create(e.ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	code, m, _ := bob.do(http.MethodGet, "/audit/alarms", nil)
	wantErr(t, "user alarms", code, 403, m, "forbidden")
	code, m, _ = admin.do(http.MethodGet, "/audit/alarms?alarm_type=1", nil)
	l := b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["ip"] != "10.1.1.1" {
		t.Fatalf("alarms: %v", m)
	}

	// Logins: the two logins above plus a failed client login for bob.
	if err := e.st.LoginEvents.Record(e.ctx, &store.LoginEvent{UserID: &bu.ID, UsernameAttempted: "bob",
		Method: store.LoginMethodPassword, Result: store.LoginFailure, FailureReason: "bad_credentials",
		ClientKind: "client", DeviceRustdeskID: "111111"}); err != nil {
		t.Fatal(err)
	}
	code, m, _ = bob.do(http.MethodGet, "/audit/logins", nil)
	l = b2bItems(m)
	if code != 200 || b2bNum(m["total"]) != 2 {
		t.Fatalf("bob logins: %v", m)
	}
	for _, it := range l {
		if u, _ := it["user"].(map[string]any); u["username"] != "bob" {
			t.Fatalf("bob sees foreign login: %v", it)
		}
	}
	code, m, _ = bob.do(http.MethodGet, "/audit/logins?user_id="+strconv.FormatInt(au.ID, 10), nil)
	wantErr(t, "bob foreign user_id", code, 403, m, "forbidden")
	code, m, _ = admin.do(http.MethodGet, "/audit/logins", nil)
	if code != 200 || b2bNum(m["total"]) != 3 {
		t.Fatalf("admin logins: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/logins?client_kind=client&result=failure&method=password", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["client_kind"] != "client" || l[0]["failure_reason"] != "bad_credentials" {
		t.Fatalf("filtered logins: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/logins?client_kind=web", nil)
	if l = b2bItems(m); code != 200 || len(l) != 2 || l[0]["client_kind"] != "web" {
		t.Fatalf("web logins: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/logins?result=maybe", nil)
	wantErr(t, "bad result", code, 400, m, "validation_failed")

	// Admin audit: admin only, filters.
	admin.do(http.MethodPost, "/address-books", map[string]any{"name": "Audited"})
	code, m, _ = bob.do(http.MethodGet, "/audit/admin", nil)
	wantErr(t, "user admin audit", code, 403, m, "forbidden")
	code, m, _ = admin.do(http.MethodGet, "/audit/admin?action=address_book.&actor_user_id="+strconv.FormatInt(au.ID, 10), nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["action"] != "address_book.create" || l[0]["actor_name"] != "root" ||
		l[0]["target_type"] != "address_book" || l[0]["target_label"] != "Audited" {
		t.Fatalf("admin audit: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/admin?q=nothing-matches", nil)
	if code != 200 || b2bNum(m["total"]) != 0 || len(b2bItems(m)) != 0 {
		t.Fatalf("q: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/audit/admin?target_type=address_book&sort=created_at", nil)
	if code != 200 || b2bNum(m["total"]) != 1 {
		t.Fatalf("target_type: %v", m)
	}
}
