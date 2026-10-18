package relays

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/wire"
)

// DefaultRelayPort is hbbr's TCP port when public_host has none.
const DefaultRelayPort = 21117

// ErrPrivateHost is returned for probe targets in private, loopback or
// link-local ranges while private relay hosts are not allowed (SSRF guard).
var ErrPrivateHost = errors.New("relay host resolves to a private, loopback or link-local address (enable \"allow private relay hosts\" to probe it)")

// Prober runs the end-to-end relay probe (docs/design/relay-agents.md §6.3):
// two TCP connections send RequestRelay with the same random UUID and the
// hbbs public key, hbbr pairs them, and 32 random bytes written on one must
// arrive on the other. This proves DNS, reachability, the relay key and data
// flow; hbbs' own check only connects.
type Prober struct {
	// Dial is injectable for tests (default net.Dialer).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Lookup resolves host names (default net.DefaultResolver.LookupNetIP).
	Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
	// Timeout bounds the whole probe (default 5 s).
	Timeout time.Duration
}

// ProbeResult is one probe outcome.
type ProbeResult struct {
	At  time.Time
	RTT time.Duration // data round trip (write on A -> read on B)
	Err error
	// Addr is the address that was dialed.
	Addr string
}

// OK reports success.
func (r ProbeResult) OK() bool { return r.Err == nil }

func (p *Prober) dial(ctx context.Context, addr string) (net.Conn, error) {
	if p.Dial != nil {
		return p.Dial(ctx, "tcp", addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

// SplitHost returns host and port of a relay public host (default 21117).
func SplitHost(h string) (string, int, error) {
	if host, port, err := net.SplitHostPort(h); err == nil {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", 0, fmt.Errorf("invalid port in %q", h)
		}
		return host, n, nil
	}
	return h, DefaultRelayPort, nil
}

// IsPrivate reports addresses the SSRF guard refuses by default.
func IsPrivate(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// Resolve picks the address to probe for publicHost and applies the SSRF guard.
func (p *Prober) Resolve(ctx context.Context, publicHost string, allowPrivate bool) (string, error) {
	host, port, err := SplitHost(publicHost)
	if err != nil {
		return "", err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		lookup := p.Lookup
		if lookup == nil {
			lookup = net.DefaultResolver.LookupNetIP
		}
		if addrs, err = lookup(ctx, "ip", host); err != nil {
			return "", fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return "", fmt.Errorf("resolve %s: no addresses", host)
		}
	}
	// Prefer IPv4 (what most clients use), else the first address.
	pick := addrs[0]
	for _, a := range addrs {
		if a.Unmap().Is4() {
			pick = a
			break
		}
	}
	pick = pick.Unmap()
	if !allowPrivate && IsPrivate(pick) {
		return "", ErrPrivateHost
	}
	// Dial the resolved address: no second lookup that could be rebound.
	return net.JoinHostPort(pick.String(), strconv.Itoa(port)), nil
}

// Probe runs one end-to-end probe of publicHost presenting key.
func (p *Prober) Probe(ctx context.Context, publicHost, key string, allowPrivate bool) ProbeResult {
	res := ProbeResult{At: time.Now()}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr, err := p.Resolve(ctx, publicHost, allowPrivate)
	if err != nil {
		res.Err = err
		return res
	}
	res.Addr = addr
	res.RTT, res.Err = p.pair(ctx, addr, key)
	return res
}

func (p *Prober) pair(ctx context.Context, addr, key string) (time.Duration, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	uuid := "rdpanel-probe-" + hex.EncodeToString(raw[:])
	hello, err := wire.AppendFrame(nil, wire.EncodeRequestRelay(wire.RequestRelay{UUID: uuid, LicenceKey: key}))
	if err != nil {
		return 0, err
	}
	open := func() (net.Conn, error) {
		c, err := p.dial(ctx, addr)
		if err != nil {
			return nil, fmt.Errorf("connect %s: %w", addr, err)
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = c.SetDeadline(dl)
		}
		if _, err := c.Write(hello); err != nil {
			c.Close()
			return nil, fmt.Errorf("send request: %w", err)
		}
		return c, nil
	}
	a, err := open()
	if err != nil {
		return 0, err
	}
	defer a.Close()
	stop := context.AfterFunc(ctx, func() { _ = a.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	// hbbr registers the first connection of a UUID before the second one
	// can pair with it; give it a moment to read the first request.
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	b, err := open()
	if err != nil {
		return 0, err
	}
	defer b.Close()
	stopB := context.AfterFunc(ctx, func() { _ = b.SetDeadline(time.Unix(1, 0)) })
	defer stopB()

	var payload [32]byte
	if _, err := rand.Read(payload[:]); err != nil {
		return 0, err
	}
	frame, _ := wire.AppendFrame(nil, payload[:])
	// hbbr pairs on the second request; data written before that is
	// buffered by TCP and spliced once paired.
	start := time.Now()
	if _, err := a.Write(frame); err != nil {
		return 0, fmt.Errorf("relay closed the connection (wrong key?): %w", err)
	}
	got, err := wire.ReadFrame(b, 1<<10)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return 0, errors.New("relay closed the connection before relaying data (wrong key, or not an hbbr?)")
	case err != nil:
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, errors.New("timeout: no data relayed (wrong key, blocked IP, or not an hbbr?)")
		}
		return 0, fmt.Errorf("read: %w", err)
	case !bytes.Equal(got, payload[:]):
		return 0, errors.New("relay returned different bytes")
	}
	return time.Since(start), nil
}

// TCPCheck attempts a lightweight TCP connect to hostPort to verify reachability.
// It applies the SSRF guard via Resolve and closes the connection immediately.
// See docs/design/relay-agents.md §17.
func (p *Prober) TCPCheck(ctx context.Context, hostPort string, allowPrivate bool) error {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr, err := p.Resolve(ctx, hostPort, allowPrivate)
	if err != nil {
		return err
	}
	conn, err := p.dial(ctx, addr)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}
