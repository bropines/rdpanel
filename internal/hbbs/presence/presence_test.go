package presence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/events"
	"github.com/bropines/rustdesk-panel/internal/hbbs/wire"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// fakeHBBS speaks the hbbs rendezvous TCP protocol as far as the panel uses it
// (src/rendezvous_server.rs handle_listener_inner/handle_tcp): on accept it
// sends a framed KeyExchange (key_exchange_phase1), reads one framed
// RendezvousMessage, answers an OnlineRequest with a framed OnlineResponse
// whose bitmap is MSB-first, then closes (handle_tcp returns false).
type fakeHBBS struct {
	ln       net.Listener
	mu       sync.Mutex
	online   map[string]bool
	requests [][]string
	// behaviour switches
	noKeyExchange atomic.Bool
	garbage       atomic.Bool
	shortBitmap   atomic.Bool
	silent        atomic.Bool
	conns         atomic.Int64
}

func newFakeHBBS(t *testing.T) *fakeHBBS {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeHBBS{ln: ln, online: map[string]bool{}}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeHBBS) addr() string { return f.ln.Addr().String() }

func (f *fakeHBBS) set(id string, on bool) {
	f.mu.Lock()
	f.online[id] = on
	f.mu.Unlock()
}

func (f *fakeHBBS) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.conns.Add(1)
		go f.handle(c)
	}
}

func (f *fakeHBBS) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if f.silent.Load() {
		buf := make([]byte, 1)
		_, _ = c.Read(buf) // hang until the client gives up
		time.Sleep(3 * time.Second)
		return
	}
	if !f.noKeyExchange.Load() {
		// sign(pk) = 64-byte signature || 32-byte key
		ke, _ := wire.AppendFrame(nil, wire.EncodeKeyExchange(bytes.Repeat([]byte{0x5A}, 96)))
		if _, err := c.Write(ke); err != nil {
			return
		}
	}
	msg, err := wire.ReadFrame(c, 1<<20)
	if err != nil {
		return
	}
	env, err := wire.DecodeEnvelope(msg)
	if err != nil || env.Field != wire.FieldOnlineRequest {
		return
	}
	peers, _ := wire.DecodeOnlineRequest(env.Body)
	f.mu.Lock()
	f.requests = append(f.requests, peers)
	states := make([]byte, wire.BitmapLen(len(peers)))
	for i, p := range peers {
		if f.online[p] {
			wire.SetOnline(states, i)
		}
	}
	f.mu.Unlock()
	if f.shortBitmap.Load() && len(states) > 0 {
		states = states[:len(states)-1]
	}
	var out []byte
	if f.garbage.Load() {
		out, _ = wire.AppendFrame(nil, []byte{0xFF, 0xFF, 0xFF})
	} else {
		out, _ = wire.AppendFrame(nil, wire.EncodeOnlineResponse(states))
	}
	_, _ = c.Write(out)
}

type staticLister struct {
	mu   sync.Mutex
	refs []DeviceRef
	err  error
}

func (s *staticLister) ListRustdeskIDs(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, len(s.refs))
	for i, r := range s.refs {
		ids[i] = r.ID
	}
	return ids, s.err
}

func (s *staticLister) ListDeviceRefs(context.Context) ([]DeviceRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DeviceRef(nil), s.refs...), s.err
}

type memLastSeen struct {
	mu     sync.Mutex
	writes []map[string]time.Time
}

func (m *memLastSeen) TouchLastSeen(_ context.Context, seen map[string]time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := map[string]time.Time{}
	for k, v := range seen {
		cp[k] = v
	}
	m.writes = append(m.writes, cp)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func owner(v int64) *int64 { return &v }

func TestPollerTransitionsEventsAndThrottle(t *testing.T) {
	hb := newFakeHBBS(t)
	lister := &staticLister{refs: []DeviceRef{
		{ID: "111111111", OwnerUserID: owner(7)},
		{ID: "222222222"},
		{ID: "333333333"},
	}}
	ls := &memLastSeen{}
	bus := events.NewBus(nil)
	sub := bus.Subscribe(nil, 64)
	defer sub.Close()
	clk := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	reg := prometheus.NewRegistry()
	p, err := New(Config{
		Addr: hb.addr(), Devices: lister, LastSeen: ls, Bus: bus, Registerer: reg,
		Now: clk.now, ChunkSize: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	hb.set("111111111", true)
	hb.set("333333333", true)
	p.PollOnce(ctx)

	// Chunking: 3 ids with chunk size 2 -> two requests.
	hb.mu.Lock()
	if len(hb.requests) != 2 || len(hb.requests[0]) != 2 || len(hb.requests[1]) != 1 {
		t.Fatalf("requests %v", hb.requests)
	}
	hb.mu.Unlock()

	if !p.IsOnline("111111111") || p.IsOnline("222222222") || !p.IsOnline("333333333") || p.IsOnline("nope") {
		t.Fatalf("snapshot %+v", p.Snapshot())
	}
	h := p.Health()
	if !h.Reachable || h.RTT <= 0 || h.Devices != 3 || h.Online != 2 || h.Polls != 1 {
		t.Fatalf("health %+v", h)
	}
	evs, _ := sub.Drain(nil)
	// hbbs.health (up) + two presence.changed (initially-online devices).
	if len(evs) != 3 || evs[0].Type != events.HBBSHealth {
		t.Fatalf("events %+v", evs)
	}
	pc := evs[1].Data.(events.Presence)
	if pc.ID != "111111111" || !pc.Online || pc.Source != SourceHBBS || evs[1].Audience.UserIDs[0] != 7 {
		t.Fatalf("presence event %+v %+v", pc, evs[1].Audience)
	}
	if !evs[2].Audience.AdminOnly {
		t.Fatal("ownerless device events are admin-only")
	}
	if len(ls.writes) != 1 || len(ls.writes[0]) != 2 {
		t.Fatalf("last seen writes %v", ls.writes)
	}

	// 5 s later: no change -> no events, no DB writes (throttled to 60 s).
	clk.add(5 * time.Second)
	p.PollOnce(ctx)
	if evs, _ := sub.Drain(nil); len(evs) != 0 {
		t.Fatalf("unexpected events %+v", evs)
	}
	if len(ls.writes) != 1 {
		t.Fatal("last_seen must be throttled")
	}

	// Device 1 goes offline, device 2 online.
	hb.set("111111111", false)
	hb.set("222222222", true)
	clk.add(5 * time.Second)
	p.PollOnce(ctx)
	evs, _ = sub.Drain(nil)
	if len(evs) != 2 {
		t.Fatalf("events %+v", evs)
	}
	off := evs[0].Data.(events.Presence)
	if off.ID != "111111111" || off.Online || !off.LastSeen.Equal(clk.now().Add(-5*time.Second)) {
		t.Fatalf("offline event %+v", off)
	}
	if len(ls.writes) != 2 || len(ls.writes[1]) != 1 { // only the newly online device
		t.Fatalf("writes %v", ls.writes)
	}
	st, _ := p.Get("111111111")
	if !st.Since.Equal(clk.now()) {
		t.Fatal("since must reset on transition")
	}

	// 61 s later device 3 is due again.
	clk.add(61 * time.Second)
	p.PollOnce(ctx)
	if len(ls.writes) != 3 || len(ls.writes[2]) != 2 {
		t.Fatalf("writes after 61s %v", ls.writes)
	}

	// Device removed from the panel disappears from the map.
	lister.mu.Lock()
	lister.refs = lister.refs[1:]
	lister.mu.Unlock()
	p.PollOnce(ctx)
	if _, ok := p.Get("111111111"); ok || len(p.Snapshot()) != 2 {
		t.Fatal("removed device still tracked")
	}

	if v := testutil.ToFloat64(p.mUp); v != 1 {
		t.Fatalf("up metric %v", v)
	}
	if v := testutil.ToFloat64(p.mOnline); v != 2 {
		t.Fatalf("online metric %v", v)
	}
}

func TestPollerUnreachableFallsBackToHeartbeat(t *testing.T) {
	hb := newFakeHBBS(t)
	clk := &clock{t: time.Now()}
	lister := &staticLister{refs: []DeviceRef{
		{ID: "aaaaaa", LastHeartbeatAt: clk.now().Add(-10 * time.Second)},
		{ID: "bbbbbb", LastHeartbeatAt: clk.now().Add(-10 * time.Minute)},
		{ID: "cccccc"},
	}}
	bus := events.NewBus(nil)
	sub := bus.Subscribe(nil, 64)
	defer sub.Close()
	p, _ := New(Config{Addr: hb.addr(), Devices: lister, Bus: bus, Now: clk.now})
	hb.set("bbbbbb", true)
	p.PollOnce(context.Background())
	if !p.IsOnline("bbbbbb") || p.IsOnline("aaaaaa") {
		t.Fatal("hbbs state")
	}
	sub.Drain(nil)

	hb.ln.Close()
	p.PollOnce(context.Background())
	h := p.Health()
	if h.Reachable || h.LastError == "" || h.Failures != 1 {
		t.Fatalf("health %+v", h)
	}
	if !p.IsOnline("aaaaaa") || p.IsOnline("bbbbbb") || p.IsOnline("cccccc") {
		t.Fatalf("fallback %+v", p.Snapshot())
	}
	st, _ := p.Get("aaaaaa")
	if st.Source != SourceHeartbeat {
		t.Fatal("source")
	}
	evs, _ := sub.Drain(nil)
	var health events.Health
	n := 0
	for _, e := range evs {
		switch d := e.Data.(type) {
		case events.Health:
			health = d
		case events.Presence:
			n++
		}
	}
	if health.Up || health.Component != "hbbs" || n != 2 {
		t.Fatalf("events %+v", evs)
	}
}

func TestQueryProtocolEdgeCases(t *testing.T) {
	hb := newFakeHBBS(t)
	p, _ := New(Config{Addr: hb.addr(), Devices: &staticLister{}, Timeout: 500 * time.Millisecond})
	ctx := context.Background()

	// Without KeyExchange (e.g. hbbs started without a key) it still works.
	hb.noKeyExchange.Store(true)
	hb.set("x1x1x1", true)
	got, _, err := p.Query(ctx, []string{"x1x1x1", "y2y2y2"})
	if err != nil || !got[0] || got[1] {
		t.Fatalf("%v %v", got, err)
	}
	hb.noKeyExchange.Store(false)

	// Empty id list: one empty round trip (health probe).
	before := hb.conns.Load()
	if got, rtt, err := p.Query(ctx, nil); err != nil || len(got) != 0 || rtt <= 0 {
		t.Fatalf("empty: %v %v %v", got, rtt, err)
	}
	if hb.conns.Load() != before+1 {
		t.Fatal("empty query must still probe")
	}

	// Bitmap of the wrong size.
	hb.shortBitmap.Store(true)
	if _, _, err := p.Query(ctx, []string{"a1a1a1"}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("short bitmap: %v", err)
	}
	hb.shortBitmap.Store(false)

	// Undecodable protobuf.
	hb.garbage.Store(true)
	if _, _, err := p.Query(ctx, []string{"a1a1a1"}); !errors.Is(err, ErrBadResponse) {
		t.Fatalf("garbage: %v", err)
	}
	hb.garbage.Store(false)

	// Peer accepts but never answers -> timeout.
	hb.silent.Store(true)
	start := time.Now()
	if _, _, err := p.Query(ctx, []string{"a1a1a1"}); err == nil {
		t.Fatal("silent peer must time out")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not honoured")
	}
	hb.silent.Store(false)

	// Cancelled context aborts quickly.
	hb.silent.Store(true)
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, _, err := p.Query(cctx, []string{"a1a1a1"}); err == nil {
		t.Fatal("cancel must abort")
	}
	hb.silent.Store(false)
}

func TestManyIDsBitmapOrder(t *testing.T) {
	hb := newFakeHBBS(t)
	ids := make([]string, 1203)
	for i := range ids {
		ids[i] = fmt.Sprintf("%09d", 100000000+i)
		if i%3 == 0 || i == 1202 {
			hb.set(ids[i], true)
		}
	}
	p, _ := New(Config{Addr: hb.addr(), Devices: &staticLister{}})
	got, _, err := p.Query(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	for i, on := range got {
		if want := i%3 == 0 || i == 1202; on != want {
			t.Fatalf("id %d: %v", i, on)
		}
	}
	hb.mu.Lock()
	defer hb.mu.Unlock()
	if len(hb.requests) != 3 {
		t.Fatalf("chunks: %d", len(hb.requests))
	}
}

func TestSQLDevices(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	st := store.New(d)
	u := &store.User{Username: "alice"}
	if err := st.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for i, id := range []string{"100000001", "100000002"} {
		var ownerID any
		if i == 0 {
			ownerID = u.ID
		}
		if _, err := d.ExecContext(ctx, `INSERT INTO devices (rustdesk_id, owner_user_id, last_heartbeat_at, last_seen_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, id, ownerID, now, now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	s := SQLDevices{DB: d.DB}
	ids, err := s.ListRustdeskIDs(ctx)
	if err != nil || len(ids) != 2 || ids[0] != "100000001" {
		t.Fatalf("%v %v", ids, err)
	}
	refs, err := s.ListDeviceRefs(ctx)
	if err != nil || refs[0].OwnerUserID == nil || *refs[0].OwnerUserID != u.ID || refs[1].OwnerUserID != nil ||
		refs[0].LastHeartbeatAt.UnixMilli() != now {
		t.Fatalf("%+v %v", refs, err)
	}
	// Forward-only update.
	err = s.TouchLastSeen(ctx, map[string]time.Time{
		"100000001": time.UnixMilli(now + 1000),
		"100000002": time.UnixMilli(now - 1000),
		"missing":   time.UnixMilli(now),
	})
	if err != nil {
		t.Fatal(err)
	}
	var a, b int64
	_ = d.QueryRowContext(ctx, `SELECT last_seen_at FROM devices WHERE rustdesk_id='100000001'`).Scan(&a)
	_ = d.QueryRowContext(ctx, `SELECT last_seen_at FROM devices WHERE rustdesk_id='100000002'`).Scan(&b)
	if a != now+1000 || b != now {
		t.Fatalf("a=%d b=%d now=%d", a, b, now)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	hb := newFakeHBBS(t)
	p, _ := New(Config{Addr: hb.addr(), Devices: &staticLister{}, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	// Wait for at least two polls instead of sleeping a fixed time: under
	// -race with parallel packages a fixed 60 ms window is not always enough.
	deadline := time.Now().Add(3 * time.Second)
	for p.Health().Polls < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := p.Health().Polls; got < 2 {
		t.Fatalf("polls %d", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}
