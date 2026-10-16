package agent_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"net/http/httptest"

	"github.com/bropines/rdpanel/internal/agent"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/store"
)

const hbbsKey = "OeVuKk5nlHiXp+APNn0Y3pC1Iwpwn44JGqrQCsWqmBw="

// fakeRunner records hbbr starts instead of running a binary.
type fakeRunner struct {
	mu    sync.Mutex
	args  [][]string
	procs []*fakeProc
}

type fakeProc struct {
	done chan struct{}
	once sync.Once
}

func (p *fakeProc) PID() int           { return 4242 }
func (p *fakeProc) Wait() error        { <-p.done; return nil }
func (p *fakeProc) Stop(time.Duration) { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (r *fakeRunner) Start(_ context.Context, args []string) (agent.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &fakeProc{done: make(chan struct{})}
	r.args = append(r.args, args)
	r.procs = append(r.procs, p)
	return p, nil
}

func (r *fakeRunner) last() ([]string, *fakeProc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.procs) == 0 {
		return nil, nil
	}
	return r.args[len(r.args)-1], r.procs[len(r.procs)-1]
}

type profile struct {
	mu sync.Mutex
	s  reconcile.Settings
}

func (p *profile) Desired(context.Context) (reconcile.Settings, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.s, nil
}
func (p *profile) ReconcileTarget(context.Context, control.Target) error { return nil }

func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestAgentHubEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	panel := store.New(d)
	_ = panel.Settings.Set(ctx, relays.SettingAllowPrivateHosts, true, nil)
	ten := 10.0
	prof := &profile{s: reconcile.Settings{TotalBandwidthMbps: &ten, Blocklist: []string{"203.0.113.66"}}}
	svc, err := relays.New(relays.Config{Store: &relays.Store{DB: d}, Panel: panel, Bus: events.NewBus(nil),
		Pusher: prof, Global: prof, PublicKey: func() (string, error) { return hbbsKey, nil },
		Prober: &relays.Prober{Timeout: time.Second}, Interval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Route("/agent/v1", svc.Register)
	srv := httptest.NewServer(router)
	defer srv.Close()
	go svc.Run(ctx)

	// The relay's public data port (probed) and its loopback admin channel.
	relayData := hbbstest.NewFakeRelay(t, hbbsKey)
	hbbrAdmin := hbbstest.NewFakeAdmin(t)

	v, enr, err := svc.Create(ctx, relays.CreateInput{Kind: relays.KindAgent, Name: "fra-1", PublicHost: relayData.Addr(), Weight: 2, InRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	runner := &fakeRunner{}
	newAgent := func() *agent.Agent {
		a, err := agent.New(agent.Config{PanelURL: srv.URL, EnrollToken: enr.Token, DataDir: dir, Runner: runner,
			ControlAddr: hbbrAdmin.Addr(), StatusInterval: 100 * time.Millisecond, ApplyInterval: 200 * time.Millisecond,
			MaxBackoff: 200 * time.Millisecond, ProcDir: t.TempDir(), HBBRBin: "/nonexistent/hbbr"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	actx, acancel := context.WithCancel(ctx)
	a := newAgent()
	done := make(chan error, 1)
	go func() { done <- a.Run(actx) }()

	get := func() relays.View {
		v, err := svc.Get(ctx, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	eventually(t, "enrollment + session", func() bool { g := get(); return g.Agent.Enrolled() && g.Connected })
	if g := get(); g.Health != relays.HealthPending || g.Agent.Approved() {
		t.Fatalf("before approval: %s approved=%v", g.Health, g.Agent.Approved())
	}
	if args, _ := runner.last(); args != nil {
		t.Fatalf("hbbr started before approval: %v", args)
	}
	if fi, err := os.Stat(filepath.Join(dir, agent.KeyFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent.key: %v %v", fi, err)
	}

	// Approval: config v2 with run=true; hbbr is started with -k <pubkey>.
	if _, err := svc.Approve(ctx, v.ID, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "hbbr start", func() bool { args, _ := runner.last(); return args != nil })
	args, _ := runner.last()
	if strings.Join(args, " ") != "-p 21117 -k "+hbbsKey {
		t.Fatalf("hbbr args %v", args)
	}
	eventually(t, "ack of the running config", func() bool {
		g := get()
		return g.Agent.DesiredVersion >= 2 && g.Agent.AckedVersion == g.Agent.DesiredVersion
	})
	_, _, _, _, block, rates, _, _ := hbbrAdmin.State()
	if rates["total-bandwidth"] != 10 || len(block) != 1 || block[0] != "203.0.113.66" {
		t.Fatalf("hbbr state: rates %v block %v", rates, block)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, reconcile.BlocklistFile)); err != nil || string(raw) != "203.0.113.66" {
		t.Fatalf("blocklist.txt: %q %v", raw, err)
	}
	// Status reports reach the panel; probe + session + ack -> active, in rotation.
	eventually(t, "live status", func() bool { g := get(); return g.Live != nil && g.Live.HBBR.Up && g.Live.Usage.RelaysActive == 2 })
	eventually(t, "active", func() bool { svc.Pass(ctx, true); return get().Health == relays.HealthActive })
	if rs := svc.RelayServers(ctx); strings.Join(rs, ",") != relayData.Addr()+","+relayData.Addr() {
		t.Fatalf("rotation %v", rs)
	}
	// Live usage over the session.
	u, err := svc.Usage(ctx, v.ID)
	if err != nil || len(u) != 2 || u[0].Addr != "1.2.3.4:5555" {
		t.Fatalf("usage %+v %v", u, err)
	}

	// Per-relay override -> new version -> applied.
	five := 5.0
	if _, err := svc.SaveSettings(ctx, v.ID, reconcile.Settings{TotalBandwidthMbps: &five}, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "override applied", func() bool {
		_, _, _, _, _, rates, _, _ := hbbrAdmin.State()
		g := get()
		return rates["total-bandwidth"] == 5 && g.Agent.AckedVersion == g.Agent.DesiredVersion
	})

	// hbbr "restart" (state lost): the agent re-applies on the start event.
	_, proc := runner.last()
	hbbrAdmin.Restart()
	proc.Stop(0)
	eventually(t, "hbbr restarted and re-applied", func() bool {
		_, p := runner.last()
		_, _, _, _, _, rates, _, _ := hbbrAdmin.State()
		return p != proc && rates["total-bandwidth"] == 5
	})
	if st := a.Supervisor().Status(); st.Restarts < 1 {
		t.Fatalf("restarts %d", st.Restarts)
	}

	// Persisted state: a restarted agent keeps enforcing its config even
	// before the panel answers (the panel is gone here).
	acancel()
	<-done
	st, err := agent.LoadState(dir)
	if err != nil || st.AgentID != v.ID || st.Config == nil || !st.Config.Run || st.Config.HBBSPublicKey != hbbsKey {
		t.Fatalf("persisted state %+v %v", st, err)
	}
	srv.Close()
	runner2 := &fakeRunner{}
	a2, err := agent.New(agent.Config{PanelURL: srv.URL, DataDir: dir, Runner: runner2, ControlAddr: hbbrAdmin.Addr(),
		MaxBackoff: 100 * time.Millisecond, ProcDir: t.TempDir(), HBBRBin: "/nonexistent/hbbr", HealthInterval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	a2ctx, a2cancel := context.WithCancel(ctx)
	defer a2cancel()
	go func() { _ = a2.Run(a2ctx) }()
	eventually(t, "hbbr from persisted config", func() bool { args, _ := runner2.last(); return args != nil })
	eventually(t, "healthy", func() bool { return agent.CheckHealth(dir, time.Now(), time.Minute) == nil })
}

func TestAgentRevokedStopsHBBR(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, _ := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	panel := store.New(d)
	_ = panel.Settings.Set(ctx, relays.SettingRequireApproval, false, nil)
	prof := &profile{}
	svc, _ := relays.New(relays.Config{Store: &relays.Store{DB: d}, Panel: panel, Pusher: prof, Global: prof,
		PublicKey: func() (string, error) { return hbbsKey, nil }, Interval: 100 * time.Millisecond})
	router := chi.NewRouter()
	router.Route("/agent/v1", svc.Register)
	srv := httptest.NewServer(router)
	defer srv.Close()
	go svc.Run(ctx)
	v, enr, _ := svc.Create(ctx, relays.CreateInput{Kind: relays.KindAgent, Name: "x", PublicHost: "x.example.com", Weight: 1, InRotation: true})
	dir := t.TempDir()
	runner := &fakeRunner{}
	a, err := agent.New(agent.Config{PanelURL: srv.URL, EnrollToken: enr.Token, DataDir: dir, Runner: runner,
		ControlAddr: hbbstest.NewFakeAdmin(t).Addr(), MaxBackoff: 100 * time.Millisecond, ProcDir: t.TempDir(), HBBRBin: "/nonexistent/hbbr"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = a.Run(ctx) }()
	// No approval gate: hbbr starts right after enrollment.
	eventually(t, "hbbr start", func() bool { args, _ := runner.last(); return args != nil })
	if _, err := svc.Revoke(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	_, proc := runner.last()
	eventually(t, "hbbr stopped after revoke", func() bool { return !proc.running() })
	eventually(t, "state cleared", func() bool { return a.State().AgentID == 0 })
}
