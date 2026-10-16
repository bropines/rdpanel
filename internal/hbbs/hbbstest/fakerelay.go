package hbbstest

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/wire"
)

// FakeRelay models hbbr's relay data path (src/relay_server.rs
// handle_connection / make_pair): the first frame of a connection must be a
// RendezvousMessage{request_relay}; with a non-empty Key, a licence_key that
// differs closes the connection ("Relay authentication failed"). The first
// connection of a UUID waits for its peer; the second one is paired and the
// two byte streams are spliced. Written from the research notes, not copied.
type FakeRelay struct {
	ln  net.Listener
	key atomic.Value // string

	mu      sync.Mutex
	waiting map[string]net.Conn
	// Paired counts completed pairings; AuthFailed counts key mismatches.
	Paired     atomic.Int64
	AuthFailed atomic.Int64
	// Blackhole pairs connections but never forwards data.
	Blackhole atomic.Bool
}

// NewFakeRelay starts a fake hbbr on 127.0.0.1:0 that expects key ("" accepts
// any licence key, as hbbr without -k does).
func NewFakeRelay(tb testing.TB, key string) *FakeRelay {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	f := &FakeRelay{ln: ln, waiting: map[string]net.Conn{}}
	f.key.Store(key)
	go f.serve()
	tb.Cleanup(f.Close)
	return f
}

// Addr is the listen address (host:port).
func (f *FakeRelay) Addr() string { return f.ln.Addr().String() }

// SetKey changes the expected licence key (simulates a relay with a foreign key).
func (f *FakeRelay) SetKey(k string) { f.key.Store(k) }

// Close stops the listener and drops waiting connections.
func (f *FakeRelay) Close() {
	_ = f.ln.Close()
	f.mu.Lock()
	for k, c := range f.waiting {
		_ = c.Close()
		delete(f.waiting, k)
	}
	f.mu.Unlock()
}

func (f *FakeRelay) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *FakeRelay) handle(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	msg, err := wire.ReadFrame(c, 1<<16)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	env, err := wire.DecodeEnvelope(msg)
	if err != nil || env.Field != wire.FieldRequestRelay {
		_ = c.Close()
		return
	}
	rr, err := wire.DecodeRequestRelay(env.Body)
	if err != nil || rr.UUID == "" {
		_ = c.Close()
		return
	}
	if k := f.key.Load().(string); k != "" && rr.LicenceKey != k {
		f.AuthFailed.Add(1)
		_ = c.Close()
		return
	}
	f.mu.Lock()
	peer, ok := f.waiting[rr.UUID]
	if !ok {
		f.waiting[rr.UUID] = c
		f.mu.Unlock()
		return
	}
	delete(f.waiting, rr.UUID)
	f.mu.Unlock()
	f.Paired.Add(1)
	if f.Blackhole.Load() {
		go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close(); _ = peer.Close() }()
		go func() { _, _ = io.Copy(io.Discard, peer) }()
		return
	}
	splice := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		_ = dst.Close()
		_ = src.Close()
	}
	go splice(c, peer)
	go splice(peer, c)
}
