package control

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// TestIntegrationRealAdmin runs against real hbbs/hbbr admin channels when
// RDPANEL_IT_HBBS_ADMIN (e.g. 127.0.0.1:21115) and RDPANEL_IT_HBBR_ADMIN
// (e.g. 127.0.0.1:21117) are set. It changes runtime state, so only point it
// at a throwaway instance.
func TestIntegrationRealAdmin(t *testing.T) {
	hs, hr := os.Getenv("RDPANEL_IT_HBBS_ADMIN"), os.Getenv("RDPANEL_IT_HBBR_ADMIN")
	if hs == "" || hr == "" {
		t.Skip("RDPANEL_IT_HBBS_ADMIN / RDPANEL_IT_HBBR_ADMIN not set")
	}
	c, err := New(Config{HBBSAddr: hs, HBBRAddr: hr})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, tg := range []Target{HBBS, HBBR} {
		rtt, err := c.Ping(ctx, tg)
		if err != nil {
			t.Fatalf("ping %s: %v", tg, err)
		}
		t.Logf("ping %s: %v", tg, rtt)
	}

	// hbbs
	for _, want := range []bool{true, false} {
		if err := c.SetMustLogin(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got, err := c.MustLogin(ctx); err != nil || got != want {
			t.Fatalf("must-login %v: %v %v", want, got, err)
		}
	}
	for _, want := range []bool{true, false} {
		if err := c.SetTCPKeepalive(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got, err := c.TCPKeepalive(ctx); err != nil || got != want {
			t.Fatalf("tcp-keepalive %v: %v %v (needs bropines/rustdesk-server >= 393f872)", want, got, err)
		}
	}
	if err := c.SetRelayServers(ctx, []string{"127.0.0.1:21117"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // applied asynchronously by the hbbs io loop
	rs, err := c.RelayServers(ctx)
	if err != nil || !slices.Equal(rs, []string{"127.0.0.1:21117"}) {
		t.Fatalf("relay-servers %v %v", rs, err)
	}
	for _, want := range []bool{true, false} {
		if err := c.SetAlwaysUseRelay(ctx, want); err != nil {
			t.Fatal(err)
		}
		if got, err := c.AlwaysUseRelay(ctx); err != nil || got != want {
			t.Fatalf("always-use-relay %v: %v %v", want, got, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	rs, _ = c.RelayServers(ctx)
	t.Logf("relay list after aur: %v (empty = legacy aur bug present)", rs)
	_ = c.SetRelayServers(ctx, []string{"127.0.0.1:21117"})

	ib, err := c.IPBlocker(ctx, 0)
	if err != nil {
		t.Fatalf("ip-blocker: %v", err)
	}
	t.Logf("ip-blocker: %+v", ib)
	ic, err := c.IPChanges(ctx, 0)
	if err != nil {
		t.Fatalf("ip-changes: %v", err)
	}
	t.Logf("ip-changes: %+v", ic)
	prs, err := c.PunchRequests(ctx, 0, 100)
	if err != nil {
		t.Fatalf("punch-requests: %v", err)
	}
	t.Logf("punch-requests: %d", len(prs))
	if err := c.ClearPunchRequests(ctx); err != nil {
		t.Fatal(err)
	}
	geo, err := c.TestGeo(ctx, "1.1.1.1", "8.8.8.8")
	if err != nil || geo != "127.0.0.1:21117" {
		t.Fatalf("test-geo %q %v", geo, err)
	}

	// hbbr
	if err := c.ListAdd(ctx, Blacklist, []string{"192.0.2.1", "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.ListContains(ctx, Blacklist, "192.0.2.1"); err != nil || !ok {
		t.Fatalf("contains %v %v", ok, err)
	}
	ips, err := c.ListIPs(ctx, Blacklist)
	if err != nil || !slices.Contains(ips, "192.0.2.2") {
		t.Fatalf("list %v %v", ips, err)
	}
	if err := c.ListRemove(ctx, Blacklist, []string{"192.0.2.1", "192.0.2.2"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.ListContains(ctx, Blacklist, "192.0.2.1"); ok {
		t.Fatal("not removed")
	}
	for _, r := range []Rate{LimitSpeed, TotalBandwidth, SingleBandwidth} {
		old, err := c.GetRate(ctx, r)
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if err := c.SetRate(ctx, r, 12.5); err != nil {
			t.Fatal(err)
		}
		if v, err := c.GetRate(ctx, r); err != nil || v != 12.5 {
			t.Fatalf("%s: %v %v", r, v, err)
		}
		_ = c.SetRate(ctx, r, old)
	}
	dt, err := c.DowngradeThreshold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetDowngradeThreshold(ctx, 0.5); err != nil {
		t.Fatal(err)
	}
	if v, _ := c.DowngradeThreshold(ctx); v != 0.5 {
		t.Fatalf("dt %v", v)
	}
	_ = c.SetDowngradeThreshold(ctx, dt)
	st, err := c.DowngradeStartCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("defaults: dt=%v t=%v", dt, st)
	u, err := c.Usage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	t.Logf("usage: %+v", u)
}
