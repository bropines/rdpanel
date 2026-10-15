// Package httpx contains HTTP middleware and helpers shared by every API
// surface: client IP resolution, request logging, panic recovery, metrics and
// JSON error responses.
package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type ctxKey int

const clientIPKey ctxKey = iota

// RealIP resolves the client IP and stores it in the request context
// (retrieve with ClientIP). Forwarding headers are honoured ONLY when the
// direct peer is inside trusted; otherwise the TCP peer address is used.
//
// header selects the source when the peer is trusted:
//   - "X-Forwarded-For": walk the list right-to-left, skipping trusted hops,
//     and take the first untrusted address (spoof-resistant);
//   - "X-Real-Ip" / "Cf-Connecting-Ip": take the single address.
func RealIP(trusted []netip.Prefix, header string) func(http.Handler) http.Handler {
	header = http.CanonicalHeaderKey(header)
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := peerAddr(r.RemoteAddr)
			if ip.IsValid() && isTrusted(ip) {
				if fwd, ok := fromHeader(r.Header, header, isTrusted); ok {
					ip = fwd
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey, ip)))
		})
	}
}

func fromHeader(h http.Header, name string, isTrusted func(netip.Addr) bool) (netip.Addr, bool) {
	values := h.Values(name)
	if len(values) == 0 {
		return netip.Addr{}, false
	}
	if name != "X-Forwarded-For" {
		a, err := parseAddr(values[len(values)-1])
		return a, err == nil
	}
	// Multiple XFF headers are equivalent to one comma-joined list.
	var hops []string
	for _, v := range values {
		hops = append(hops, strings.Split(v, ",")...)
	}
	var last netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := parseAddr(hops[i])
		if err != nil {
			// Garbage in the chain: stop at the last good hop we trust.
			break
		}
		last = a
		if !isTrusted(a) {
			return a, true
		}
	}
	// Every hop was trusted: use the left-most valid one.
	return last, last.IsValid()
}

func parseAddr(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), nil
	}
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}, err
	}
	return a.Unmap(), nil
}

func peerAddr(remote string) netip.Addr {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// ClientIP returns the client IP resolved by RealIP (invalid if the
// middleware did not run and RemoteAddr is unparsable).
func ClientIP(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(clientIPKey).(netip.Addr); ok {
		return a
	}
	return peerAddr(r.RemoteAddr)
}

// ClientIPString is ClientIP formatted for logs/DB ("" when unknown).
func ClientIPString(r *http.Request) string {
	a := ClientIP(r)
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// AllowIPs returns middleware rejecting (403) clients whose resolved IP is
// outside allowed. Used for /metrics.
func AllowIPs(allowed []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r)
			for _, p := range allowed {
				if ip.IsValid() && p.Contains(ip) {
					next.ServeHTTP(w, r)
					return
				}
			}
			WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
		})
	}
}
