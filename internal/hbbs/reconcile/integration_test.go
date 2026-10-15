package reconcile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/events"
	"github.com/bropines/rustdesk-panel/internal/hbbs/control"
)

// TestIntegrationReconcileRealServers applies desired settings to real
// hbbs/hbbr (RDPANEL_IT_HBBS_ADMIN, RDPANEL_IT_HBBR_ADMIN), reads them back,
// and, when RDPANEL_IT_RESTART_CMD is set (e.g. "docker restart rdpanel-it"),
// restarts both servers and checks that the reconciler restores the state.
func TestIntegrationReconcileRealServers(t *testing.T) {
	hs, hr := os.Getenv("RDPANEL_IT_HBBS_ADMIN"), os.Getenv("RDPANEL_IT_HBBR_ADMIN")
	if hs == "" || hr == "" {
		t.Skip("RDPANEL_IT_HBBS_ADMIN / RDPANEL_IT_HBBR_ADMIN not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ctl, err := control.New(control.Config{HBBSAddr: hs, HBBRAddr: hr})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Config{Control: ctl, Store: &Store{DB: d}, Bus: events.NewBus(nil), ListDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		RelayServers:            []string{"127.0.0.1:31117"},
		AlwaysUseRelay:          bp(true),
		MustLogin:               bp(true),
		TCPKeepalive:            bp(true),
		Blacklist:               []string{"192.0.2.10", "2001:db8::10"},
		Blocklist:               []string{"198.51.100.7"},
		LimitSpeedMbps:          fp(16),
		TotalBandwidthMbps:      fp(256),
		SingleBandwidthMbps:     fp(0.1),
		DowngradeThreshold:      fp(0.5),
		DowngradeStartCheckSecs: ip64(900),
	}
	if err := r.Apply(ctx, want, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // hbbs applies the relay list asynchronously
	matches := func() (bool, Observed) {
		obs, err := r.Current(ctx)
		if err != nil || obs.Errors != nil {
			return false, obs
		}
		s := obs.Settings
		ok := slices.Equal(s.RelayServers, want.RelayServers) && *s.AlwaysUseRelay && *s.MustLogin &&
			s.TCPKeepalive != nil && *s.TCPKeepalive &&
			slices.Equal(s.Blacklist, []string{"192.0.2.10", "2001:db8::10"}) &&
			slices.Equal(s.Blocklist, want.Blocklist) && *s.LimitSpeedMbps == 16 && *s.TotalBandwidthMbps == 256 &&
			rateEqual(0.1, *s.SingleBandwidthMbps) && *s.DowngradeThreshold == 0.5 && *s.DowngradeStartCheckSecs == 900
		return ok, obs
	}
	if ok, obs := matches(); !ok {
		t.Fatalf("read-back mismatch: %+v", obs)
	}
	ks, _ := r.KeyStatus(ctx)
	for _, k := range ks {
		if k.AppliedAt == nil || k.LastError != "" {
			t.Fatalf("key not applied: %+v", k)
		}
	}

	cmd := os.Getenv("RDPANEL_IT_RESTART_CMD")
	if cmd == "" {
		return
	}
	r.Tick(ctx) // establish "reachable" before the restart
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
	if ok, _ := matches(); ok {
		t.Log("warning: servers still report desired state right after restart")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		r.Tick(ctx)
		time.Sleep(300 * time.Millisecond)
		if ok, _ := matches(); ok {
			break
		}
		if time.Now().After(deadline) {
			_, obs := matches()
			t.Fatalf("state not restored after restart: %+v status %+v", obs, r.Status())
		}
	}
	t.Logf("restored after restart; status %+v", r.Status())
}
