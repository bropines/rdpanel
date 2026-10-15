package clientapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/store"
)

func TestSysinfo(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	body := `{"cpu":"Intel(R) Core(TM) i7, 3.6GHz, 8/4 cores","memory":"15.87GB","os":"windows / Windows 11 Pro - 10.0.22631",
		"hostname":"HOST","username":"alice","version":"1.5.0","id":"123456789","uuid":"dXVpZA=="}`
	r := e.mustDo("POST", "/api/sysinfo", body, "", 200)
	if r.body != "SYSINFO_UPDATED" || !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("sysinfo answer must be plain text: %q %q", r.body, r.hdr.Get("Content-Type"))
	}
	d, err := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if err != nil || d.OS != "windows / Windows 11 Pro - 10.0.22631" || d.OSUsername != "alice" || d.ClientVersion != "1.5.0" ||
		d.UUID != "dXVpZA==" || d.SysinfoAt == nil {
		t.Fatalf("device %+v %v", d, err)
	}
	// Update without username keeps it.
	e.mustDo("POST", "/api/sysinfo", `{"id":"123456789","uuid":"dXVpZA==","hostname":"HOST2","os":"x"}`, "", 200)
	d, _ = e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d.Hostname != "HOST2" || d.OSUsername != "alice" {
		t.Fatalf("update %+v", d)
	}
	// Unowned device: uuid may change.
	e.mustDo("POST", "/api/sysinfo", `{"id":"123456789","uuid":"bmV3"}`, "", 200)

	// Owned device: uuid change refused unless the owner's token is presented.
	u := e.user("own", "pw-own-12345", nil)
	tok := e.login("own", "pw-own-12345", "123456789", "bmV3")
	d, _ = e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d.OwnerUserID == nil || *d.OwnerUserID != u.ID {
		t.Fatal("not bound at login")
	}
	e.golden("sysinfo_uuid_mismatch", e.do("POST", "/api/sysinfo", `{"id":"123456789","uuid":"ZXZpbA=="}`, ""), 403)
	e.mustDo("POST", "/api/sysinfo", `{"id":"123456789","uuid":"ZXZpbA=="}`, "garbage", 403)
	e.mustDo("POST", "/api/sysinfo", `{"id":"123456789","uuid":"ZXZpbA=="}`, tok, 200)
	d, _ = e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d.UUID != "ZXZpbA==" {
		t.Fatal("owner uuid change not applied")
	}

	e.golden("invalid_request", e.do("POST", "/api/sysinfo", `{"id":"1"}`, ""), 400)
	e.golden("invalid_request", e.do("POST", "/api/sysinfo", `{"id":"a b","uuid":"x"}`, ""), 400)
	e.golden("invalid_request", e.do("POST", "/api/sysinfo", "", ""), 400)
}

func TestSysinfoPresetsAndOwnerGuess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("pre", "pw-pre-12345", nil)
	g := &store.DeviceGroup{Name: "Kiosks"}
	_ = e.st.DeviceGroups.Create(ctx, g)
	e.mustDo("POST", "/api/sysinfo", `{"id":"777","uuid":"dQ==","preset-user-name":"pre","preset-device-group-name":"kiosks",
		"preset-note":"lobby","preset-address-book-name":"ignored"}`, "", 200)
	d, _ := e.st.Devices.GetByRustdeskID(ctx, "777")
	if d.OwnerUserID == nil || *d.OwnerUserID != u.ID || d.DeviceGroupID == nil || *d.DeviceGroupID != g.ID || d.Note != "lobby" {
		t.Fatalf("presets %+v", d)
	}
	// Presets apply on first contact only.
	e.mustDo("POST", "/api/sysinfo", `{"id":"777","uuid":"dQ==","preset-note":"changed"}`, "", 200)
	d, _ = e.st.Devices.GetByRustdeskID(ctx, "777")
	if d.Note != "lobby" {
		t.Fatal("preset re-applied")
	}
	// A user that logged in before the first sysinfo owns the new device.
	v := e.user("early", "pw-early-1234", nil)
	e.login("early", "pw-early-1234", "888", "ZQ==")
	e.mustDo("POST", "/api/sysinfo", `{"id":"888","uuid":"ZQ==","preset-user-name":"pre"}`, "", 200)
	d, _ = e.st.Devices.GetByRustdeskID(ctx, "888")
	if d.OwnerUserID == nil || *d.OwnerUserID != v.ID {
		t.Fatalf("guessed owner %+v", d.OwnerUserID)
	}
}

func TestSysinfoVer(t *testing.T) {
	e := newEnv(t)
	r := e.mustDo("POST", "/api/sysinfo_ver", "", "", 200)
	if r.body != fmt.Sprintf("dev\n%d", e.clk.now().Unix()) || !strings.HasPrefix(r.hdr.Get("Content-Type"), "text/plain") {
		t.Fatalf("%q", r.body)
	}
}

type fixedStrategy struct{ s *Strategy }

func (f fixedStrategy) StrategyFor(context.Context, *store.Device) (*Strategy, error) {
	return f.s, nil
}

func TestHeartbeat(t *testing.T) {
	strat := &Strategy{ModifiedAt: 1700000000, ConfigOptions: map[string]string{"approve-mode": "click", "whitelist": ""}}
	e := newEnv(t, func(d *Deps) { d.Strategies = fixedStrategy{strat} })
	ctx := context.Background()

	// Unknown device: never created, asked for sysinfo.
	e.golden("heartbeat_sysinfo", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","ver":1005000}`, ""), 200)
	if _, err := e.st.Devices.GetByRustdeskID(ctx, "123456789"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("heartbeat created a device")
	}
	e.device("123456789", "dXVpZA==")

	// Known device, strategy differs from the client's modified_at.
	e.golden("heartbeat_strategy", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","ver":1005000,"modified_at":0}`, ""), 200)
	e.golden("heartbeat_empty", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","ver":1005000,"modified_at":1700000000}`, ""), 200)
	d, _ := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d.LastHeartbeatAt == nil || d.LastSeenIP != "192.0.2.10" {
		t.Fatalf("heartbeat not recorded: %+v", d)
	}

	// uuid mismatch: ignored, uuid untouched.
	e.clk.advance(time.Minute)
	e.golden("heartbeat_empty", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"ZXZpbA==","modified_at":1700000000}`, ""), 200)
	d2, _ := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d2.UUID != "dXVpZA==" || !d2.LastHeartbeatAt.Equal(*d.LastHeartbeatAt) {
		t.Fatal("mismatched heartbeat changed the device")
	}

	// Admin kick: delivered once, only for live connections.
	e.kicks.Kick("123456789", 7, 9)
	e.golden("heartbeat_disconnect", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","conns":[3,7],"modified_at":1700000000}`, ""), 200)
	e.golden("heartbeat_empty", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","conns":[3,7],"modified_at":1700000000}`, ""), 200)

	// Server-requested sysinfo re-upload.
	e.clk.advance(time.Second)
	_ = e.st.Devices.RequestSysinfo(ctx, d.ID)
	e.golden("heartbeat_sysinfo", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","modified_at":1700000000}`, ""), 200)
	e.clk.advance(time.Second)
	e.device("123456789", "dXVpZA==")
	e.golden("heartbeat_empty", e.do("POST", "/api/heartbeat", `{"id":"123456789","uuid":"dXVpZA==","modified_at":1700000000}`, ""), 200)

	e.golden("invalid_request", e.do("POST", "/api/heartbeat", `{"uuid":"x"}`, ""), 400)
}

func TestDeviceRateLimit(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.DeviceRate = RateConfig{Rate: 1, Burst: 2} })
	for i := 0; i < 2; i++ {
		e.mustDo("POST", "/api/heartbeat", `{"id":"1","uuid":"u"}`, "", 200)
	}
	r := e.do("POST", "/api/heartbeat", `{"id":"1","uuid":"u"}`, "")
	if r.code != 429 || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d", r.code)
	}
	e.clk.advance(time.Second)
	e.mustDo("POST", "/api/heartbeat", `{"id":"1","uuid":"u"}`, "", 200)
}

func TestKickQueue(t *testing.T) {
	clk := &clock{t: time.Unix(0, 0)}
	q := NewMemKickQueue(time.Minute, clk.now)
	if got := q.Take("a", []int32{1}); got != nil {
		t.Fatal(got)
	}
	q.Kick("a") // all
	if got := q.Take("a", nil); got != nil {
		t.Fatal("kick-all with no live conns must stay pending")
	}
	if got := q.Take("a", []int32{4, 5}); len(got) != 2 {
		t.Fatalf("all: %v", got)
	}
	q.Kick("b", 1)
	q.Kick("b", 2, 1)
	if got := q.Take("b", []int32{2, 3}); len(got) != 1 || got[0] != 2 {
		t.Fatalf("specific: %v", got)
	}
	q.Kick("c", 1)
	clk.advance(2 * time.Minute)
	if q.Len() != 0 || q.Take("c", []int32{1}) != nil {
		t.Fatal("expired kick delivered")
	}
}
