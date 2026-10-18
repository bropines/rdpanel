package clientopts

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func startFakeSTUNServer(t *testing.T) (string, func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	done := make(chan struct{})

	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n >= 20 && buf[0] == 0x00 && buf[1] == 0x01 {
				// Echo back binding success response with same tx ID
				resp := make([]byte, 20)
				resp[0] = 0x01
				resp[1] = 0x01
				copy(resp[4:8], buf[4:8])   // magic cookie
				copy(resp[8:20], buf[8:20]) // tx id
				_, _ = pc.WriteTo(resp, addr)
			}
		}
	}()

	addr := pc.LocalAddr().(*net.UDPAddr)
	return net.JoinHostPort(addr.IP.String(), strconv.Itoa(addr.Port)), func() {
		_ = pc.Close()
		close(done)
	}
}

func TestProbeSTUNSuccess(t *testing.T) {
	addr, cleanup := startFakeSTUNServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	url := "stun:" + addr
	res := ProbeOne(ctx, url, true)
	if !res.Reachable {
		t.Fatalf("expected reachable, got error: %s", res.Error)
	}
	if res.RTTMs <= 0 {
		t.Fatalf("expected positive RTT, got %f", res.RTTMs)
	}
	if res.Scheme != "stun" {
		t.Fatalf("expected scheme stun, got %s", res.Scheme)
	}
}

func TestProbeSTUNSSRFGuard(t *testing.T) {
	addr, cleanup := startFakeSTUNServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	url := "stun:" + addr
	res := ProbeOne(ctx, url, false) // allowPrivate = false
	if res.Reachable {
		t.Fatalf("expected unreachable due to SSRF guard, got reachable")
	}
	if res.Error == "" || res.Error != "SSRF guard: private or local address refused" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
}

func TestProbeICEServersMultiple(t *testing.T) {
	addr, cleanup := startFakeSTUNServer(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	input := "stun:" + addr + "\nturn://user:pass@" + addr + "\nbadscheme://foo"
	results := ProbeICEServers(ctx, input, true)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if !results[0].Reachable {
		t.Fatalf("expected results[0] reachable, error: %s", results[0].Error)
	}
	if !results[1].Reachable {
		t.Fatalf("expected results[1] reachable, error: %s", results[1].Error)
	}
	if results[2].Reachable {
		t.Fatalf("expected results[2] unreachable for badscheme")
	}
}
