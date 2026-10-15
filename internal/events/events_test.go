package events

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBusFanOut(t *testing.T) {
	b := NewBus(prometheus.NewRegistry())
	s1 := b.Subscribe(nil, 8)
	s2 := b.Subscribe(func(e Event) bool { return e.Type == HBBSHealth }, 8)
	defer s1.Close()
	defer s2.Close()

	b.Publish(PresenceChanged, Presence{ID: "a"}, Audience{})
	b.Publish(HBBSHealth, Health{Component: "hbbs", Up: true}, Admins)

	got1, d1 := s1.Drain(nil)
	got2, d2 := s2.Drain(nil)
	if len(got1) != 2 || len(got2) != 1 || d1 != 0 || d2 != 0 {
		t.Fatalf("s1=%d s2=%d", len(got1), len(got2))
	}
	if got1[0].ID >= got1[1].ID || got1[0].Time.IsZero() {
		t.Fatal("ids must increase and time must be set")
	}
	if got2[0].Type != HBBSHealth {
		t.Fatal("filter")
	}
	if b.Subscribers() != 2 {
		t.Fatal("subscribers")
	}
	s1.Close()
	s1.Close() // idempotent
	b.Publish(PresenceChanged, nil, Audience{})
	if got, _ := s1.Drain(nil); len(got) != 0 {
		t.Fatal("closed subscription must not receive")
	}
	if b.Subscribers() != 1 {
		t.Fatal("subscribers after close")
	}
	var nilBus *Bus
	nilBus.Publish(PresenceChanged, nil, Audience{}) // no panic
}

func TestBusBackpressureDropsOldest(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := NewBus(reg)
	s := b.Subscribe(nil, 3)
	defer s.Close()
	for i := 0; i < 10; i++ {
		b.Publish(PresenceChanged, i, Audience{})
	}
	got, dropped := s.Drain(nil)
	if dropped != 7 || len(got) != 3 {
		t.Fatalf("dropped=%d len=%d", dropped, len(got))
	}
	for i, e := range got {
		if e.Data.(int) != 7+i {
			t.Fatalf("kept %v, want newest", e.Data)
		}
	}
	if s.Dropped() != 7 {
		t.Fatal("lifetime drops")
	}
	if _, d := s.Drain(nil); d != 0 {
		t.Fatal("drop counter must reset on drain")
	}
	if v := testutil.ToFloat64(b.dropped); v != 7 {
		t.Fatalf("metric %v", v)
	}
}

func TestBusNextAndConcurrency(t *testing.T) {
	b := NewBus(nil)
	s := b.Subscribe(nil, 1024)
	defer s.Close()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.Publish(DeviceUpdated, i, Audience{})
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n := 0
	for n < 800 {
		if _, err := s.Next(ctx); err != nil {
			t.Fatalf("after %d: %v", n, err)
		}
		n++
	}
	wg.Wait()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := s.Next(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestVisibility(t *testing.T) {
	admin := Principal{UserID: 1, Role: RoleAdmin}
	alice := Principal{UserID: 2, Role: "user"}
	bob := Principal{UserID: 3, Role: "user"}
	for _, tc := range []struct {
		aud               Audience
		admin, alice, bob bool
	}{
		{Audience{}, true, true, true},
		{Admins, true, false, false},
		{Audience{UserIDs: []int64{2}}, true, true, false},
	} {
		e := Event{Audience: tc.aud}
		if e.VisibleTo(admin) != tc.admin || e.VisibleTo(alice) != tc.alice || e.VisibleTo(bob) != tc.bob {
			t.Errorf("%+v", tc.aud)
		}
	}
}

// --- SSE ---

type testAuth struct {
	mu   sync.Mutex
	p    map[string]Principal // by X-Test-User header
	fail atomic.Bool
}

func (a *testAuth) Authenticate(r *http.Request) (Principal, error) {
	if a.fail.Load() {
		return Principal{}, ErrUnauthenticated
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.p[r.Header.Get("X-Test-User")]
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}

type sseReader struct {
	t  *testing.T
	sc *bufio.Scanner
}

// next returns the next SSE block (lines up to a blank line).
func (r *sseReader) next() []string {
	r.t.Helper()
	var lines []string
	for r.sc.Scan() {
		l := r.sc.Text()
		if l == "" {
			if len(lines) > 0 {
				return lines
			}
			continue
		}
		lines = append(lines, l)
	}
	r.t.Fatalf("stream ended: %v (partial %q)", r.sc.Err(), lines)
	return nil
}

func openStream(t *testing.T, srv *httptest.Server, user, query string) (*http.Response, *sseReader) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events/"+query, nil)
	req.Header.Set("X-Test-User", user)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, &sseReader{t: t, sc: bufio.NewScanner(resp.Body)}
}

func newSSEServer(t *testing.T, d Deps) *httptest.Server {
	r := chi.NewRouter()
	r.Route("/api/v1/events", func(r chi.Router) { Register(r, d) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func waitSubs(t *testing.T, b *Bus, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for b.Subscribers() != n {
		if time.Now().After(deadline) {
			t.Fatalf("subscribers=%d want %d", b.Subscribers(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSSEDefaultDeny(t *testing.T) {
	srv := newSSEServer(t, Deps{Bus: NewBus(nil)})
	resp, _ := openStream(t, srv, "admin", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSSEStreamsAndFiltersByRole(t *testing.T) {
	bus := NewBus(nil)
	auth := &testAuth{p: map[string]Principal{
		"admin": {UserID: 1, Role: RoleAdmin},
		"alice": {UserID: 2, Role: "user"},
	}}
	srv := newSSEServer(t, Deps{Bus: bus, Auth: auth, Keepalive: time.Hour})

	if resp, _ := openStream(t, srv, "mallory", ""); resp.StatusCode != 401 {
		t.Fatalf("unknown user: %d", resp.StatusCode)
	}

	aResp, admin := openStream(t, srv, "admin", "")
	if aResp.StatusCode != 200 || !strings.HasPrefix(aResp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("admin open: %d %s", aResp.StatusCode, aResp.Header.Get("Content-Type"))
	}
	if aResp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("missing X-Accel-Buffering")
	}
	if l := admin.next(); l[0] != "retry: 5000" {
		t.Fatalf("preamble %q", l)
	}
	_, alice := openStream(t, srv, "alice", "")
	alice.next()
	waitSubs(t, bus, 2)

	bus.Publish(HBBSHealth, Health{Component: "hbbs", Up: false, Error: "refused"}, Admins)
	bus.Publish(PresenceChanged, Presence{ID: "111111", Online: true, Source: "hbbs"}, Audience{UserIDs: []int64{3}})
	bus.Publish(PresenceChanged, Presence{ID: "222222", Online: true, Source: "hbbs"}, Audience{UserIDs: []int64{2}})

	ev := admin.next()
	if ev[1] != "event: hbbs.health" || !strings.Contains(ev[2], `"component":"hbbs"`) || !strings.HasPrefix(ev[0], "id: ") {
		t.Fatalf("admin first event %q", ev)
	}
	if ev = admin.next(); !strings.Contains(ev[2], "111111") {
		t.Fatalf("admin second %q", ev)
	}
	if ev = admin.next(); !strings.Contains(ev[2], "222222") {
		t.Fatalf("admin third %q", ev)
	}
	// Alice sees only her own device.
	if ev = alice.next(); ev[1] != "event: presence.changed" || !strings.Contains(ev[2], `"id":"222222"`) {
		t.Fatalf("alice %q", ev)
	}
}

func TestSSETypeFilterKeepaliveAndRevocation(t *testing.T) {
	bus := NewBus(nil)
	auth := &testAuth{p: map[string]Principal{"admin": {UserID: 1, Role: RoleAdmin}}}
	srv := newSSEServer(t, Deps{Bus: bus, Auth: auth, Keepalive: 50 * time.Millisecond})
	_, s := openStream(t, srv, "admin", "?types=device.updated")
	s.next()
	waitSubs(t, bus, 1)
	bus.Publish(PresenceChanged, Presence{ID: "x"}, Audience{})
	bus.Publish(DeviceUpdated, DeviceUpdate{ID: "y", Fields: []string{"alias"}}, Audience{})
	for {
		ev := s.next()
		if ev[0] == ": keepalive" {
			continue
		}
		if ev[1] != "event: device.updated" || ev[2] != `data: {"id":"y","fields":["alias"]}` {
			t.Fatalf("got %q", ev)
		}
		break
	}
	if ev := s.next(); ev[0] != ": keepalive" {
		t.Fatalf("want keepalive, got %q", ev)
	}
	auth.fail.Store(true)
	for {
		ev := s.next()
		if ev[0] == "event: unauthenticated" {
			break
		}
	}
	waitSubs(t, bus, 0)
}
