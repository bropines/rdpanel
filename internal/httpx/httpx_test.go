package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}

func resolve(t *testing.T, trusted []netip.Prefix, header, remote string, hdrs map[string][]string) string {
	t.Helper()
	var got string
	h := RealIP(trusted, header)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = ClientIPString(r)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	for k, vs := range hdrs {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestRealIP(t *testing.T) {
	trusted := append([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, loopback...)
	xff := "X-Forwarded-For"
	cases := []struct {
		name, header, remote string
		hdrs                 map[string][]string
		want                 string
	}{
		{"untrusted peer ignores XFF", xff, "203.0.113.9:5000", map[string][]string{xff: {"1.1.1.1"}}, "203.0.113.9"},
		{"trusted peer uses XFF", xff, "127.0.0.1:5000", map[string][]string{xff: {"198.51.100.7"}}, "198.51.100.7"},
		{"spoofed left-most hop is skipped", xff, "127.0.0.1:1", map[string][]string{xff: {"6.6.6.6, 198.51.100.7, 10.0.0.2"}}, "198.51.100.7"},
		{"multiple XFF headers", xff, "127.0.0.1:1", map[string][]string{xff: {"6.6.6.6", "198.51.100.7"}}, "198.51.100.7"},
		{"all trusted -> left-most", xff, "127.0.0.1:1", map[string][]string{xff: {"10.0.0.5, 10.0.0.2"}}, "10.0.0.5"},
		{"garbage XFF -> peer", xff, "127.0.0.1:1", map[string][]string{xff: {"nonsense"}}, "127.0.0.1"},
		{"no header -> peer", xff, "[::1]:1", nil, "::1"},
		{"ipv4-mapped peer", xff, "[::ffff:127.0.0.1]:1", map[string][]string{xff: {"198.51.100.7"}}, "198.51.100.7"},
		{"XFF with port", xff, "127.0.0.1:1", map[string][]string{xff: {"[2001:db8::1]:443"}}, "2001:db8::1"},
		{"X-Real-IP from trusted", "X-Real-IP", "127.0.0.1:1", map[string][]string{"X-Real-Ip": {"198.51.100.8"}}, "198.51.100.8"},
		{"X-Real-IP from untrusted", "X-Real-IP", "203.0.113.1:1", map[string][]string{"X-Real-Ip": {"198.51.100.8"}}, "203.0.113.1"},
		{"CF header", "CF-Connecting-IP", "127.0.0.1:1", map[string][]string{"Cf-Connecting-Ip": {"2001:db8::2"}}, "2001:db8::2"},
		{"configured header only", "CF-Connecting-IP", "127.0.0.1:1", map[string][]string{xff: {"198.51.100.7"}}, "127.0.0.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolve(t, trusted, c.header, c.remote, c.hdrs); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestAllowIPs(t *testing.T) {
	h := RealIP(loopback, "X-Forwarded-For")(AllowIPs(loopback)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:9"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("loopback denied: %d", rec.Code)
	}
	// Proxied external client is denied even though the peer is loopback.
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("external allowed: %d", rec.Code)
	}
	// A proxy sending a different header than real_ip_header is still denied.
	for _, hv := range [][2]string{{"X-Real-Ip", "198.51.100.1"}, {"Forwarded", `for="198.51.100.1:443";proto=https`},
		{"Cf-Connecting-Ip", "garbage"}, {"X-Forwarded-For", "127.0.0.1, 198.51.100.1"}} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.RemoteAddr = "127.0.0.1:9"
		req.Header.Set(hv[0], hv[1])
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s allowed: %d", hv[0], hv[1], rec.Code)
		}
	}
	// A local scrape through the proxy (forwarded address is loopback) is fine.
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:9"
	req.Header.Set("X-Real-Ip", "127.0.0.1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("local proxied scrape denied: %d", rec.Code)
	}
}

func TestRecovererAndLogger(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	r := chi.NewRouter()
	r.Use(Logger(log), Recoverer(log))
	r.Get("/boom/{id}", func(http.ResponseWriter, *http.Request) { panic("kaboom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom/1?code=SECRETCODE", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "internal" {
		t.Fatalf("body %s", rec.Body.String())
	}
	logs := buf.String()
	if !strings.Contains(logs, "kaboom") || !strings.Contains(logs, `"route":"/boom/{id}"`) {
		t.Fatalf("logs: %s", logs)
	}
	if strings.Contains(logs, "SECRETCODE") {
		t.Fatal("query string must not be logged")
	}
}

func TestMetricsMiddleware(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	for _, p := range []string{"/items/1", "/items/2", "/nope"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET", "/items/{id}", "201")); got != 2 {
		t.Fatalf("items counter = %v", got)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET", "unmatched", "404")); got != 1 {
		t.Fatalf("unmatched counter = %v", got)
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, "bad_request", "nope")
	if rec.Code != 400 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"error":{"code":"bad_request","message":"nope"}}` {
		t.Fatalf("body %s", rec.Body.String())
	}
}
