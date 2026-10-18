// Package hbbstest provides test doubles for hbbs/hbbr.
package hbbstest

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// FakeAdmin models the hbbs/hbbr loopback admin channel as implemented in
// src/rendezvous_server.rs (check_cmd, handle_listener2) and
// src/relay_server.rs (check_cmd, handle_connection): one read() of at most
// 1024 bytes with a 1 s timeout, cmd.trim().split(' '), one write of the
// reply, then close. State changes mirror the Rust code closely enough for
// the panel's round-trips (written from the research notes, not copied).
type FakeAdmin struct {
	ln net.Listener

	mu   sync.Mutex
	cmds []string
	// hbbs
	relays         []string
	alwaysUseRelay bool
	mustLogin      bool
	tcpKeepalive   bool
	webrtc         bool
	// LegacyAURBug reproduces the upstream bug where "aur Y|N" re-parses the
	// relay list from "Y"/"N" (which fails DNS and empties it).
	LegacyAURBug bool
	// hbbr
	black, block map[string]bool
	rates        map[string]float64
	dt           float64
	t            int

	// Override, when set (via SetOverride), replaces the reply for a command line.
	Override func(cmd string) (string, bool)
	// Delay before replying.
	Delay atomic.Int64
	// Drop reads the command and closes without replying.
	Drop atomic.Bool
}

// NewFakeAdmin starts a fake admin channel on 127.0.0.1:0.
func NewFakeAdmin(tb testing.TB) *FakeAdmin {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	f := &FakeAdmin{
		ln:     ln,
		relays: []string{"relay.example.com"},
		black:  map[string]bool{}, block: map[string]bool{},
		rates: map[string]float64{"limit-speed": 32, "total-bandwidth": 1024, "single-bandwidth": 128},
		dt:    0.66, t: 1800,
	}
	go f.serve()
	tb.Cleanup(func() { ln.Close() })
	return f
}

// Addr is the listen address.
func (f *FakeAdmin) Addr() string { return f.ln.Addr().String() }

// SetOverride installs (or with nil removes) a reply override.
func (f *FakeAdmin) SetOverride(fn func(cmd string) (string, bool)) {
	f.mu.Lock()
	f.Override = fn
	f.mu.Unlock()
}

// SetLegacyAURBug toggles the upstream "aur wipes relay list" behaviour.
func (f *FakeAdmin) SetLegacyAURBug(on bool) {
	f.mu.Lock()
	f.LegacyAURBug = on
	f.mu.Unlock()
}

// Close stops accepting connections (simulates the process being down).
func (f *FakeAdmin) Close() { _ = f.ln.Close() }

// State returns a copy of selected state for assertions.
func (f *FakeAdmin) State() (relays []string, aur, ml bool, black, block []string, rates map[string]float64, dt float64, t int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := map[string]float64{}
	for k, v := range f.rates {
		r[k] = v
	}
	return append([]string(nil), f.relays...), f.alwaysUseRelay, f.mustLogin, setKeys(f.black), setKeys(f.block), r, f.dt, f.t
}

// TCPKeepalive reports the fake's TCP_KEEPALIVE flag.
func (f *FakeAdmin) TCPKeepalive() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tcpKeepalive
}

// WebRTC reports the fake's WEBRTC flag.
func (f *FakeAdmin) WebRTC() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.webrtc
}

// Restart simulates a process restart (state back to "CLI defaults").
func (f *FakeAdmin) Restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.relays = []string{"relay.example.com"}
	f.alwaysUseRelay, f.mustLogin, f.tcpKeepalive, f.webrtc = false, false, false, false
	f.black, f.block = map[string]bool{}, map[string]bool{}
	f.rates = map[string]float64{"limit-speed": 32, "total-bandwidth": 1024, "single-bandwidth": 128}
	f.dt, f.t = 0.66, 1800
}

// Commands returns the raw command lines received so far.
func (f *FakeAdmin) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

// ResetCommands forgets the received commands.
func (f *FakeAdmin) ResetCommands() {
	f.mu.Lock()
	f.cmds = nil
	f.mu.Unlock()
}

func (f *FakeAdmin) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(c)
	}
}

func (f *FakeAdmin) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil {
		return
	}
	if f.Drop.Load() {
		return
	}
	cmd := string(buf[:n])
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd)
	f.mu.Unlock()
	if d := f.Delay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	f.mu.Lock()
	ov := f.Override
	f.mu.Unlock()
	var res string
	if ov != nil {
		if r, ok := ov(cmd); ok {
			res = r
		} else {
			res = f.check(cmd)
		}
	} else {
		res = f.check(cmd)
	}
	_, _ = c.Write([]byte(res))
}

func setKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fmtMb(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + "Mb/s\n" }

func (f *FakeAdmin) check(cmd string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	fds := strings.Split(strings.TrimSpace(cmd), " ")
	arg := func(i int) (string, bool) {
		if i < len(fds) {
			return fds[i], true
		}
		return "", false
	}
	var b strings.Builder
	switch fds[0] {
	case "h":
		return "help line 1\nhelp line 2\n"
	case "relay-servers", "rs":
		if a, ok := arg(1); ok {
			f.relays = nil
			for _, s := range strings.Split(a, ",") {
				if s != "" && s != "Y" && s != "N" && !strings.Contains(s, "invalid") {
					f.relays = append(f.relays, s)
				}
			}
			return ""
		}
		for _, r := range f.relays {
			b.WriteString(r + "\n")
		}
	case "always-use-relay", "aur":
		if a, ok := arg(1); ok {
			f.alwaysUseRelay = strings.ToUpper(a) == "Y"
			if f.LegacyAURBug {
				f.relays = nil
			}
			return ""
		}
		fmt.Fprintf(&b, "ALWAYS_USE_RELAY: %v\n", f.alwaysUseRelay)
	case "must-login", "ml":
		if a, ok := arg(1); ok {
			f.mustLogin = strings.ToUpper(a) == "Y"
			return ""
		}
		fmt.Fprintf(&b, "MUST_LOGIN: %v\n", f.mustLogin)
	case "tcp-keepalive", "tk":
		if a, ok := arg(1); ok {
			f.tcpKeepalive = strings.ToUpper(a) == "Y"
			return ""
		}
		fmt.Fprintf(&b, "TCP_KEEPALIVE: %v\n", f.tcpKeepalive)
	case "webrtc", "wr":
		if a, ok := arg(1); ok {
			f.webrtc = strings.ToUpper(a) == "Y"
			return ""
		}
		fmt.Fprintf(&b, "WEBRTC: %v\n", f.webrtc)
	case "ip-blocker", "ib":
		b.WriteString("2\n")
		a, ok := arg(1)
		if _, err := strconv.Atoi(a); !ok || err == nil {
			b.WriteString("1.2.3.4: 31/12s 2/3600s\n::ffff:127.0.0.1: 1/0s 1/0s\n")
		} else if a == "1.2.3.4" {
			b.WriteString("31/12s 2/3600s\n")
		}
	case "ip-changes", "ic":
		b.WriteString("1\n")
		a, ok := arg(1)
		if n, err := strconv.Atoi(a); !ok || (err == nil && n >= 0 && n <= 10_000_000) {
			b.WriteString(`123456789012: 120s {"10.0.0.1": 3, "10.0.0.2": 1}` + "\n")
		} else if a == "123456789012" {
			b.WriteString(`120s {"10.0.0.1": 3, "10.0.0.2": 1}` + "\n")
		}
	case "punch-requests", "pr":
		if a, _ := arg(1); a == "-" {
			return ""
		}
		b.WriteString("2026-10-15T12:00:00Z 1.2.3.4 -> 123456789@5.6.7.8\n2026-10-15T12:00:05Z ::1 -> abcdef@::2\n")
	case "test-geo", "tg":
		if _, ok := arg(1); ok {
			return `"relay.example.com"`
		}
	case "blacklist-add", "ba", "blocklist-add", "Ba":
		m := f.black
		if fds[0] == "blocklist-add" || fds[0] == "Ba" {
			m = f.block
		}
		if a, ok := arg(1); ok {
			for _, ip := range strings.Split(a, "|") {
				m[ip] = true
			}
		}
	case "blacklist-remove", "br", "blocklist-remove", "Br":
		m := f.black
		if fds[0] == "blocklist-remove" || fds[0] == "Br" {
			m = f.block
		}
		if a, ok := arg(1); ok {
			if a == "all" {
				for k := range m {
					delete(m, k)
				}
			} else {
				for _, ip := range strings.Split(a, "|") {
					delete(m, ip)
				}
			}
		}
	case "blacklist", "b", "blocklist", "B":
		m := f.black
		if fds[0] == "blocklist" || fds[0] == "B" {
			m = f.block
		}
		if a, ok := arg(1); ok {
			fmt.Fprintf(&b, "%v\n", m[a])
		} else {
			for _, k := range setKeys(m) {
				b.WriteString(k + "\n")
			}
		}
	case "downgrade-threshold", "dt":
		if a, ok := arg(1); ok {
			if v, err := strconv.ParseFloat(a, 64); err == nil && v > 0 {
				f.dt = float64(int(v*100)) / 100
			}
			return ""
		}
		b.WriteString(strconv.FormatFloat(f.dt, 'f', -1, 64) + "\n")
	case "downgrade-start-check", "t":
		if a, ok := arg(1); ok {
			if v, err := strconv.Atoi(a); err == nil && v > 0 {
				f.t = v
			}
			return ""
		}
		fmt.Fprintf(&b, "%ds\n", f.t)
	case "limit-speed", "ls", "total-bandwidth", "tb", "single-bandwidth", "sb":
		name := map[string]string{"ls": "limit-speed", "tb": "total-bandwidth", "sb": "single-bandwidth"}[fds[0]]
		if name == "" {
			name = fds[0]
		}
		if a, ok := arg(1); ok {
			if v, err := strconv.ParseFloat(a, 64); err == nil && v > 0 {
				f.rates[name] = v
			}
			return ""
		}
		b.WriteString(fmtMb(f.rates[name]))
	case "usage", "u":
		b.WriteString("1.2.3.4:5555: 120s 12.50MB 900kb/s 850kb/s 800kb/s\n[::1]:6000: 10s 0.00MB 0kb/s 0kb/s 0kb/s\n")
	case "reload-geo", "rg":
		return "loaded 2 prefix rules\n"
	case "stats":
		return "bytes_total=104857600\nrelays_total=42\nrelays_active=3\nwaiting=1\nauth_failed=2\nblocked=5\ndowngraded=0\nuptime_s=3600\nversion=1.1.16\n"
	}
	return b.String()
}
