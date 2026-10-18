package relays

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
)

const testKey = "OeVuKk5nlHiXp+APNn0Y3pC1Iwpwn44JGqrQCsWqmBw="

func TestProbeOK(t *testing.T) {
	fr := hbbstest.NewFakeRelay(t, testKey)
	p := &Prober{Timeout: 3 * time.Second}
	res := p.Probe(context.Background(), fr.Addr(), testKey, true)
	if !res.OK() || res.RTT <= 0 {
		t.Fatalf("probe: %+v", res)
	}
	if fr.Paired.Load() != 1 {
		t.Fatalf("paired %d", fr.Paired.Load())
	}
}

func TestProbeWrongKey(t *testing.T) {
	fr := hbbstest.NewFakeRelay(t, "another-key")
	res := (&Prober{Timeout: 2 * time.Second}).Probe(context.Background(), fr.Addr(), testKey, true)
	if res.OK() || !strings.Contains(res.Err.Error(), "wrong key") {
		t.Fatalf("want wrong-key failure, got %v", res.Err)
	}
	if fr.AuthFailed.Load() == 0 {
		t.Fatal("fake relay saw no auth failure")
	}
}

func TestProbeNoData(t *testing.T) {
	fr := hbbstest.NewFakeRelay(t, "")
	fr.Blackhole.Store(true)
	res := (&Prober{Timeout: 500 * time.Millisecond}).Probe(context.Background(), fr.Addr(), testKey, true)
	if res.OK() {
		t.Fatal("blackhole relay passed")
	}
}

func TestProbeClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	res := (&Prober{Timeout: time.Second}).Probe(context.Background(), addr, testKey, true)
	if res.OK() || !strings.Contains(res.Err.Error(), "connect") {
		t.Fatalf("closed port: %v", res.Err)
	}
}

func TestProbeSSRFGuard(t *testing.T) {
	fr := hbbstest.NewFakeRelay(t, "")
	p := &Prober{Timeout: time.Second}
	if res := p.Probe(context.Background(), fr.Addr(), testKey, false); !errors.Is(res.Err, ErrPrivateHost) {
		t.Fatalf("loopback probe without allow_private: %v", res.Err)
	}
	// Names are resolved once and the resolved address is checked and dialed.
	p.Lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	if res := p.Probe(context.Background(), "relay.example.com", testKey, false); !errors.Is(res.Err, ErrPrivateHost) {
		t.Fatalf("private name: %v", res.Err)
	}
	p.Lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("203.0.113.9")}, nil
	}
	addr, err := p.Resolve(context.Background(), "relay.example.com:21200", false)
	if err != nil || addr != "203.0.113.9:21200" {
		t.Fatalf("resolve prefers IPv4: %q %v", addr, err)
	}
	for _, a := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1", "fe80::1", "fd00::1", "0.0.0.0"} {
		if !IsPrivate(netip.MustParseAddr(a)) {
			t.Errorf("%s not private", a)
		}
	}
	if IsPrivate(netip.MustParseAddr("203.0.113.9")) {
		t.Error("public address flagged")
	}
}

func TestTCPCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	p := &Prober{Timeout: time.Second}
	// Loopback with allowPrivate=true succeeds
	if err := p.TCPCheck(context.Background(), addr, true); err != nil {
		t.Fatalf("TCPCheck open port: %v", err)
	}
	// Loopback without allowPrivate fails with ErrPrivateHost
	if err := p.TCPCheck(context.Background(), addr, false); !errors.Is(err, ErrPrivateHost) {
		t.Fatalf("TCPCheck private without allowPrivate: %v", err)
	}
	// Closed port fails
	closedLn, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closedLn.Addr().String()
	closedLn.Close()
	if err := p.TCPCheck(context.Background(), closedAddr, true); err == nil {
		t.Fatal("TCPCheck closed port should fail")
	}
}
