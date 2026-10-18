package control

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
)

type memAudit struct {
	mu   sync.Mutex
	recs []CommandRecord
}

func (m *memAudit) RecordCommand(_ context.Context, r CommandRecord) {
	m.mu.Lock()
	m.recs = append(m.recs, r)
	m.mu.Unlock()
}

func (m *memAudit) last() CommandRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recs[len(m.recs)-1]
}

func newClient(t *testing.T) (*Client, *hbbstest.FakeAdmin, *hbbstest.FakeAdmin, *memAudit) {
	t.Helper()
	hs, hr := hbbstest.NewFakeAdmin(t), hbbstest.NewFakeAdmin(t)
	au := &memAudit{}
	c, err := New(Config{HBBSAddr: hs.Addr(), HBBRAddr: hr.Addr(), Audit: au, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, hs, hr, au
}

func TestNewRequiresLoopback(t *testing.T) {
	for _, a := range []string{"10.0.0.1:21115", "example.com:21115", "0.0.0.0:21115", "[::]:21115", "bad"} {
		if _, err := New(Config{HBBSAddr: a}); err == nil {
			t.Errorf("%s accepted", a)
		}
	}
	for _, a := range []string{"127.0.0.1:21115", "127.1.2.3:1", "[::1]:21115", "localhost:21115", "[::ffff:127.0.0.1]:1", "unix:/run/hbb/admin.sock"} {
		if _, err := New(Config{HBBSAddr: a}); err != nil {
			t.Errorf("%s rejected: %v", a, err)
		}
	}
	if _, err := New(Config{HBBRAddr: "192.168.1.1:21117"}); !errors.Is(err, ErrNotLoopback) {
		t.Errorf("hbbr non-loopback: %v", err)
	}
}

func TestHBBSTyped(t *testing.T) {
	c, hs, _, au := newClient(t)
	ctx := WithActor(context.Background(), Actor{Name: "alice", IP: "10.1.1.1"})

	if d, err := c.Ping(ctx, HBBS); err != nil || d <= 0 {
		t.Fatalf("ping %v %v", d, err)
	}

	rs, err := c.RelayServers(ctx)
	if err != nil || len(rs) != 1 || rs[0] != "relay.example.com" {
		t.Fatalf("rs %v %v", rs, err)
	}
	if err := c.SetRelayServers(ctx, []string{" r1.example.com:21117", "r1.example.com:21117", "[2001:db8::1]:21117", "10.0.0.1", ""}); err != nil {
		t.Fatal(err)
	}
	// Duplicates are kept (weighted rotation).
	if got := hs.Commands(); got[len(got)-1] != "relay-servers r1.example.com:21117,r1.example.com:21117,[2001:db8::1]:21117,10.0.0.1" {
		t.Fatalf("wire %q", got[len(got)-1])
	}
	r := au.last()
	if !r.Mutating || r.Actor.Name != "alice" || r.Command != "relay-servers" || r.Raw {
		t.Fatalf("audit %+v", r)
	}
	if err := c.SetRelayServers(ctx, nil); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("empty relay list: %v", err)
	}
	for _, bad := range []string{"::1", "a b", "a,b", "host:0", "host:99999", "-bad-.com", "x;rm"} {
		if err := c.SetRelayServers(ctx, []string{bad}); !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("relay %q accepted: %v", bad, err)
		}
	}

	if err := c.SetAlwaysUseRelay(ctx, true); err != nil {
		t.Fatal(err)
	}
	if v, err := c.AlwaysUseRelay(ctx); err != nil || !v {
		t.Fatalf("aur %v %v", v, err)
	}
	if err := c.SetMustLogin(ctx, true); err != nil {
		t.Fatal(err)
	}
	if v, err := c.MustLogin(ctx); err != nil || !v {
		t.Fatalf("ml %v %v", v, err)
	}
	if got := hs.Commands(); got[len(got)-2] != "must-login Y" {
		t.Fatalf("wire %q", got)
	}
	if v, err := c.TCPKeepalive(ctx); err != nil || v {
		t.Fatalf("tk default %v %v", v, err)
	}
	if err := c.SetTCPKeepalive(ctx, true); err != nil {
		t.Fatal(err)
	}
	if v, err := c.TCPKeepalive(ctx); err != nil || !v || !hs.TCPKeepalive() {
		t.Fatalf("tk %v %v", v, err)
	}
	if got := hs.Commands(); got[len(got)-2] != "tcp-keepalive Y" || got[len(got)-1] != "tcp-keepalive" {
		t.Fatalf("wire %q", got)
	}
	if err := c.SetTCPKeepalive(ctx, false); err != nil || hs.TCPKeepalive() {
		t.Fatalf("tk off: %v", err)
	}
	if v, err := c.WebRTC(ctx); err != nil || v {
		t.Fatalf("wr default %v %v", v, err)
	}
	if err := c.SetWebRTC(ctx, true); err != nil {
		t.Fatal(err)
	}
	if v, err := c.WebRTC(ctx); err != nil || !v || !hs.WebRTC() {
		t.Fatalf("wr %v %v", v, err)
	}
	if got := hs.Commands(); got[len(got)-2] != "webrtc Y" || got[len(got)-1] != "webrtc" {
		t.Fatalf("wire %q", got)
	}
	if err := c.SetWebRTC(ctx, false); err != nil || hs.WebRTC() {
		t.Fatalf("wr off: %v", err)
	}

	page, err := c.IPBlocker(ctx, 0)
	if err != nil || page.Total != 2 || len(page.Entries) != 2 {
		t.Fatalf("ib %+v %v", page, err)
	}
	e := page.Entries[0]
	if e.IP != "1.2.3.4" || e.Attempts != 31 || e.AttemptsAge != 12*time.Second || e.DistinctIDs != 2 || e.IDsAge != time.Hour {
		t.Fatalf("ib entry %+v", e)
	}
	if page.Entries[1].IP != "::ffff:127.0.0.1" {
		t.Fatalf("v4-mapped key %+v", page.Entries[1])
	}
	one, err := c.IPBlockerLookup(ctx, "1.2.3.4", false)
	if err != nil || one == nil || one.Attempts != 31 {
		t.Fatalf("ib lookup %+v %v", one, err)
	}
	none, err := c.IPBlockerLookup(ctx, "9.9.9.9", true)
	if err != nil || none != nil {
		t.Fatalf("ib missing %+v %v", none, err)
	}
	if got := hs.Commands(); got[len(got)-1] != "ip-blocker 9.9.9.9 -" || !au.last().Mutating {
		t.Fatalf("unblock wire %q", got[len(got)-1])
	}

	ic, err := c.IPChanges(ctx, 0)
	if err != nil || ic.Total != 1 || ic.Entries[0].ID != "123456789012" || ic.Entries[0].Age != 2*time.Minute ||
		ic.Entries[0].IPs["10.0.0.1"] != 3 || ic.Entries[0].IPs["10.0.0.2"] != 1 {
		t.Fatalf("ic %+v %v", ic, err)
	}
	icOne, err := c.IPChangesLookup(ctx, "123456789012", false)
	if err != nil || icOne == nil || len(icOne.IPs) != 2 {
		t.Fatalf("ic lookup %+v %v", icOne, err)
	}
	if _, err := c.IPChangesLookup(ctx, "1234567", false); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("small numeric id: %v", err)
	}

	prs, err := c.PunchRequests(ctx, 0, 1000)
	if err != nil || len(prs) != 2 || prs[0].FromIP != "1.2.3.4" || prs[0].ToID != "123456789" || prs[0].ToIP != "5.6.7.8" ||
		!prs[0].Time.Equal(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)) || prs[1].ToIP != "::2" {
		t.Fatalf("pr %+v %v", prs, err)
	}
	if err := c.ClearPunchRequests(ctx); err != nil {
		t.Fatal(err)
	}
	if got := hs.Commands(); got[len(got)-1] != "punch-requests -" {
		t.Fatalf("pr clear wire %q", got[len(got)-1])
	}

	if s, err := c.TestGeo(ctx, "1.1.1.1", "8.8.8.8"); err != nil || s != "relay.example.com" {
		t.Fatalf("tg %q %v", s, err)
	}
}

func TestHBBRTyped(t *testing.T) {
	c, _, hr, _ := newClient(t)
	ctx := context.Background()

	if err := c.ListAdd(ctx, Blacklist, []string{"1.1.1.1", "::ffff:2.2.2.2", "1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if got := hr.Commands(); got[len(got)-1] != "blacklist-add 1.1.1.1|2.2.2.2" {
		t.Fatalf("wire %q", got[len(got)-1])
	}
	ips, err := c.ListIPs(ctx, Blacklist)
	if err != nil || strings.Join(ips, ",") != "1.1.1.1,2.2.2.2" {
		t.Fatalf("list %v %v", ips, err)
	}
	if ok, err := c.ListContains(ctx, Blacklist, "1.1.1.1"); err != nil || !ok {
		t.Fatalf("contains %v %v", ok, err)
	}
	if ok, err := c.ListContains(ctx, Blocklist, "1.1.1.1"); err != nil || ok {
		t.Fatalf("blocklist contains %v %v", ok, err)
	}
	if err := c.ListRemove(ctx, Blacklist, []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.ListAdd(ctx, Blocklist, []string{"3.3.3.3"}); err != nil {
		t.Fatal(err)
	}
	if err := c.ListClear(ctx, Blacklist); err != nil {
		t.Fatal(err)
	}
	_, _, _, black, block, _, _, _ := hr.State()
	if len(black) != 0 || len(block) != 1 {
		t.Fatalf("state %v %v", black, block)
	}
	if err := c.ListAdd(ctx, Blacklist, []string{"10.0.0.0/8"}); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("cidr accepted: %v", err)
	}

	// Batching: 200 IPv6 addresses do not fit in one 1 KiB command.
	hr.ResetCommands()
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, "2001:db8:1234:5678:9abc:def0:1234:"+strings.ToLower(strings.TrimLeft(hex4(i), "0")+"1"))
	}
	if err := c.ListAdd(ctx, Blocklist, many); err != nil {
		t.Fatal(err)
	}
	cmds := hr.Commands()
	if len(cmds) < 2 {
		t.Fatalf("expected batching, got %d commands", len(cmds))
	}
	for _, cmd := range cmds {
		if len(cmd) > maxLine {
			t.Fatalf("command too long: %d", len(cmd))
		}
	}
	_, _, _, _, block, _, _, _ = hr.State()
	if len(block) != 201 {
		t.Fatalf("blocklist size %d", len(block))
	}

	for _, r := range []Rate{LimitSpeed, TotalBandwidth, SingleBandwidth} {
		if err := c.SetRate(ctx, r, 12.5); err != nil {
			t.Fatal(err)
		}
		if v, err := c.GetRate(ctx, r); err != nil || v != 12.5 {
			t.Fatalf("%s %v %v", r, v, err)
		}
	}
	if err := c.SetRate(ctx, LimitSpeed, -1); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("negative rate: %v", err)
	}
	if err := c.SetDowngradeThreshold(ctx, 0.5); err != nil {
		t.Fatal(err)
	}
	if v, err := c.DowngradeThreshold(ctx); err != nil || v != 0.5 {
		t.Fatalf("dt %v %v", v, err)
	}
	if err := c.SetDowngradeStartCheck(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, err := c.DowngradeStartCheck(ctx); err != nil || v != 10*time.Minute {
		t.Fatalf("t %v %v", v, err)
	}
	u, err := c.Usage(ctx)
	if err != nil || len(u) != 2 || u[0].Addr != "1.2.3.4:5555" || u[0].Elapsed != 2*time.Minute || u[0].TotalMB != 12.5 ||
		u[0].HighestKbps != 900 || u[0].AvgKbps != 850 || u[0].CurrentKbps != 800 || u[1].Addr != "[::1]:6000" {
		t.Fatalf("usage %+v %v", u, err)
	}

	st, err := c.Stats(ctx)
	if err != nil || st.BytesTotal != 104857600 || st.RelaysTotal != 42 || st.RelaysActive != 3 || st.Waiting != 1 ||
		st.AuthFailed != 2 || st.Blocked != 5 || st.Downgraded != 0 || st.UptimeSeconds != 3600 || st.Version != "1.1.16" {
		t.Fatalf("stats %+v %v", st, err)
	}
}

func hex4(i int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[i>>12&15], digits[i>>8&15], digits[i>>4&15], digits[i&15]})
}

func TestAllowListAndValidation(t *testing.T) {
	c, hs, hr, au := newClient(t)
	ctx := context.Background()
	for _, tc := range []struct {
		t    Target
		line string
		err  error
	}{
		{HBBS, "reload-geo extra", ErrInvalidArgs},
		{HBBS, "rg extra", ErrInvalidArgs},
		{HBBS, "stats", ErrNotAllowed},     // hbbr command sent to hbbs
		{HBBR, "stats extra", ErrInvalidArgs},
		{HBBS, "blacklist", ErrNotAllowed}, // hbbr command sent to hbbs
		{HBBR, "must-login", ErrNotAllowed},
		{HBBS, "", ErrNotAllowed},
		{"x", "h", ErrNotAllowed},
		{HBBS, "ml maybe", ErrInvalidArgs},
		{HBBS, "ml Y extra", ErrInvalidArgs},
		{HBBS, "tk maybe", ErrInvalidArgs},
		{HBBS, "tcp-keepalive Y N", ErrInvalidArgs},
		{HBBS, "wr maybe", ErrInvalidArgs},
		{HBBS, "webrtc Y N", ErrInvalidArgs},
		{HBBR, "wr", ErrNotAllowed},
		{HBBR, "tk", ErrNotAllowed},
		{HBBS, "h now", ErrInvalidArgs},
		{HBBS, "ib 1.2.3.4 +", ErrInvalidArgs},
		{HBBS, "ib not-an-ip", ErrInvalidArgs},
		{HBBS, "ic 123 -", ErrInvalidArgs},
		{HBBS, "ic bad!id", ErrInvalidArgs},
		{HBBS, "pr 0 0", ErrInvalidArgs},
		{HBBS, "pr -1", ErrInvalidArgs},
		{HBBS, "tg", ErrInvalidArgs},
		{HBBS, "tg 1.1.1.1 2.2.2.2 3.3.3.3", ErrInvalidArgs},
		{HBBR, "ba", ErrInvalidArgs},
		{HBBR, "ba 1.1.1.1|nope", ErrInvalidArgs},
		{HBBR, "ls 0", ErrInvalidArgs},
		{HBBR, "ls NaN", ErrInvalidArgs},
		{HBBR, "ls Inf", ErrInvalidArgs},
		{HBBR, "t 1.5", ErrInvalidArgs},
		{HBBR, "u now", ErrInvalidArgs},
	} {
		if _, err := c.ExecLine(ctx, tc.t, tc.line); !errors.Is(err, tc.err) {
			t.Errorf("%s %q: %v, want %v", tc.t, tc.line, err, tc.err)
		}
	}
	if len(hs.Commands())+len(hr.Commands()) != 0 {
		t.Fatal("rejected commands must not reach the server")
	}
	if len(au.recs) != 0 {
		t.Fatal("rejected commands are not audited as executed")
	}

	// Aliases are canonicalised; args normalised.
	if _, err := c.ExecLine(ctx, HBBS, "  ml   y "); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecLine(ctx, HBBS, "tk y"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecLine(ctx, HBBS, "wr y"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecLine(ctx, HBBR, "B ::ffff:1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecLine(ctx, HBBR, "b"); err != nil {
		t.Fatal(err)
	}
	if hs.Commands()[0] != "must-login Y" || hs.Commands()[1] != "tcp-keepalive Y" || !hs.TCPKeepalive() ||
		hs.Commands()[2] != "webrtc Y" || !hs.WebRTC() || hr.Commands()[0] != "blocklist 1.2.3.4" || hr.Commands()[1] != "blacklist" {
		t.Fatalf("canonical: %q %q", hs.Commands(), hr.Commands())
	}
	if r := au.last(); !r.Raw || r.Mutating || r.Command != "blacklist" {
		t.Fatalf("audit %+v", r)
	}
	if len(Commands()) != len(specs) {
		t.Fatal("Commands")
	}
}

func TestTransportFailures(t *testing.T) {
	c, hs, _, au := newClient(t)
	ctx := context.Background()

	// Oversize reply.
	c.cfg.MaxResponse = 64
	hs.SetOverride(func(string) (string, bool) { return strings.Repeat("x", 65), true })
	if _, err := c.Exec(ctx, HBBS, "h"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	c.cfg.MaxResponse = DefaultMaxResponse

	// Non-UTF-8 garbage.
	hs.SetOverride(func(string) (string, bool) { return "\xff\xfe", true })
	if _, err := c.Exec(ctx, HBBS, "h"); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("garbage: %v", err)
	}
	// Unparseable replies for typed getters.
	hs.SetOverride(func(string) (string, bool) { return "MUST_LOGIN: true\n", true })
	if _, err := c.TCPKeepalive(ctx); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("tk with wrong prefix: %v", err)
	}
	if _, err := c.WebRTC(ctx); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("wr with wrong prefix: %v", err)
	}
	hs.SetOverride(func(string) (string, bool) { return "MUST_LOGIN: perhaps\n", true })
	if _, err := c.MustLogin(ctx); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("bad bool: %v", err)
	}
	hs.SetOverride(func(string) (string, bool) { return "", true })
	if _, err := c.MustLogin(ctx); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("empty getter reply: %v", err)
	}
	if _, err := c.Ping(ctx, HBBS); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("empty ping: %v", err)
	}
	hs.SetOverride(func(string) (string, bool) { return "x\n1.2.3.4: what\n", true })
	if _, err := c.IPBlocker(ctx, 0); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("bad ib: %v", err)
	}
	hs.SetOverride(func(string) (string, bool) { return "yesterday 1.1.1.1 -> a@b\n", true })
	if _, err := c.PunchRequests(ctx, 0, 10); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("bad pr: %v", err)
	}
	hs.SetOverride(nil)

	// Slow server -> timeout, recorded in the audit log with the error.
	c.cfg.Timeout = 200 * time.Millisecond
	hs.Delay.Store(int64(time.Second))
	start := time.Now()
	if _, err := c.Exec(ctx, HBBS, "h"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout: %v after %v", err, time.Since(start))
	}
	if au.last().Err == "" {
		t.Fatal("error not audited")
	}
	hs.Delay.Store(0)

	// Server closes without replying: empty reply is valid for setters.
	hs.Drop.Store(true)
	if out, err := c.Exec(ctx, HBBS, "ml", "Y"); err != nil || out != "" {
		t.Fatalf("drop: %q %v", out, err)
	}
	hs.Drop.Store(false)

	// Server down.
	hs.Close()
	if _, err := c.Ping(ctx, HBBS); err == nil {
		t.Fatal("ping of closed port must fail")
	}

	// Cancelled context.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Exec(cctx, HBBR, "h"); err == nil {
		t.Fatal("cancelled ctx")
	}
}

func TestSerializedPerTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	active, maxActive := 0, 0
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				mu.Lock()
				active++
				maxActive = max(maxActive, active)
				mu.Unlock()
				buf := make([]byte, 1024)
				_, _ = conn.Read(buf)
				time.Sleep(20 * time.Millisecond)
				_, _ = conn.Write([]byte("ok\n"))
				mu.Lock()
				active--
				mu.Unlock()
			}()
		}
	}()
	c, _ := New(Config{HBBSAddr: ln.Addr().String()})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Exec(context.Background(), HBBS, "h") }()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("max concurrent connections %d", maxActive)
	}
}

func TestParsers(t *testing.T) {
	m, err := parseRustStringIntMap(`{}`)
	if err != nil || len(m) != 0 {
		t.Fatal(m, err)
	}
	m, err = parseRustStringIntMap(`{"::ffff:1.2.3.4": 2}`)
	if err != nil || m["::ffff:1.2.3.4"] != 2 {
		t.Fatal(m, err)
	}
	for _, bad := range []string{`{"a" 1}`, `{a: 1}`, `["a": 1]`, `{"a": x}`} {
		if _, err := parseRustStringIntMap(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if b := BatchIPs([]string{"aaa", "bbb", "ccc"}, 7); len(b) != 2 || len(b[0]) != 2 {
		t.Fatalf("batch %v", b)
	}
	if b := BatchIPs(nil, 7); len(b) != 0 {
		t.Fatal("empty batch")
	}
}
