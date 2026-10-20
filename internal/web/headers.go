package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ContentSecurityPolicy is applied to every response. Scripts must come from
// our origin only; inline styles are allowed because Radix/sonner set them at
// runtime. Avatars may be loaded from IdPs over https. Workers may be spawned
// from self or blob: URLs (e.g. browser extensions, client background jobs).
// connect-src is completed per request (see SecurityHeaders): same-origin
// WebSockets for the web client, plus configured hbbs/hbbr WebSocket origins.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'%s; " +
	"worker-src 'self' blob:; " +
	"child-src 'self' blob:; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// WSOrigin returns the ws(s)://host[:port] origin of a WebSocket URL, or "".
func WSOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || !safeHost(u.Host) {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// safeHost keeps request-derived hosts from injecting CSP syntax.
func safeHost(h string) bool {
	if h == "" || len(h) > 255 {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c)) {
			return false
		}
	}
	return true
}

// SecurityHeaders sets defensive headers on every response. HSTS is only sent
// when the panel is served over https (public URL scheme). wsOrigins are
// extra WebSocket origins the web client may connect to.
func SecurityHeaders(hsts bool, wsOrigins ...string) func(http.Handler) http.Handler {
	var extra strings.Builder
	for _, o := range wsOrigins {
		if o = WSOrigin(o); o != "" {
			extra.WriteString(" " + o)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			// Same-origin WebSockets (/ws/id, /ws/relay): older Safari does
			// not match ws(s) with 'self', so name the origin explicitly.
			conn := extra.String()
			if safeHost(r.Host) {
				scheme := "ws://"
				if hsts || r.TLS != nil {
					scheme = "wss://"
				}
				conn = " " + scheme + r.Host + conn
			}
			h.Set("Content-Security-Policy", fmt.Sprintf(ContentSecurityPolicy, conn))
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
			if hsts {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
			next.ServeHTTP(w, r)
		})
	}
}
