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

// forwardingHeaders are the headers proxies use to pass the client address.
var forwardingHeaders = []string{"X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip", "True-Client-Ip", "Forwarded"}

// forwardedAddrs returns every address named by forwarding headers and
// whether all of them could be parsed.
func forwardedAddrs(h http.Header) ([]netip.Addr, bool) {
	var out []netip.Addr
	ok := true
	for _, name := range forwardingHeaders {
		for _, v := range h.Values(name) {
			for _, part := range strings.Split(v, ",") {
				if name == "Forwarded" {
					// for=192.0.2.1;proto=https, for="[2001:db8::1]:443"
					for _, kv := range strings.Split(part, ";") {
						k, val, has := strings.Cut(strings.TrimSpace(kv), "=")
						if !has || !strings.EqualFold(k, "for") {
							continue
						}
						a, err := parseAddr(strings.Trim(val, `"`))
						if err != nil {
							ok = false // obfuscated identifiers ("_hidden", "unknown") are not local
							continue
						}
						out = append(out, a)
					}
					continue
				}
				a, err := parseAddr(part)
				if err != nil {
					ok = false
					continue
				}
				out = append(out, a)
			}
		}
	}
	return out, ok
}

// AllowIPs returns middleware rejecting (403) clients whose resolved IP is
// outside allowed. Used for /metrics. Defence in depth for deployments behind
// a loopback reverse proxy: every address in any forwarding header
// (X-Forwarded-For, X-Real-IP, CF-Connecting-IP, True-Client-IP, Forwarded)
// must also be allowed, so a proxied request is never mistaken for a local
// scraper even when the proxy uses a different header than real_ip_header.
func AllowIPs(allowed []netip.Prefix) func(http.Handler) http.Handler {
	in := func(ip netip.Addr) bool {
		for _, p := range allowed {
			if ip.IsValid() && p.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok := in(ClientIP(r))
			if ok {
				fwd, parsed := forwardedAddrs(r.Header)
				ok = parsed
				for _, a := range fwd {
					ok = ok && in(a)
				}
			}
			if !ok {
				WriteError(w, http.StatusForbidden, "forbidden", "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
