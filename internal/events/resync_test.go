package events

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// gatedWriter blocks Write while the gate is closed, simulating a slow client.
type gatedWriter struct {
	http.ResponseWriter
	gate chan struct{} // receive = permission to write
}

func (g *gatedWriter) Write(b []byte) (int, error) {
	<-g.gate
	return g.ResponseWriter.Write(b)
}

func (g *gatedWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func TestSSEResyncOnDrops(t *testing.T) {
	bus := NewBus(nil)
	auth := &testAuth{p: map[string]Principal{"admin": {UserID: 1, Role: RoleAdmin}}}
	h := Handler(Deps{Bus: bus, Auth: auth, Keepalive: time.Hour, Buffer: 2})
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&gatedWriter{ResponseWriter: w, gate: gate}, r)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("X-Test-User", "admin")
	respCh := make(chan *http.Response, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Error(err)
			close(respCh)
			return
		}
		respCh <- resp
	}()
	gate <- struct{}{} // allow the "retry:" preamble
	resp := <-respCh
	if resp == nil {
		t.FailNow()
	}
	defer resp.Body.Close()
	s := &sseReader{t: t, sc: bufio.NewScanner(resp.Body)}
	s.next()
	waitSubs(t, bus, 1)

	// First event: the handler drains it and blocks writing it.
	bus.Publish(DeviceUpdated, DeviceUpdate{ID: "first"}, Audience{})
	time.Sleep(50 * time.Millisecond)
	// Burst while the client is stuck: queue holds 2, the rest is dropped.
	for i := 0; i < 50; i++ {
		bus.Publish(DeviceUpdated, DeviceUpdate{ID: "burst"}, Audience{})
	}
	close(gate) // the client catches up

	var got []string
	for len(got) < 4 {
		ev := s.next()
		got = append(got, strings.Join(ev, "|"))
	}
	if !strings.Contains(got[0], "first") {
		t.Fatalf("first %q", got[0])
	}
	if got[1] != `event: resync|data: {"dropped":48}` {
		t.Fatalf("resync %q", got[1])
	}
	if !strings.Contains(got[2], "burst") || !strings.Contains(got[3], "burst") {
		t.Fatalf("kept newest %q", got[2:])
	}
}
