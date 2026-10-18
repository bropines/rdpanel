package adminapi

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/presence"
	"github.com/bropines/rdpanel/internal/store"
)

func TestServerWithoutHBBS(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)
	code, m, _ := user.do(http.MethodGet, "/server/health", nil)
	wantErr(t, "user health", code, 403, m, "forbidden")
	code, m, _ = e.client().do(http.MethodGet, "/server/health", nil)
	wantErr(t, "anon health", code, 401, m, "unauthorized")

	code, m, _ = admin.do(http.MethodGet, "/server/health", nil)
	want(t, "health", code, 200, m)
	hbbs, _ := m["hbbs"].(map[string]any)
	pres, _ := m["presence"].(map[string]any)
	if hbbs["status"] != "unknown" || pres["source"] != "heartbeat" {
		t.Fatalf("health: %v", m)
	}
	for _, p := range []string{"/server/settings", "/server/ip-blocker", "/server/ip-changes", "/server/relay-usage"} {
		code, m, _ := admin.do(http.MethodGet, p, nil)
		wantErr(t, p, code, 503, m, "server_unavailable")
	}
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbs", "cmd": "h"})
	wantErr(t, "console", code, 503, m, "server_unavailable")
	// Punch requests and console metadata come from the panel itself.
	code, m, _ = admin.do(http.MethodGet, "/server/punch-requests", nil)
	want(t, "punch requests", code, 200, m)
	code, m, _ = admin.do(http.MethodGet, "/server/console/commands", nil)
	want(t, "console commands", code, 200, m)
}

func TestServerHealthAndSettings(t *testing.T) {
	e := newB2Env(t, b2opts{hbbs: true})
	admin, au := e.as("root", store.RoleAdmin)
	e.pres.health = presence.Health{Reachable: true, Since: time.Unix(1700000000, 0), LastOK: time.Unix(1700000100, 0),
		RTT: 3 * time.Millisecond, Polls: 5, Online: 7}
	e.rec.Tick(e.ctx)

	code, m, _ := admin.do(http.MethodGet, "/server/health", nil)
	want(t, "health", code, 200, m)
	hbbs, _ := m["hbbs"].(map[string]any)
	hbbr, _ := m["hbbr"].(map[string]any)
	pres, _ := m["presence"].(map[string]any)
	if hbbs["status"] != "up" || hbbr["status"] != "up" || hbbr["address"] != e.fake.Addr() ||
		pres["source"] != "hbbs" || b2bNum(pres["devices_online"]) != 7 || m["settings_in_sync"] != true {
		t.Fatalf("health: %v", m)
	}

	code, m, _ = admin.do(http.MethodGet, "/server/settings", nil)
	want(t, "get settings", code, 200, m)
	desired, _ := m["desired"].(map[string]any)
	applied, _ := m["applied"].(map[string]any)
	keys := map[string]map[string]any{}
	for _, k := range m["keys"].([]any) {
		km := k.(map[string]any)
		keys[km["key"].(string)] = km
	}
	if desired["must_login"] != nil || applied["must_login"] != false || b2bNum(applied["limit_speed_mbps"]) != 32 ||
		!hasStr(applied["relay_servers"], "relay.example.com") || b2bNum(applied["downgrade_start_check_s"]) != 1800 ||
		len(keys) != 12 || keys["must_login"]["managed"] != false || keys["must_login"]["in_sync"] != true ||
		applied["tcp_keepalive"] != false || desired["tcp_keepalive"] != nil || keys["tcp_keepalive"]["target"] != "hbbs" ||
		applied["webrtc"] != false || keys["webrtc"]["target"] != "hbbs" {
		t.Fatalf("settings: %v", m)
	}

	code, m, _ = admin.do(http.MethodPatch, "/server/settings", map[string]any{"must_login": true, "tcp_keepalive": true, "webrtc": true,
		"limit_speed_mbps": 16, "blacklist": []string{"1.2.3.4"}, "downgrade_start_check_s": 900})
	want(t, "patch", code, 200, m)
	_, _, ml, black, _, rates, _, tsec := e.fake.State()
	if !ml || !e.fake.TCPKeepalive() || !e.fake.WebRTC() || rates["limit-speed"] != 16 || !slices.Equal(black, []string{"1.2.3.4"}) || tsec != 900 {
		t.Fatalf("fake state: ml=%v rates=%v black=%v t=%d", ml, rates, black, tsec)
	}
	for _, k := range m["keys"].([]any) {
		km := k.(map[string]any)
		switch km["key"] {
		case "must_login", "tcp_keepalive", "webrtc", "limit_speed_mbps", "blacklist", "downgrade_start_check_s":
			if km["managed"] != true || km["in_sync"] != true || km["updated_by"] != "root" || km["applied_at"] == nil {
				t.Fatalf("key status: %v", km)
			}
		}
	}
	if d := m["desired"].(map[string]any); d["must_login"] != true || d["tcp_keepalive"] != true || b2bNum(d["limit_speed_mbps"]) != 16 {
		t.Fatalf("desired: %v", d)
	}
	// Partial: other keys keep their desired value; null unmanages.
	code, m, _ = admin.do(http.MethodPatch, "/server/settings", map[string]any{"blacklist": nil})
	want(t, "patch null", code, 200, m)
	if d := m["desired"].(map[string]any); d["blacklist"] != nil || d["must_login"] != true {
		t.Fatalf("desired after null: %v", d)
	}
	for _, bad := range []map[string]any{
		{"relay_servers": []string{"a b"}}, {"limit_speed_mbps": -1}, {"blocklist": []string{"not-an-ip"}},
		{"frobnicate": true}, {"must_login": "yes"}, {"tcp_keepalive": "Y"}, {"webrtc": "Y"}, {"relay_servers": []string{}},
	} {
		code, m, _ := admin.do(http.MethodPatch, "/server/settings", bad)
		wantErr(t, "invalid patch", code, 400, m, "validation_failed")
	}

	// A restarted hbbr forgets its runtime state; reapply restores it.
	e.fake.Restart()
	code, m, _ = admin.do(http.MethodGet, "/server/settings", nil)
	want(t, "get after restart", code, 200, m)
	for _, k := range m["keys"].([]any) {
		if km := k.(map[string]any); (km["key"] == "must_login" || km["key"] == "tcp_keepalive") && km["in_sync"] != false {
			t.Fatalf("drift not reported: %v", km)
		}
	}
	code, m, _ = admin.do(http.MethodPost, "/server/settings/reapply", nil)
	want(t, "reapply", code, 200, m)
	if _, _, ml, _, _, rates, _, _ := e.fake.State(); !ml || !e.fake.TCPKeepalive() || rates["limit-speed"] != 16 {
		t.Fatalf("reapply did not restore: ml=%v rates=%v", ml, rates)
	}

	// Audit: semantic rows plus admin-channel commands attributed to the admin.
	rows, _, _ := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{})
	var settingsRows, commandRows int
	for _, r := range rows {
		switch r.Action {
		case "server.settings.update", "server.settings.reapply":
			settingsRows++
		case ActionServerCommand:
			commandRows++
			if r.ActorUserID == nil || *r.ActorUserID != au.ID || r.ActorName != "root" || r.Details["mutating"] != true {
				t.Fatalf("command audit row: %+v", r)
			}
		}
	}
	if settingsRows != 3 || commandRows == 0 {
		t.Fatalf("audit rows: settings=%d commands=%d", settingsRows, commandRows)
	}
}

func TestServerTables(t *testing.T) {
	e := newB2Env(t, b2opts{hbbs: true})
	admin, _ := e.as("root", store.RoleAdmin)
	code, m, _ := admin.do(http.MethodGet, "/server/ip-blocker", nil)
	want(t, "ip-blocker", code, 200, m)
	l := b2bItems(m)
	if b2bNum(m["total"]) != 2 || len(l) != 2 || l[0]["ip"] != "1.2.3.4" || l[0]["blocked"] != true ||
		b2bNum(l[0]["attempts"]) != 31 || b2bNum(l[0]["ids_window_s"]) != 3600 || l[1]["blocked"] != false {
		t.Fatalf("ip-blocker: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/server/ip-blocker?page=1&page_size=1", nil)
	if code != 200 || len(b2bItems(m)) != 1 || b2bNum(m["total"]) != 2 {
		t.Fatalf("ip-blocker paged: %v", m)
	}
	e.fake.ResetCommands()
	code, _, _ = admin.do(http.MethodDelete, "/server/ip-blocker/%3A%3Affff%3A95.165.172.169", nil)
	want(t, "unblock percent-encoded v4-mapped", code, 204, nil)
	if cmds := e.fake.Commands(); len(cmds) != 1 || cmds[0] != "ip-blocker ::ffff:95.165.172.169 -" {
		t.Fatalf("unblock percent-encoded sent %q", cmds)
	}
	e.fake.ResetCommands()
	code, _, _ = admin.do(http.MethodDelete, "/server/ip-blocker/1.2.3.4", nil)
	want(t, "unblock", code, 204, nil)
	if cmds := e.fake.Commands(); len(cmds) != 1 || cmds[0] != "ip-blocker 1.2.3.4 -" {
		t.Fatalf("unblock sent %q", cmds)
	}
	code, m, _ = admin.do(http.MethodDelete, "/server/ip-blocker/not-an-ip", nil)
	wantErr(t, "unblock invalid", code, 400, m, "validation_failed")

	code, m, _ = admin.do(http.MethodGet, "/server/ip-changes", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["rustdesk_id"] != "123456789012" || b2bNum(l[0]["seconds"]) != 120 {
		t.Fatalf("ip-changes: %v", m)
	}
	if ips, _ := l[0]["ips"].(map[string]any); b2bNum(ips["10.0.0.1"]) != 3 {
		t.Fatalf("ips: %v", l[0])
	}
	code, _, _ = admin.do(http.MethodDelete, "/server/ip-changes/123456789012", nil)
	want(t, "clear ip-changes", code, 204, nil)
	code, m, _ = admin.do(http.MethodDelete, "/server/ip-changes/1234567", nil)
	wantErr(t, "ip-changes page-offset id", code, 400, m, "validation_failed")

	code, m, _ = admin.do(http.MethodGet, "/server/relay-usage", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 2 || l[0]["peer"] != "1.2.3.4:5555" || b2bNum(l[0]["highest_kbps"]) != 900 ||
		l[0]["total_mb"] != 12.5 || m["checked_at"] == nil {
		t.Fatalf("relay usage: %v", m)
	}

	// Mutating typed calls are audited with the admin as actor; reads are not.
	rows, _, _ := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{Action: ActionServerCommand})
	if len(rows) != 3 || rows[0].ActorName != "root" {
		t.Fatalf("command audit: %+v", rows)
	}
	for _, r := range rows {
		if r.Details["cmd"] != "ip-changes" && r.Details["cmd"] != "ip-blocker" {
			t.Fatalf("unexpected audited command %v", r.Details)
		}
	}

	at := time.Date(2026, 10, 15, 11, 0, 0, 0, time.UTC)
	if err := e.st.PunchRequests.Insert(e.ctx, []store.PunchRequest{
		{At: at, FromIP: "1.2.3.4", ToRustdeskID: "123456789", ToIP: "5.6.7.8"},
		{At: at.Add(time.Minute), FromIP: "::1", ToRustdeskID: "abcdef"},
	}); err != nil {
		t.Fatal(err)
	}
	code, m, _ = admin.do(http.MethodGet, "/server/punch-requests?q=abc", nil)
	l = b2bItems(m)
	if code != 200 || len(l) != 1 || l[0]["to_rustdesk_id"] != "abcdef" || l[0]["to_ip"] != nil {
		t.Fatalf("punch requests: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/server/punch-requests", nil)
	if l = b2bItems(m); code != 200 || len(l) != 2 || l[0]["from_ip"] != "::1" {
		t.Fatalf("punch requests newest first: %v", m)
	}
}

func TestConsole(t *testing.T) {
	e := newB2Env(t, b2opts{hbbs: true, consoleLimit: 4})
	admin, au := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)

	code, m, _ := admin.do(http.MethodGet, "/server/console/commands", nil)
	want(t, "commands", code, 200, m)
	var found bool
	for _, it := range b2bItems(m) {
		if it["target"] == "hbbs" && it["cmd"] == "must-login" {
			found = it["alias"] == "ml" && it["mutating"] == true && b2bNum(it["max_args"]) == 1 && it["description"] != ""
		}
	}
	if !found || len(b2bItems(m)) != len(control.Commands()) {
		t.Fatalf("commands: %v", m)
	}

	code, m, _ = user.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbs", "cmd": "h"})
	wantErr(t, "user console", code, 403, m, "forbidden")
	admin.noCSRF = true
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbs", "cmd": "h"})
	wantErr(t, "console without CSRF", code, 403, m, "csrf_failed")
	admin.noCSRF = false

	e.fake.ResetCommands()
	for _, bad := range []map[string]any{
		{"target": "hbbs", "cmd": "ml"},                                     // alias
		{"target": "hbbs", "cmd": "bogus-cmd"},                              // not allow-listed
		{"target": "hbbx", "cmd": "h"},                                      // target
		{"target": "hbbs", "cmd": "must-login", "args": []string{"X"}},      // pattern
		{"target": "hbbs", "cmd": "must-login", "args": []string{"Y", "Y"}}, // too many
		{"target": "hbbs", "cmd": "h", "args": []string{"a b"}},             // whitespace
		{"target": "hbbr", "cmd": "blacklist-add", "args": []string{"1.2.3.4;rm"}},
		{"target": "hbbr", "cmd": "blacklist-add", "args": []string{"999.1.1.1"}}, // control re-validates
		{"target": "hbbs", "cmd": "h", "extra": 1},
	} {
		code, m, _ := admin.do(http.MethodPost, "/server/console", bad)
		wantErr(t, "console reject", code, 400, m, "validation_failed")
	}
	if cmds := e.fake.Commands(); len(cmds) != 0 {
		t.Fatalf("rejected commands reached hbbs: %q", cmds)
	}

	// The control-level rejection above consumed one of the 4 allowed calls.
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbs", "cmd": "must-login", "args": []string{"y"}})
	want(t, "console set", code, 200, m)
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbs", "cmd": "must-login"})
	want(t, "console get", code, 200, m)
	if !strings.Contains(m["output"].(string), "MUST_LOGIN: true") || m["executed_at"] == nil || m["truncated"] != false {
		t.Fatalf("console output: %v", m)
	}
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbr", "cmd": "usage"})
	want(t, "console 3", code, 200, m)
	code, m, _ = admin.do(http.MethodPost, "/server/console", map[string]any{"target": "hbbr", "cmd": "usage"})
	wantErr(t, "console rate limit", code, 429, m, "rate_limited")

	code, m, _ = admin.do(http.MethodGet, "/server/console/history", nil)
	l := b2bItems(m)
	if code != 200 || b2bNum(m["total"]) != 3 || l[0]["cmd"] != "usage" || l[0]["target"] != "hbbr" ||
		l[1]["output_excerpt"] != "MUST_LOGIN: true\n" || !hasStr(l[2]["args"], "Y") || l[2]["actor_name"] != "root" {
		t.Fatalf("history: %v", m)
	}
	// One audit row per console call (the sink defers to the handler).
	rows, _, _ := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{Action: ActionServerConsole})
	if len(rows) != 3 || *rows[0].ActorUserID != au.ID {
		t.Fatalf("console audit rows: %+v", rows)
	}

	// Unreachable admin channel: 503.
	e.fake.Close()
	code, m, _ = admin.do(http.MethodGet, "/server/ip-blocker", nil)
	wantErr(t, "ip-blocker down", code, 503, m, "server_unavailable")
}

func TestCommandAuditSink(t *testing.T) {
	e := newB2Env(t, b2opts{})
	sink := NewCommandAudit(e.st, nil)
	uid := int64(1)
	e.user("root", testPassword, store.RoleAdmin)
	ctx := context.Background()
	sink.RecordCommand(ctx, control.CommandRecord{Target: control.HBBS, Command: "must-login"})
	sink.RecordCommand(ctx, control.CommandRecord{Target: control.HBBS, Command: "punch-requests", Args: []string{"-"},
		Mutating: true, Actor: control.Actor{Name: PunchCollectorActor}})
	sink.RecordCommand(ctx, control.CommandRecord{Target: control.HBBR, Command: "blacklist-add", Args: []string{"1.2.3.4"},
		Mutating: true, Actor: control.Actor{UserID: &uid, Name: "root", IP: "127.0.0.1"}, ResponseSHA256: "ab", Err: "boom"})
	sink.RecordCommand(ctx, control.CommandRecord{Target: control.HBBS, Command: "relay-servers", Args: []string{"r1"},
		Mutating: true, Actor: control.SystemActor})
	rows, _, err := e.st.AdminAudit.ListPanel(ctx, store.AdminAuditQuery{AuditQuery: store.AuditQuery{Desc: false}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %d %v", len(rows), err)
	}
	r := rows[0]
	if r.Action != ActionServerCommand || r.TargetID != "hbbr" || r.ActorIP != "127.0.0.1" || r.Details["error"] != "boom" ||
		r.Details["cmd"] != "blacklist-add" {
		t.Fatalf("row: %+v", r)
	}
	if rows[1].ActorName != "system" || rows[1].ActorUserID != nil {
		t.Fatalf("system row: %+v", rows[1])
	}
}

func TestEncodeRustdeskConfig(t *testing.T) {
	host := "rd.example.com"
	relay := "relay.example.com"
	api := "https://panel.example.com"
	key := "test-pubkey-abc123xyz"

	encoded, err := EncodeRustdeskConfig(host, relay, api, key)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if encoded == "" || strings.Contains(encoded, "=") {
		t.Fatalf("unexpected encoded string: %q", encoded)
	}

	dHost, dRelay, dAPI, dKey, err := DecodeRustdeskConfig(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if dHost != host || dRelay != relay || dAPI != api || dKey != key {
		t.Fatalf("mismatch after decode: got (%q, %q, %q, %q), want (%q, %q, %q, %q)",
			dHost, dRelay, dAPI, dKey, host, relay, api, key)
	}
}

func TestServerClientConfig(t *testing.T) {
	e := newB2Env(t, b2opts{})
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)

	code, m, _ := user.do(http.MethodGet, "/server/client-config", nil)
	wantErr(t, "user client-config", code, 403, m, "forbidden")

	code, m, _ = e.client().do(http.MethodGet, "/server/client-config", nil)
	wantErr(t, "anon client-config", code, 401, m, "unauthorized")

	code, m, _ = admin.do(http.MethodGet, "/server/client-config", nil)
	want(t, "admin client-config", code, 200, m)

	idSrv, _ := m["id_server"].(string)
	relaySrv, _ := m["relay_server"].(string)
	apiSrv, _ := m["api_server"].(string)
	cfgStr, _ := m["config_string"].(string)

	if idSrv == "" || relaySrv == "" || apiSrv == "" || cfgStr == "" {
		t.Fatalf("empty fields in client-config: %+v", m)
	}

	dHost, dRelay, dAPI, _, err := DecodeRustdeskConfig(cfgStr)
	if err != nil {
		t.Fatalf("decode config_string failed: %v", err)
	}
	if dHost != idSrv || dRelay != relaySrv || dAPI != apiSrv {
		t.Fatalf("config_string mismatch: got (%q, %q, %q), want (%q, %q, %q)",
			dHost, dRelay, dAPI, idSrv, relaySrv, apiSrv)
	}
}

func TestResolveHostIP(t *testing.T) {
	ctx := context.Background()
	// Already IP
	if res := resolveHostIP(ctx, "1.2.3.4"); res != "" {
		t.Fatalf("expected empty for IPv4, got %q", res)
	}
	if res := resolveHostIP(ctx, "1.2.3.4:21117"); res != "" {
		t.Fatalf("expected empty for IPv4 with port, got %q", res)
	}
	if res := resolveHostIP(ctx, ""); res != "" {
		t.Fatalf("expected empty for empty host, got %q", res)
	}
	// Localhost should resolve to 127.0.0.1
	if res := resolveHostIP(ctx, "localhost"); res != "127.0.0.1" {
		t.Logf("localhost resolved to %q (may vary on some systems)", res)
	}
	if res := resolveHostIP(ctx, "localhost:21117"); res != "127.0.0.1:21117" && res != "" {
		t.Logf("localhost:21117 resolved to %q", res)
	}
}
