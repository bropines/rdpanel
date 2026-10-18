package clientopts

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProbeResult holds reachability and latency for one ICE server.
type ProbeResult struct {
	Server    string  `json:"server"`
	Scheme    string  `json:"scheme"`
	Reachable bool    `json:"reachable"`
	RTTMs     float64 `json:"rtt_ms,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// DefaultSTUNPort is the standard UDP port for STUN/TURN.
const DefaultSTUNPort = 3478

// DefaultSTUNSPort is the standard TLS port for STUNS/TURNS.
const DefaultSTUNSPort = 5349

// ProbeICEServers probes every server in iceServers in parallel (max MaxICEServers).
func ProbeICEServers(ctx context.Context, iceServers string, allowPrivate bool) []ProbeResult {
	entries := strings.FieldsFunc(iceServers, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	var clean []string
	for _, e := range entries {
		s := strings.TrimSpace(e)
		if s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) > MaxICEServers {
		clean = clean[:MaxICEServers]
	}
	if len(clean) == 0 {
		return nil
	}

	results := make([]ProbeResult, len(clean))
	var wg sync.WaitGroup
	for i, entry := range clean {
		wg.Add(1)
		go func(idx int, raw string) {
			defer wg.Done()
			results[idx] = ProbeOne(ctx, raw, allowPrivate)
		}(i, entry)
	}
	wg.Wait()
	return results
}

// ProbeOne probes a single ICE server (STUN or TURN).
func ProbeOne(ctx context.Context, raw string, allowPrivate bool) ProbeResult {
	res := ProbeResult{Server: raw}
	u, err := url.Parse(raw)
	if err != nil {
		res.Error = "invalid URL"
		return res
	}
	res.Scheme = u.Scheme

	var host string
	var port int

	switch u.Scheme {
	case "stun", "turn":
		port = DefaultSTUNPort
	case "stuns", "turns":
		port = DefaultSTUNSPort
	default:
		res.Error = fmt.Sprintf("unsupported scheme %q", u.Scheme)
		return res
	}

	if u.Host != "" {
		host = u.Hostname()
		if pStr := u.Port(); pStr != "" {
			if p, err := strconv.Atoi(pStr); err == nil && p > 0 && p <= 65535 {
				port = p
			}
		}
	} else if u.Opaque != "" {
		opaque := u.Opaque
		if h, pStr, err := net.SplitHostPort(opaque); err == nil {
			host = strings.Trim(h, "[]")
			if p, err := strconv.Atoi(pStr); err == nil && p > 0 && p <= 65535 {
				port = p
			}
		} else {
			host = strings.Trim(opaque, "[]")
		}
	}

	if host == "" {
		res.Error = "missing host"
		return res
	}

	// Resolve host and apply SSRF protection
	ipAddrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ipAddrs) == 0 {
		res.Error = fmt.Sprintf("resolve failed: %v", err)
		return res
	}

	targetIP := ipAddrs[0]
	if !allowPrivate && isPrivateIP(targetIP) {
		res.Error = "SSRF guard: private or local address refused"
		return res
	}

	targetAddr := net.JoinHostPort(targetIP.String(), strconv.Itoa(port))

	if u.Scheme == "stuns" || u.Scheme == "turns" {
		return probeTLS(ctx, res, targetAddr)
	}
	return probeUDP(ctx, res, targetAddr)
}

func probeUDP(ctx context.Context, res ProbeResult, targetAddr string) ProbeResult {
	raddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		res.Error = err.Error()
		return res
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	// RFC 5389 Binding Request (20 bytes)
	// 0..1: 0x0001 (Binding Request)
	// 2..3: 0x0000 (Message Length)
	// 4..7: 0x2112a442 (Magic Cookie)
	// 8..19: 12 bytes Transaction ID
	req := make([]byte, 20)
	req[0] = 0x00
	req[1] = 0x01
	req[4] = 0x21
	req[5] = 0x12
	req[6] = 0xa4
	req[7] = 0x42
	if _, err := rand.Read(req[8:20]); err != nil {
		res.Error = "crypto rand failed"
		return res
	}

	start := time.Now()
	if _, err := conn.Write(req); err != nil {
		res.Error = fmt.Sprintf("send error: %v", err)
		return res
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		res.Error = "timeout or no response"
		return res
	}
	elapsed := time.Since(start)

	if n < 20 {
		res.Error = "malformed response (too short)"
		return res
	}
	// Verify Binding Success Response (0x0101) or Error Response (0x0111)
	if (buf[0] != 0x01 || (buf[1] != 0x01 && buf[1] != 0x11)) ||
		buf[4] != 0x21 || buf[5] != 0x12 || buf[6] != 0xa4 || buf[7] != 0x42 ||
		!strings.EqualFold(string(buf[8:20]), string(req[8:20])) {
		res.Error = "unexpected STUN response"
		return res
	}

	res.Reachable = true
	res.RTTMs = float64(elapsed.Microseconds()) / 1000.0
	return res
}

func probeTLS(ctx context.Context, res ProbeResult, targetAddr string) ProbeResult {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	start := time.Now()
	conn, err := tls.DialWithDialer(dialer, "tcp", targetAddr, &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	_ = conn.Close()
	elapsed := time.Since(start)

	res.Reachable = true
	res.RTTMs = float64(elapsed.Microseconds()) / 1000.0
	return res
}

func isPrivateIP(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}
