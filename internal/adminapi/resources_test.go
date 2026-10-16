package adminapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/store"
)

// ---- fakes ----

type fakePresence struct {
	mu     sync.Mutex
	online map[string]time.Time
	health PresenceHealth
}

func (f *fakePresence) set(id string, since time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[id] = since
}

func (f *fakePresence) OnlineIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for id := range f.online {
		out = append(out, id)
	}
	return out
}

func (f *fakePresence) DevicePresence(id string) (bool, time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.online[id]
	return ok, s, ok
}

func (f *fakePresence) PresenceHealth() PresenceHealth { return f.health }

type fakeServers struct{ hbbs, hbbr ChannelStatus }

func (f fakeServers) ChannelStatus() (ChannelStatus, ChannelStatus) { return f.hbbs, f.hbbr }
func (f fakeServers) SettingsInSync(context.Context) (bool, error)  { return true, nil }

type fakeKicks struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeKicks) Kick(id string, _ ...int32) {
	f.mu.Lock()
	f.ids = append(f.ids, id)
	f.mu.Unlock()
}

func (f *fakeKicks) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

type pubEvent struct {
	t   events.Type
	d   any
	aud events.Audience
}

type fakeEvents struct {
	mu  sync.Mutex
	evs []pubEvent
}

func (f *fakeEvents) Publish(t events.Type, d any, aud events.Audience) {
	f.mu.Lock()
	f.evs = append(f.evs, pubEvent{t, d, aud})
	f.mu.Unlock()
}

func (f *fakeEvents) list() []pubEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pubEvent(nil), f.evs...)
}

// ---- environment ----

type renv struct {
	*tenv
	presence *fakePresence
	kicks    *fakeKicks
	events   *fakeEvents
	admin    *client
	adminU   *store.User
}

// newREnv is newTEnv with the resource collaborators faked and an admin
// ("root") logged in.
func newREnv(t *testing.T, withPresence bool) *renv {
	t.Helper()
	e := &renv{tenv: newTEnv(t), kicks: &fakeKicks{}, events: &fakeEvents{}}
	e.deps.Kicks, e.deps.Events = e.kicks, e.events
	if withPresence {
		e.presence = &fakePresence{online: map[string]time.Time{},
			health: PresenceHealth{Polls: 3, Reachable: true, Since: e.clk.now().Add(-time.Hour), LastOK: e.clk.now(), RTT: 3 * time.Millisecond}}
		e.deps.Presence = e.presence
		e.deps.Servers = fakeServers{hbbs: ChannelStatus{Known: true, Reachable: true, RTT: time.Millisecond},
			hbbr: ChannelStatus{Known: true, LastError: "connection refused"}}
	}
	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) { Register(r, e.deps) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.srv = srv
	e.adminU = e.user("root", "root password", store.RoleAdmin)
	e.admin = e.client()
	if code, m := e.admin.login("root", "root password"); code != 200 {
		t.Fatalf("admin login: %d %v", code, m)
	}
	return e
}

func (e *renv) login(name string, role store.Role) (*client, *store.User) {
	e.t.Helper()
	u := e.user(name, name+" password", role)
	c := e.client()
	if code, m := c.login(name, name+" password"); code != 200 {
		e.t.Fatalf("login %s: %d %v", name, code, m)
	}
	return c, u
}

func (e *renv) device(rid string, mut func(d *store.Device)) *store.Device {
	e.t.Helper()
	d := &store.Device{RustdeskID: rid, Hostname: "host-" + rid}
	if mut != nil {
		mut(d)
	}
	if err := e.st.Devices.Create(context.Background(), d); err != nil {
		e.t.Fatal(err)
	}
	return d
}

func (e *renv) auditCount(action string) int {
	var n int
	_ = e.st.Querier().QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit WHERE action = ?`, action).Scan(&n)
	return n
}

func items(m map[string]any) []map[string]any {
	raw, _ := m["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

func ids(m map[string]any, key string) []string {
	var out []string
	for _, it := range items(m) {
		out = append(out, fmt.Sprint(it[key]))
	}
	return out
}

func num(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// ---- tests ----

func TestResourceAuthz(t *testing.T) {
	e := newREnv(t, false)
	usr, _ := e.login("bob", store.RoleUser)
	anon := e.client()
	adminOnly := []struct{ method, path string }{
		{"POST", "/devices/bulk"}, {"DELETE", "/devices/1"}, {"POST", "/devices/1/kick"},
		{"GET", "/users"}, {"POST", "/users"}, {"GET", "/users/1"}, {"PATCH", "/users/1"}, {"DELETE", "/users/1"},
		{"POST", "/users/1/password"}, {"POST", "/users/1/totp/reset"}, {"GET", "/users/1/sessions"},
		{"POST", "/users/1/sessions/revoke"}, {"DELETE", "/users/1/sessions/1"}, {"DELETE", "/users/1/identities/1"},
		{"GET", "/user-groups"}, {"POST", "/user-groups"}, {"PATCH", "/user-groups/1"}, {"DELETE", "/user-groups/1"},
		{"GET", "/device-groups"}, {"POST", "/device-groups"}, {"PATCH", "/device-groups/1"}, {"DELETE", "/device-groups/1"},
		{"GET", "/settings"}, {"PATCH", "/settings"}, {"PUT", "/settings/logo"}, {"DELETE", "/settings/logo"},
	}
	for _, rt := range adminOnly {
		if code, m, _ := anon.do(rt.method, rt.path, map[string]any{}); code != 401 && !(code == 403 && errCode(m) == "csrf_failed") {
			t.Errorf("anonymous %s %s: %d %v", rt.method, rt.path, code, m)
		}
		if code, m, _ := usr.do(rt.method, rt.path, map[string]any{}); code != 403 || errCode(m) != "forbidden" {
			t.Errorf("user %s %s: %d %v", rt.method, rt.path, code, m)
		}
	}
	userRoutes := []string{"/dashboard", "/devices", "/devices/facets", "/devices/export.csv", "/devices/1",
		"/lookup/users", "/lookup/user-groups"}
	for _, p := range userRoutes {
		if code, _, _ := anon.do("GET", p, nil); code != 401 {
			t.Errorf("anonymous GET %s: %d", p, code)
		}
	}
	// Anonymous GET with a valid CSRF cookie still gets 401 on mutations.
	anon.do("GET", "/auth/csrf", nil)
	if code, _, _ := anon.do("POST", "/users", map[string]any{}); code != 401 {
		t.Errorf("anonymous POST /users: %d", code)
	}
	// CSRF is enforced for authenticated mutations.
	e.admin.noCSRF = true
	if code, m, _ := e.admin.do("POST", "/user-groups", map[string]any{"name": "x"}); code != 403 || errCode(m) != "csrf_failed" {
		t.Errorf("csrf: %d %v", code, m)
	}
	e.admin.noCSRF = false
	// Public branding.
	if code, m, _ := anon.do("GET", "/branding", nil); code != 200 || m["title"] != DefaultBrandingTitle || m["logo_url"] != nil {
		t.Errorf("branding: %d %v", code, m)
	}
}

func TestDevicesListFiltersSortPaging(t *testing.T) {
	e := newREnv(t, true)
	ctx := context.Background()
	_, bob := e.login("bob", store.RoleUser)
	g := &store.DeviceGroup{Name: "Office"}
	_ = e.st.DeviceGroups.Create(ctx, g)
	now := e.clk.now()
	old := now.Add(-48 * time.Hour)
	e.device("100", func(d *store.Device) {
		d.OS, d.ClientVersion, d.Alias, d.OwnerUserID, d.LastSeenAt = "windows / Windows 11 Pro", "1.4.0", "Zeta", &bob.ID, &now
	})
	e.device("200", func(d *store.Device) {
		d.OS, d.ClientVersion, d.Hostname, d.DeviceGroupID, d.LastSeenAt = "linux / Ubuntu 22.04", "1.3.9", "alpha", &g.ID, &old
	})
	e.device("300", func(d *store.Device) { d.OS, d.ClientVersion, d.Note = "Mac OS 14", "1.4.0", "50% off_x" })
	e.presence.set("100", now.Add(-time.Minute))
	e.presence.set("300", now.Add(-2*time.Minute))

	get := func(qs string) []string {
		t.Helper()
		code, m, _ := e.admin.do("GET", "/devices?"+qs, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", qs, code, m)
		}
		return ids(m, "rustdesk_id")
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	eq("default sort -last_seen_at", get(""), "100", "200", "300")
	eq("online", get("online=true&sort=rustdesk_id"), "100", "300")
	eq("offline", get("online=false"), "200")
	eq("owner=id", get(fmt.Sprintf("owner=%d", bob.ID)), "100")
	eq("owner=none", get("owner=none&sort=rustdesk_id"), "200", "300")
	eq("owner=me", get("owner=me"))
	eq("group", get(fmt.Sprintf("device_group=%d", g.ID)), "200")
	eq("group none", get("device_group=none&sort=-rustdesk_id"), "300", "100")
	eq("os windows", get("os=windows"), "100")
	eq("os macos", get("os=macos"), "300")
	eq("version", get("version=1.4.0&sort=rustdesk_id"), "100", "300")
	eq("seen_after", get("seen_after="+now.Add(-time.Hour).Format(time.RFC3339)), "100")
	eq("q alias case-insensitive", get("q=zet"), "100")
	eq("q like metachar", get("q=50%25"), "300")
	eq("q underscore literal", get("q=f_x"), "300")
	eq("sort hostname", get("sort=hostname"), "200", "100", "300")
	eq("sort -online", get("sort=-online&page_size=2"), "300", "100")

	code, m, _ := e.admin.do("GET", "/devices?sort=rustdesk_id&page=2&page_size=2", nil)
	if code != 200 || num(m["total"]) != 3 || num(m["page"]) != 2 || num(m["page_size"]) != 2 || len(items(m)) != 1 {
		t.Fatalf("paging: %d %v", code, m)
	}
	d := items(m)[0]
	if d["rustdesk_id"] != "300" || d["online"] != true || d["online_since"] == nil || d["os_family"] != "macos" || d["owner"] != nil {
		t.Fatalf("device dto: %v", d)
	}
	code, m, _ = e.admin.do("GET", "/devices?q=100", nil)
	if code != 200 {
		t.Fatalf("search: %d %v", code, m)
	}
	d = items(m)[0]
	if owner, _ := d["owner"].(map[string]any); owner == nil || owner["username"] != "bob" || num(d["owner_user_id"]) != bob.ID {
		t.Fatalf("owner embed: %v", d)
	}
	for _, bad := range []string{"page=0", "page_size=501", "sort=cpu", "online=maybe", "owner=x", "device_group=-1",
		"os=beos", "seen_after=yesterday", "q=" + strings.Repeat("a", 201)} {
		if code, m, _ := e.admin.do("GET", "/devices?"+bad, nil); code != 400 || errCode(m) != "validation_failed" {
			t.Errorf("%s: %d %v", bad, code, m)
		}
	}

	// Users see only their own devices.
	bobC := e.client()
	bobC.login("bob", "bob password")
	code, m, _ = bobC.do("GET", "/devices", nil)
	if code != 200 || strings.Join(ids(m, "rustdesk_id"), ",") != "100" {
		t.Fatalf("user scope: %d %v", code, m)
	}
	if _, m, _ := bobC.do("GET", "/devices?owner=none", nil); len(items(m)) != 0 {
		t.Fatalf("user owner=none must be empty: %v", m)
	}
	if _, m, _ := bobC.do("GET", fmt.Sprintf("/devices?owner=%d", e.adminU.ID), nil); len(items(m)) != 0 {
		t.Fatalf("user other owner must be empty: %v", m)
	}

	// Facets.
	code, m, _ = e.admin.do("GET", "/devices/facets", nil)
	if code != 200 || num(m["total"]) != 3 {
		t.Fatalf("facets: %d %v", code, m)
	}
	vers := m["versions"].([]any)
	if v := vers[0].(map[string]any); v["value"] != "1.4.0" || num(v["count"]) != 2 || len(vers) != 2 {
		t.Fatalf("versions facet: %v", vers)
	}
	if len(m["os"].([]any)) != 3 {
		t.Fatalf("os facet: %v", m["os"])
	}
	if _, m, _ = bobC.do("GET", "/devices/facets", nil); num(m["total"]) != 1 {
		t.Fatalf("user facets: %v", m)
	}
}

func TestDevicesHeartbeatFallback(t *testing.T) {
	e := newREnv(t, false)
	now := e.clk.now()
	recent, stale := now.Add(-10*time.Second), now.Add(-time.Minute)
	e.device("1", func(d *store.Device) { d.LastHeartbeatAt = &recent })
	e.device("2", func(d *store.Device) { d.LastHeartbeatAt = &stale })
	_, m, _ := e.admin.do("GET", "/devices?online=true", nil)
	if got := ids(m, "rustdesk_id"); len(got) != 1 || got[0] != "1" || items(m)[0]["online_since"] != nil {
		t.Fatalf("fallback: %v", m)
	}
}

func TestDeviceExportCSV(t *testing.T) {
	e := newREnv(t, true)
	e.device("100", func(d *store.Device) { d.Alias, d.Note = "=HYPERLINK(\"x\")", "+1 -2" })
	e.device("200", func(d *store.Device) { d.Alias = "@sum" })
	e.presence.set("200", e.clk.now())
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/admin/devices/export.csv?online=true", nil)
	resp, err := e.admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="devices-2026-10-15.csv"` {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	recs, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || strings.Join(recs[0], ",") != "rustdesk_id,alias,hostname,os,client_version,owner,group,online,last_seen_at,last_seen_ip,note" {
		t.Fatalf("csv: %v", recs)
	}
	if recs[1][0] != "200" || recs[1][1] != "'@sum" || recs[1][7] != "true" {
		t.Fatalf("row: %v", recs[1])
	}
	// All rows, injection-escaped.
	req, _ = http.NewRequest("GET", e.srv.URL+"/api/v1/admin/devices/export.csv", nil)
	resp2, err := e.admin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	recs, _ = csv.NewReader(resp2.Body).ReadAll()
	if len(recs) != 3 || recs[1][1] != `'=HYPERLINK("x")` || recs[1][10] != "'+1 -2" {
		t.Fatalf("escaping: %v", recs)
	}
	if got := csvSafe("-5"); got != "'-5" {
		t.Fatalf("csvSafe: %q", got)
	}
	if got := csvSafe("\tx"); got != "'\tx" || csvSafe("ok=1") != "ok=1" || csvSafe("") != "" {
		t.Fatalf("csvSafe tab: %q", got)
	}
}

func TestDeviceDetailUpdateDeleteKick(t *testing.T) {
	e := newREnv(t, true)
	ctx := context.Background()
	bobC, bob := e.login("bob", store.RoleUser)
	_, carol := e.login("carol", store.RoleUser)
	g := &store.DeviceGroup{Name: "Lab"}
	_ = e.st.DeviceGroups.Create(ctx, g)
	d := e.device("555", func(d *store.Device) { d.OwnerUserID, d.UUID = &bob.ID, "uuid-555" })
	other := e.device("666", func(d *store.Device) { d.OwnerUserID = &carol.ID })
	_ = e.st.ConnectionAudits.Create(ctx, &store.ConnectionAudit{DeviceID: "555", ControllerRustdeskID: "999", ConnType: 1})
	book, _ := e.st.AddressBooks.EnsurePersonal(ctx, bob.ID)
	_, _ = e.st.AddressBooks.UpsertEntry(ctx, book.ID, "555", store.ABEntryPatch{})
	cbook, _ := e.st.AddressBooks.EnsurePersonal(ctx, carol.ID)
	_, _ = e.st.AddressBooks.UpsertEntry(ctx, cbook.ID, "555", store.ABEntryPatch{})

	path := fmt.Sprintf("/devices/%d", d.ID)
	code, m, _ := bobC.do("GET", path, nil)
	if code != 200 || len(m["recent_connections"].([]any)) != 1 || len(m["address_books"].([]any)) != 1 {
		t.Fatalf("detail (user): %d %v", code, m)
	}
	if rc := m["recent_connections"].([]any)[0].(map[string]any); rc["controller_rustdesk_id"] != "999" || num(rc["device_id"]) != d.ID || num(rc["conn_type"]) != 1 {
		t.Fatalf("recent connection: %v", rc)
	}
	if _, m, _ = e.admin.do("GET", path, nil); len(m["address_books"].([]any)) != 2 {
		t.Fatalf("detail (admin) books: %v", m["address_books"])
	}
	if code, _, _ := bobC.do("GET", fmt.Sprintf("/devices/%d", other.ID), nil); code != 404 {
		t.Fatalf("other's device: %d", code)
	}
	if code, _, _ := e.admin.do("GET", "/devices/99999", nil); code != 404 {
		t.Fatalf("missing: %d", code)
	}

	// User edits alias/note of own device, not owner/group.
	if code, m, _ := bobC.do("PATCH", path, map[string]any{"alias": "Laptop", "note": "n"}); code != 200 || m["alias"] != "Laptop" {
		t.Fatalf("user patch: %d %v", code, m)
	}
	if code, m, _ := bobC.do("PATCH", path, map[string]any{"owner_user_id": nil}); code != 403 || errCode(m) != "forbidden" {
		t.Fatalf("user owner patch: %d %v", code, m)
	}
	if code, _, _ := bobC.do("PATCH", fmt.Sprintf("/devices/%d", other.ID), map[string]any{"alias": "x"}); code != 404 {
		t.Fatalf("user patch other: %d", code)
	}
	if code, m, _ := bobC.do("PATCH", path, map[string]any{"alias": strings.Repeat("a", 129)}); code != 400 {
		t.Fatalf("alias too long: %d %v", code, m)
	}
	if code, _, _ := bobC.do("PATCH", path, map[string]any{"bogus": 1}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	// Admin: owner/group, including validation.
	if code, m, _ := e.admin.do("PATCH", path, map[string]any{"owner_user_id": 4242}); code != 400 || m["error"].(map[string]any)["fields"] == nil {
		t.Fatalf("bad owner: %d %v", code, m)
	}
	code, m, _ = e.admin.do("PATCH", path, map[string]any{"owner_user_id": carol.ID, "device_group_id": g.ID})
	if code != 200 || num(m["owner_user_id"]) != carol.ID || m["device_group"].(map[string]any)["name"] != "Lab" {
		t.Fatalf("admin patch: %d %v", code, m)
	}
	evs := e.events.list()
	last := evs[len(evs)-1]
	if last.t != events.DeviceUpdated || len(last.aud.UserIDs) != 2 {
		t.Fatalf("event: %+v", last)
	}
	if dto, ok := last.d.(deviceDTO); !ok || dto.RustdeskID != "555" {
		t.Fatalf("event payload: %#v", last.d)
	}
	if code, m, _ = e.admin.do("PATCH", path, map[string]any{"owner_user_id": nil, "device_group_id": nil}); code != 200 || m["owner"] != nil || m["device_group"] != nil {
		t.Fatalf("unassign: %d %v", code, m)
	}
	if e.auditCount("device.update") != 3 {
		t.Fatalf("audit device.update: %d", e.auditCount("device.update"))
	}

	// Kick.
	if code, _, _ := e.admin.do("POST", path+"/kick", nil); code != 202 || strings.Join(e.kicks.list(), ",") != "555" {
		t.Fatalf("kick: %d %v", code, e.kicks.list())
	}
	if code, _, _ := e.admin.do("POST", "/devices/99999/kick", nil); code != 404 {
		t.Fatalf("kick missing: %d", code)
	}

	// Delete revokes client tokens bound to the device uuid.
	tok := &store.AuthToken{UserID: bob.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken("dev-tok"),
		ExpiresAt: e.clk.now().Add(time.Hour), DeviceUUID: "uuid-555"}
	_ = e.st.AuthTokens.Create(ctx, tok)
	if code, _, _ := e.admin.do("DELETE", path, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if got, _ := e.st.AuthTokens.Get(ctx, tok.ID); got.RevokedAt == nil {
		t.Fatal("device token not revoked")
	}
	if code, _, _ := e.admin.do("DELETE", path, nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
	if e.auditCount("device.delete") != 1 || e.auditCount("device.kick") != 1 {
		t.Fatal("audit delete/kick missing")
	}
}

func TestDeviceBulk(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	_, bob := e.login("bob", store.RoleUser)
	g := &store.DeviceGroup{Name: "Fleet"}
	_ = e.st.DeviceGroups.Create(ctx, g)
	d1 := e.device("1", func(d *store.Device) { d.UUID = "u1" })
	d2 := e.device("2", nil)

	bulk := func(body map[string]any) (int, map[string]any) {
		code, m, _ := e.admin.do("POST", "/devices/bulk", body)
		return code, m
	}
	code, m := bulk(map[string]any{"action": "assign_owner", "device_ids": []int64{d1.ID, d2.ID, 9999}, "owner_user_id": bob.ID})
	if code != 200 || num(m["affected"]) != 2 || len(m["errors"].([]any)) != 1 {
		t.Fatalf("assign_owner: %d %v", code, m)
	}
	if got, _ := e.st.Devices.Get(ctx, d2.ID); got.OwnerUserID == nil || *got.OwnerUserID != bob.ID {
		t.Fatal("owner not assigned")
	}
	if len(e.events.list()) != 2 {
		t.Fatalf("events: %d", len(e.events.list()))
	}
	code, m = bulk(map[string]any{"action": "assign_owner", "device_ids": []int64{d1.ID}, "owner_user_id": bob.ID})
	if code != 200 || num(m["affected"]) != 0 || num(m["skipped"]) != 1 {
		t.Fatalf("assign_owner noop: %d %v", code, m)
	}
	if code, m = bulk(map[string]any{"action": "assign_group", "device_ids": []int64{d1.ID}, "device_group_id": g.ID}); code != 200 || num(m["affected"]) != 1 {
		t.Fatalf("assign_group: %d %v", code, m)
	}
	if code, m = bulk(map[string]any{"action": "assign_group", "device_ids": []int64{d1.ID}, "device_group_id": nil}); code != 200 || num(m["affected"]) != 1 {
		t.Fatalf("assign_group null: %d %v", code, m)
	}
	if code, m = bulk(map[string]any{"action": "kick", "device_ids": []int64{d1.ID, d2.ID}}); code != 200 || len(e.kicks.list()) != 2 {
		t.Fatalf("kick: %d %v", code, m)
	}
	for _, bad := range []map[string]any{
		{"action": "explode", "device_ids": []int64{1}},
		{"action": "delete", "device_ids": []int64{}},
		{"action": "assign_owner", "device_ids": []int64{1}, "owner_user_id": 9999},
		{"action": "assign_group", "device_ids": []int64{1}, "device_group_id": 9999},
		{"action": "delete", "device_ids": []int64{1}, "extra": true},
	} {
		if code, m := bulk(bad); code != 400 {
			t.Errorf("bad bulk %v: %d %v", bad, code, m)
		}
	}
	tok := &store.AuthToken{UserID: bob.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken("t1"),
		ExpiresAt: e.clk.now().Add(time.Hour), DeviceUUID: "u1"}
	_ = e.st.AuthTokens.Create(ctx, tok)
	if code, m = bulk(map[string]any{"action": "delete", "device_ids": []int64{d1.ID, d2.ID}}); code != 200 || num(m["affected"]) != 2 {
		t.Fatalf("delete: %d %v", code, m)
	}
	if got, _ := e.st.AuthTokens.Get(ctx, tok.ID); got.RevokedAt == nil {
		t.Fatal("token not revoked by bulk delete")
	}
	for _, a := range []string{"device.bulk_assign_owner", "device.bulk_assign_group", "device.bulk_kick", "device.bulk_delete"} {
		if e.auditCount(a) == 0 {
			t.Errorf("missing audit %s", a)
		}
	}
}

func TestUsersCRUD(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	grp := &store.UserGroup{Name: "Support"}
	_ = e.st.UserGroups.Create(ctx, grp)

	code, m, _ := e.admin.do("POST", "/users", map[string]any{"username": "dave", "password": "dave password",
		"email": "dave@example.com", "display_name": "Dave", "role": "user", "user_group_id": grp.ID, "totp_required": true})
	if code != 201 || m["username"] != "dave" || m["has_password"] != true || m["user_group"].(map[string]any)["name"] != "Support" ||
		m["is_last_admin"] != false || m["totp_required"] != true {
		t.Fatalf("create: %d %v", code, m)
	}
	dave := num(m["id"])
	if code, m, _ := e.admin.do("POST", "/users", map[string]any{"username": "DAVE", "role": "user"}); code != 409 || errCode(m) != "conflict" {
		t.Fatalf("dup: %d %v", code, m)
	}
	code, m, _ = e.admin.do("POST", "/users", map[string]any{"username": "bad name", "password": "short", "role": "root", "email": "nope"})
	f, _ := m["error"].(map[string]any)["fields"].(map[string]any)
	if code != 400 || f["username"] == nil || f["password"] == nil || f["role"] == nil || f["email"] == nil {
		t.Fatalf("validation: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("POST", "/users", map[string]any{"username": "oidc-only", "role": "user"}); code != 201 {
		t.Fatalf("passwordless create: %d", code)
	}
	if e.auditCount("user.create") != 2 {
		t.Fatal("audit user.create")
	}
	var details string
	_ = e.st.Querier().QueryRowContext(ctx, `SELECT details FROM admin_audit WHERE action='user.create' LIMIT 1`).Scan(&details)
	if strings.Contains(details, "dave password") || strings.Contains(details, "argon2") {
		t.Fatalf("secret in audit: %s", details)
	}

	// List: filters, sort, paging.
	code, m, _ = e.admin.do("GET", "/users?sort=-username", nil)
	if code != 200 || strings.Join(ids(m, "username"), ",") != "root,oidc-only,dave" || num(m["total"]) != 3 {
		t.Fatalf("list: %d %v", code, m)
	}
	if _, m, _ = e.admin.do("GET", "/users?role=admin", nil); strings.Join(ids(m, "username"), ",") != "root" || items(m)[0]["is_last_admin"] != true {
		t.Fatalf("role filter: %v", m)
	}
	if _, m, _ = e.admin.do("GET", fmt.Sprintf("/users?user_group_id=%d", grp.ID), nil); strings.Join(ids(m, "username"), ",") != "dave" {
		t.Fatalf("group filter: %v", m)
	}
	if _, m, _ = e.admin.do("GET", "/users?q=EXAMPLE.com", nil); strings.Join(ids(m, "username"), ",") != "dave" {
		t.Fatalf("q: %v", m)
	}
	if _, m, _ = e.admin.do("GET", "/users?page=2&page_size=2", nil); len(items(m)) != 1 || num(m["total"]) != 3 {
		t.Fatalf("paging: %v", m)
	}
	if code, _, _ := e.admin.do("GET", "/users?status=gone", nil); code != 400 {
		t.Fatalf("bad status: %d", code)
	}

	// Detail with identities.
	prov := &store.IdentityProvider{Slug: "gh", Kind: store.ProviderGitHub, ClientID: "c", DisplayName: "GitHub", Enabled: true}
	if err := e.st.IdentityProviders.Create(ctx, prov); err != nil {
		t.Fatal(err)
	}
	ident := &store.UserIdentity{UserID: dave, ProviderID: prov.ID, Subject: "42", Email: "d@x.io", EmailVerified: true}
	_ = e.st.UserIdentities.Create(ctx, ident)
	code, m, _ = e.admin.do("GET", fmt.Sprintf("/users/%d", dave), nil)
	ids0 := m["identities"].([]any)
	if code != 200 || len(ids0) != 1 || ids0[0].(map[string]any)["provider_name"] != "GitHub" || ids0[0].(map[string]any)["username"] != nil ||
		num(m["address_book_count"]) != 0 || num(m["device_count"]) != 0 {
		t.Fatalf("detail: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("GET", "/users/9999", nil); code != 404 {
		t.Fatalf("missing user: %d", code)
	}

	// Patch.
	code, m, _ = e.admin.do("PATCH", fmt.Sprintf("/users/%d", dave), map[string]any{"display_name": "D.", "email": nil, "user_group_id": nil})
	if code != 200 || m["display_name"] != "D." || m["email"] != nil || m["user_group"] != nil {
		t.Fatalf("patch: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("PATCH", fmt.Sprintf("/users/%d", dave), map[string]any{"username": "root"}); code != 409 {
		t.Fatalf("rename conflict: %d", code)
	}
	if code, _, _ := e.admin.do("PATCH", fmt.Sprintf("/users/%d", dave), map[string]any{"password": "whatever123"}); code != 400 {
		t.Fatalf("password via patch: %d", code)
	}

	// Unlink identity: the only sign-in method of a passwordless user is protected.
	oidcOnly, _ := e.st.Users.GetByUsername(ctx, "oidc-only")
	ident2 := &store.UserIdentity{UserID: oidcOnly.ID, ProviderID: prov.ID, Subject: "43"}
	_ = e.st.UserIdentities.Create(ctx, ident2)
	if code, m, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d/identities/%d", oidcOnly.ID, ident2.ID), nil); code != 409 || errCode(m) != "last_login_method" {
		t.Fatalf("last login method: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d/identities/%d", oidcOnly.ID, ident.ID), nil); code != 404 {
		t.Fatalf("foreign identity: %d", code)
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d/identities/%d", dave, ident.ID), nil); code != 204 {
		t.Fatalf("unlink: %d", code)
	}

	// Delete.
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d", dave), nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d", dave), nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
	for _, a := range []string{"user.update", "user.delete", "user.identity_unlink"} {
		if e.auditCount(a) != 1 {
			t.Errorf("audit %s: %d", a, e.auditCount(a))
		}
	}
}

func TestLastAdminProtection(t *testing.T) {
	e := newREnv(t, false)
	root := fmt.Sprintf("/users/%d", e.adminU.ID)
	if code, m, _ := e.admin.do("PATCH", root, map[string]any{"role": "user"}); code != 409 || errCode(m) != "last_admin" {
		t.Fatalf("demote last admin: %d %v", code, m)
	}
	if code, m, _ := e.admin.do("PATCH", root, map[string]any{"status": "disabled"}); code != 409 || errCode(m) != "last_admin" {
		t.Fatalf("disable last admin: %d %v", code, m)
	}
	if code, m, _ := e.admin.do("DELETE", root, nil); code != 409 || errCode(m) != "self" {
		t.Fatalf("delete self: %d %v", code, m)
	}
	// A second admin may delete the first while another active admin remains,
	// but not the last one.
	second, secondU := e.login("second", store.RoleAdmin)
	if code, m, _ := e.admin.do("GET", root, nil); code != 200 || m["is_last_admin"] != false {
		t.Fatalf("is_last_admin with two admins: %v", m)
	}
	// A disabled admin does not count.
	_, third := e.login("third", store.RoleAdmin)
	if code, _, _ := e.admin.do("PATCH", fmt.Sprintf("/users/%d", third.ID), map[string]any{"status": "disabled"}); code != 200 {
		t.Fatalf("disable third: %d", code)
	}
	if code, _, _ := second.do("DELETE", root, nil); code != 204 {
		t.Fatalf("delete first admin: %d", code)
	}
	if code, m, _ := second.do("PATCH", fmt.Sprintf("/users/%d", secondU.ID), map[string]any{"role": "user"}); code != 409 || errCode(m) != "last_admin" {
		t.Fatalf("demote remaining admin: %d %v", code, m)
	}
	if code, _, _ := second.do("PATCH", fmt.Sprintf("/users/%d", third.ID), map[string]any{"role": "user"}); code != 200 {
		t.Fatalf("demote disabled admin: %d", code)
	}
}

func TestUserDisableRevokesSessions(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	bobC, bob := e.login("bob", store.RoleUser)
	tok := &store.AuthToken{UserID: bob.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken("c1"), ExpiresAt: e.clk.now().Add(time.Hour)}
	_ = e.st.AuthTokens.Create(ctx, tok)
	if code, _, _ := e.admin.do("PATCH", fmt.Sprintf("/users/%d", bob.ID), map[string]any{"status": "disabled"}); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code, _, _ := bobC.do("GET", "/auth/me", nil); code != 401 {
		t.Fatalf("disabled session: %d", code)
	}
	if got, _ := e.st.AuthTokens.Get(ctx, tok.ID); got.RevokedAt == nil {
		t.Fatal("client token not revoked")
	}
}

func TestUserPasswordTOTPSessions(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	bobC, bob := e.login("bob", store.RoleUser)
	bob2 := e.client()
	bob2.login("bob", "bob password")
	tok := &store.AuthToken{UserID: bob.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken("c1"),
		ExpiresAt: e.clk.now().Add(time.Hour), DeviceRustdeskID: "123", ClientPlatform: "Windows"}
	_ = e.st.AuthTokens.Create(ctx, tok)
	base := fmt.Sprintf("/users/%d", bob.ID)

	code, m, _ := e.admin.do("GET", base+"/sessions", nil)
	if code != 200 || len(items(m)) != 3 {
		t.Fatalf("sessions: %d %v", code, m)
	}
	kinds := map[string]int{}
	for _, s := range items(m) {
		kinds[s["kind"].(string)]++
		if s["current"] != false {
			t.Fatalf("current flag for another user's session: %v", s)
		}
	}
	if kinds["web"] != 2 || kinds["client"] != 1 {
		t.Fatalf("kinds: %v", kinds)
	}
	// Revoke one web session.
	webID := int64(0)
	for _, s := range items(m) {
		if s["kind"] == "web" {
			webID = num(s["id"])
			break
		}
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("%s/sessions/%d", base, webID), nil); code != 204 {
		t.Fatalf("revoke one: %d", code)
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("%s/sessions/%d", base, webID), nil); code != 404 {
		t.Fatalf("revoke one again: %d", code)
	}
	if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("/users/%d/sessions/%d", e.adminU.ID, tok.ID), nil); code != 404 {
		t.Fatalf("session of another user: %d", code)
	}
	// Revoke client tokens only.
	code, m, _ = e.admin.do("POST", base+"/sessions/revoke", map[string]any{"kind": "client"})
	if code != 200 || num(m["revoked"]) != 1 {
		t.Fatalf("revoke client: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("POST", base+"/sessions/revoke", map[string]any{"kind": "bogus"}); code != 400 {
		t.Fatalf("bad kind: %d", code)
	}

	// Password reset revokes the remaining sessions; the new password works.
	if code, _, _ := e.admin.do("POST", base+"/password", map[string]any{"password": "short"}); code != 400 {
		t.Fatalf("short password: %d", code)
	}
	if code, _, _ := e.admin.do("POST", base+"/password", map[string]any{"password": "brand new pass"}); code != 204 {
		t.Fatalf("reset: %d", code)
	}
	for _, c := range []*client{bobC, bob2} {
		if code, _, _ := c.do("GET", "/auth/me", nil); code != 401 {
			t.Fatalf("session survived reset: %d", code)
		}
	}
	if code, _ := e.client().login("bob", "brand new pass"); code != 200 {
		t.Fatalf("login with new password: %d", code)
	}
	if code, _, _ := e.admin.do("POST", "/users/9999/password", map[string]any{"password": "brand new pass"}); code != 404 {
		t.Fatalf("reset missing user: %d", code)
	}

	// TOTP reset.
	_ = e.st.TOTPSecrets.StartEnrolment(ctx, bob.ID, []byte("sealed"))
	if code, _, _ := e.admin.do("POST", base+"/totp/reset", nil); code != 204 {
		t.Fatalf("totp reset: %d", code)
	}
	if _, err := e.st.TOTPSecrets.Get(ctx, bob.ID); err == nil {
		t.Fatal("totp secret not removed")
	}
	for _, a := range []string{"user.password_reset", "user.totp_reset", "user.sessions_revoke", "user.session_revoke"} {
		if e.auditCount(a) != 1 {
			t.Errorf("audit %s: %d", a, e.auditCount(a))
		}
	}
}

func TestGroupsCRUD(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	for _, kind := range []struct{ path, countKey string }{{"/user-groups", "member_count"}, {"/device-groups", "device_count"}} {
		code, m, _ := e.admin.do("POST", kind.path, map[string]any{"name": "Beta", "note": "b"})
		if code != 201 || m["name"] != "Beta" || num(m[kind.countKey]) != 0 {
			t.Fatalf("%s create: %d %v", kind.path, code, m)
		}
		id := num(m["id"])
		e.admin.do("POST", kind.path, map[string]any{"name": "alpha"})
		if code, m, _ := e.admin.do("POST", kind.path, map[string]any{"name": "BETA"}); code != 409 || errCode(m) != "conflict" {
			t.Fatalf("%s dup: %d %v", kind.path, code, m)
		}
		if code, _, _ := e.admin.do("POST", kind.path, map[string]any{"name": " "}); code != 400 {
			t.Fatalf("%s empty name: %d", kind.path, code)
		}
		if kind.path == "/user-groups" {
			u := &store.User{Username: "member", UserGroupID: &id}
			_ = e.st.Users.Create(ctx, u)
			if code, m, _ := e.admin.do("POST", kind.path, map[string]any{"name": "x", "visibility": "public"}); code != 400 {
				t.Fatalf("bad visibility: %d %v", code, m)
			}
		} else {
			e.device("d1", func(d *store.Device) { d.DeviceGroupID = &id })
			if code, _, _ := e.admin.do("POST", kind.path, map[string]any{"name": "x", "visibility": "self"}); code != 400 {
				t.Fatalf("device group visibility must be refused: %d", code)
			}
		}
		code, m, _ = e.admin.do("GET", kind.path+"?sort=-"+kind.countKey, nil)
		if code != 200 || strings.Join(ids(m, "name"), ",") != "Beta,alpha" || num(items(m)[0][kind.countKey]) != 1 {
			t.Fatalf("%s list: %d %v", kind.path, code, m)
		}
		if _, m, _ = e.admin.do("GET", kind.path+"?q=alp", nil); strings.Join(ids(m, "name"), ",") != "alpha" {
			t.Fatalf("%s q: %v", kind.path, m)
		}
		if _, m, _ = e.admin.do("GET", kind.path, nil); strings.Join(ids(m, "name"), ",") != "alpha,Beta" {
			t.Fatalf("%s default sort: %v", kind.path, m)
		}
		if code, _, _ := e.admin.do("GET", kind.path+"?sort=bogus", nil); code != 400 {
			t.Fatalf("%s bad sort: %d", kind.path, code)
		}
		body := map[string]any{"name": "Gamma"}
		if kind.path == "/user-groups" {
			body["visibility"] = "shared"
		}
		code, m, _ = e.admin.do("PATCH", fmt.Sprintf("%s/%d", kind.path, id), body)
		if code != 200 || m["name"] != "Gamma" || m["note"] != "b" {
			t.Fatalf("%s patch: %d %v", kind.path, code, m)
		}
		if kind.path == "/user-groups" && m["visibility"] != "shared" {
			t.Fatalf("visibility: %v", m)
		}
		if code, _, _ := e.admin.do("PATCH", fmt.Sprintf("%s/%d", kind.path, id), map[string]any{"name": "alpha"}); code != 409 {
			t.Fatalf("%s rename conflict: %d", kind.path, code)
		}
		if code, _, _ := e.admin.do("PATCH", kind.path+"/9999", map[string]any{"name": "z"}); code != 404 {
			t.Fatalf("%s patch missing: %d", kind.path, code)
		}
		if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("%s/%d", kind.path, id), nil); code != 204 {
			t.Fatalf("%s delete: %d", kind.path, code)
		}
		if code, _, _ := e.admin.do("DELETE", fmt.Sprintf("%s/%d", kind.path, id), nil); code != 404 {
			t.Fatalf("%s delete again: %d", kind.path, code)
		}
	}
	// Members / devices fall back to no group.
	if u, _ := e.st.Users.GetByUsername(ctx, "member"); u.UserGroupID != nil {
		t.Fatal("member keeps deleted group")
	}
	if d, _ := e.st.Devices.GetByRustdeskID(ctx, "d1"); d.DeviceGroupID != nil {
		t.Fatal("device keeps deleted group")
	}
	for _, a := range []string{"user_group.create", "user_group.update", "user_group.delete", "device_group.create",
		"device_group.update", "device_group.delete"} {
		if e.auditCount(a) == 0 {
			t.Errorf("audit %s missing", a)
		}
	}
}

func TestLookups(t *testing.T) {
	e := newREnv(t, false)
	ctx := context.Background()
	usr, _ := e.login("bob", store.RoleUser)
	e.user("bobby", "", store.RoleUser)
	dis := &store.User{Username: "bobdisabled", Status: store.UserDisabled}
	_ = e.st.Users.Create(ctx, dis)
	_ = e.st.UserGroups.Create(ctx, &store.UserGroup{Name: "Bobs"})
	code, m, _ := usr.do("GET", "/lookup/users?q=bob", nil)
	if code != 200 || strings.Join(ids(m, "username"), ",") != "bob,bobby" {
		t.Fatalf("lookup users: %d %v", code, m)
	}
	if it := items(m)[0]; len(it) != 3 || it["display_name"] == nil {
		t.Fatalf("user ref shape: %v", it)
	}
	code, m, _ = usr.do("GET", "/lookup/user-groups?q=bo", nil)
	if code != 200 || strings.Join(ids(m, "name"), ",") != "Bobs" {
		t.Fatalf("lookup groups: %d %v", code, m)
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (c *client) raw(method, path, ctype string, body []byte) (*http.Response, []byte) {
	c.e.t.Helper()
	if c.cookie(CSRFCookie) == "" {
		c.do(http.MethodGet, "/auth/csrf", nil)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+"/api/v1/admin"+path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set(CSRFHeader, c.cookie(CSRFCookie))
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestSettingsAndBranding(t *testing.T) {
	e := newREnv(t, false)
	code, m, _ := e.admin.do("GET", "/settings", nil)
	if code != 200 || m["public_url"] != "http://panel.test" {
		t.Fatalf("defaults: %d %v", code, m)
	}
	au := m["auth"].(map[string]any)
	if au["password_login_enabled"] != true || num(au["session_idle_hours"]) != 12 || num(au["session_absolute_days"]) != 7 {
		t.Fatalf("auth defaults: %v", au)
	}
	if b := m["branding"].(map[string]any); b["title"] != DefaultBrandingTitle || b["has_logo"] != false {
		t.Fatalf("branding defaults: %v", b)
	}
	if _, ok := m["trusted_proxies"].([]any); !ok {
		t.Fatalf("trusted_proxies must be an array: %v", m["trusted_proxies"])
	}

	welcome := "# Hi {{username}}\n<script>alert(1)</script>"
	if rl := m["rate_limits"].(map[string]any); num(rl["login_free_attempts"]) < 1 || m["admin_url"] != "http://panel.test" {
		t.Fatalf("effective read-only values: %v", m)
	}
	if r := m["retention"].(map[string]any); num(r["punch_requests_days"]) != int64(store.RetentionDefaults[store.RetentionPunchRequests]) {
		t.Fatalf("retention defaults: %v", r)
	}
	code, m, _ = e.admin.do("PATCH", "/settings", map[string]any{
		"branding":  map[string]any{"title": "Acme Desk", "welcome_markdown": welcome, "web_client_url": "https://web.example.com"},
		"auth":      map[string]any{"require_admin_2fa": false, "session_idle_hours": 24},
		"retention": map[string]any{"login_events_days": 90},
	})
	if code != 200 || m["public_url"] != "http://panel.test" {
		t.Fatalf("patch: %d %v", code, m)
	}
	// The janitor reads the same retention values.
	if days, _ := e.st.Settings.RetentionDays(context.Background()); days[store.RetentionLoginEvents] != 90 {
		t.Fatalf("janitor retention: %v", days)
	}
	if b := m["branding"].(map[string]any); b["title"] != "Acme Desk" || b["welcome_markdown"] != welcome || b["web_client_url"] != "https://web.example.com" {
		t.Fatalf("branding patched: %v", b)
	}
	if num(m["auth"].(map[string]any)["session_idle_hours"]) != 24 || num(m["retention"].(map[string]any)["login_events_days"]) != 90 {
		t.Fatalf("patched values: %v", m)
	}
	// Partial: other keys untouched; null clears the web client URL.
	code, m, _ = e.admin.do("PATCH", "/settings", map[string]any{"branding": map[string]any{"web_client_url": nil}})
	if b := m["branding"].(map[string]any); code != 200 || b["web_client_url"] != nil || b["title"] != "Acme Desk" {
		t.Fatalf("partial: %d %v", code, m)
	}
	code, m, _ = e.admin.do("PATCH", "/settings", map[string]any{
		"public_url": "ftp://x", "branding": map[string]any{"title": strings.Repeat("t", 65)},
		"auth": map[string]any{"session_idle_hours": 0}, "retention": map[string]any{"admin_audit_days": -1},
		"trusted_proxies": []string{"not-an-ip"}})
	f, _ := m["error"].(map[string]any)["fields"].(map[string]any)
	if code != 400 || len(f) != 5 || f["auth.session_idle_hours"] == nil || f["public_url"] == nil || f["trusted_proxies"] == nil {
		t.Fatalf("validation: %d %v", code, m)
	}
	code, m, _ = e.admin.do("PATCH", "/settings", map[string]any{"auth": map[string]any{"session_idle_hours": 200, "session_absolute_days": 2}})
	if f, _ := m["error"].(map[string]any)["fields"].(map[string]any); code != 400 || f["auth.session_idle_hours"] == nil {
		t.Fatalf("idle > absolute: %d %v", code, m)
	}
	if code, _, _ := e.admin.do("PATCH", "/settings", map[string]any{"auth": map[string]any{"unknown": 1}}); code != 400 {
		t.Fatalf("unknown nested field: %d", code)
	}
	if e.auditCount("settings.update") != 2 {
		t.Fatalf("audit settings.update: %d", e.auditCount("settings.update"))
	}

	// Public branding.
	anon := e.client()
	code, m, _ = anon.do("GET", "/branding", nil)
	if code != 200 || m["title"] != "Acme Desk" || m["welcome_markdown"] != welcome || m["logo_url"] != nil {
		t.Fatalf("branding: %d %v", code, m)
	}
	if code, _, _ := anon.do("GET", "/branding/logo", nil); code != 404 {
		t.Fatalf("no logo: %d", code)
	}

	// Logo upload validation.
	img := pngBytes(t, 16, 16)
	if resp, b := e.admin.raw("PUT", "/settings/logo", "image/svg+xml", []byte(`<svg onload="alert(1)"/>`)); resp.StatusCode != 400 {
		t.Fatalf("svg: %d %s", resp.StatusCode, b)
	}
	if resp, _ := e.admin.raw("PUT", "/settings/logo", "image/jpeg", img); resp.StatusCode != 400 {
		t.Fatalf("mismatched type: %d", resp.StatusCode)
	}
	if resp, _ := e.admin.raw("PUT", "/settings/logo", "image/png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)); resp.StatusCode != 400 {
		t.Fatalf("corrupt png: %d", resp.StatusCode)
	}
	big := append(append([]byte{}, img...), make([]byte, 256<<10)...)
	if resp, _ := e.admin.raw("PUT", "/settings/logo", "image/png", big); resp.StatusCode != 400 {
		t.Fatalf("too large: %d", resp.StatusCode)
	}
	if resp, _ := e.admin.raw("PUT", "/settings/logo", "", img); resp.StatusCode != 400 {
		t.Fatalf("no content type: %d", resp.StatusCode)
	}
	usr, _ := e.login("bob", store.RoleUser)
	if resp, _ := usr.raw("PUT", "/settings/logo", "image/png", img); resp.StatusCode != 403 {
		t.Fatalf("user upload: %d", resp.StatusCode)
	}
	resp, b := e.admin.raw("PUT", "/settings/logo", "image/png", img)
	var br map[string]any
	_ = json.Unmarshal(b, &br)
	logoURL, _ := br["logo_url"].(string)
	if resp.StatusCode != 200 || !strings.HasPrefix(logoURL, LogoPath+"?v=") {
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	resp, b = anon.raw("GET", "/branding/logo", "", nil)
	if resp.StatusCode != 200 || !bytes.Equal(b, img) || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("get logo: %d %v", resp.StatusCode, resp.Header)
	}
	if _, m, _ = e.admin.do("GET", "/settings", nil); m["branding"].(map[string]any)["has_logo"] != true {
		t.Fatal("has_logo")
	}
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
	if resp, _ := e.admin.raw("PUT", "/settings/logo", "image/webp", webp); resp.StatusCode != 200 {
		t.Fatalf("webp: %d", resp.StatusCode)
	}
	if resp, _ := e.admin.raw("DELETE", "/settings/logo", "", nil); resp.StatusCode != 204 {
		t.Fatalf("delete logo: %d", resp.StatusCode)
	}
	if code, _, _ := anon.do("GET", "/branding/logo", nil); code != 404 {
		t.Fatalf("logo after delete: %d", code)
	}
	if e.auditCount("settings.logo_upload") != 2 || e.auditCount("settings.logo_delete") != 1 {
		t.Fatal("logo audit")
	}
}

func TestDashboard(t *testing.T) {
	e := newREnv(t, true)
	ctx := context.Background()
	bobC, bob := e.login("bob", store.RoleUser)
	e.device("1", func(d *store.Device) { d.OwnerUserID = &bob.ID })
	e.device("2", nil)
	e.device("3", nil)
	e.presence.set("1", e.clk.now())
	e.presence.set("2", e.clk.now())
	_ = e.st.ConnectionAudits.Create(ctx, &store.ConnectionAudit{DeviceID: "1", StartedAt: e.clk.now().Add(-30 * time.Minute)})
	ended := e.clk.now().Add(-2 * time.Hour)
	_ = e.st.ConnectionAudits.Create(ctx, &store.ConnectionAudit{DeviceID: "2", StartedAt: e.clk.now().Add(-3 * time.Hour), EndedAt: &ended})
	_ = e.st.ConnectionAudits.Create(ctx, &store.ConnectionAudit{DeviceID: "2", StartedAt: e.clk.now().Add(-48 * time.Hour)})
	_ = e.st.PresenceSamples().Record(ctx, e.clk.now().Add(-5*time.Hour), 7)
	e.client().login("bob", "wrong")

	code, m, _ := e.admin.do("GET", "/dashboard", nil)
	if code != 200 || num(m["devices_total"]) != 3 || num(m["devices_online"]) != 2 || num(m["users_total"]) != 2 ||
		num(m["connections_24h"]) != 2 || num(m["active_connections"]) != 1 || num(m["logins_24h"]) != 2 ||
		num(m["failed_logins_24h"]) != 1 {
		t.Fatalf("admin dashboard: %d %v", code, m)
	}
	s := m["series"].(map[string]any)
	for _, k := range []string{"logins", "connections", "online"} {
		if len(s[k].([]any)) != 24 {
			t.Fatalf("series %s: %v", k, s[k])
		}
	}
	online := s["online"].([]any)
	if num(online[23].(map[string]any)["v"]) != 2 || num(online[18].(map[string]any)["v"]) != 7 {
		t.Fatalf("online series: %v", online)
	}
	conns := s["connections"].([]any)
	if num(conns[23].(map[string]any)["v"])+num(conns[22].(map[string]any)["v"]) != 1 || num(conns[20].(map[string]any)["v"]) != 1 {
		t.Fatalf("connections series: %v", conns)
	}
	h := m["health"].(map[string]any)
	if h["hbbs"].(map[string]any)["status"] != "up" || h["hbbr"].(map[string]any)["status"] != "down" ||
		h["presence"].(map[string]any)["source"] != "hbbs" || num(h["presence"].(map[string]any)["devices_online"]) != 2 ||
		h["settings_in_sync"] != true {
		t.Fatalf("health: %v", h)
	}
	if v := m["version"].(map[string]any); v["panel"] == "" {
		t.Fatalf("version: %v", v)
	}

	code, m, _ = bobC.do("GET", "/dashboard", nil)
	if code != 200 || num(m["devices_total"]) != 1 || num(m["devices_online"]) != 1 || num(m["connections_24h"]) != 1 ||
		m["health"] != nil || num(m["logins_24h"]) != 1 || num(m["failed_logins_24h"]) != 1 {
		t.Fatalf("user dashboard: %d %v", code, m)
	}

	// Sampling helper.
	if err := SampleOnline(ctx, e.st, e.presence); err != nil {
		t.Fatal(err)
	}
	samples, _ := e.st.PresenceSamples().Since(ctx, store.HourStart(e.clk.now()))
	if samples[store.HourStart(e.clk.now()).UnixMilli()] != 2 {
		t.Fatalf("samples: %v", samples)
	}
}
