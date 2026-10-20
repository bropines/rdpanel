package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	l := NewRateLimiter(1, 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if !l.Allow("a") {
			t.Fatalf("burst %d refused", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("over the burst")
	}
	if !l.Allow("b") {
		t.Fatal("keys are independent")
	}
	now = now.Add(1500 * time.Millisecond)
	if !l.Allow("a") || l.Allow("a") {
		t.Fatal("refill at 1/s")
	}
	// Idle keys are pruned.
	now = now.Add(time.Hour)
	l.Allow("c")
	if _, ok := l.buckets["a"]; ok {
		t.Fatal("idle bucket kept")
	}
	if !NewRateLimiter(0, 0, nil).Allow("x") {
		t.Fatal("rate 0 allows all")
	}
	var nilLimiter *RateLimiter
	if !nilLimiter.Allow("x") {
		t.Fatal("nil limiter allows all")
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	l := NewRateLimiter(0.001, 1, nil)
	h := RealIP(loopback, "X-Forwarded-For")(RateLimit(l, "60", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusTooManyRequests, "rate_limited", "slow down")
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	do := func(ip string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:1"
		req.Header.Set("X-Forwarded-For", ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("203.0.113.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := do("203.0.113.1"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("second: %d %v", rec.Code, rec.Header())
	}
	if rec := do("203.0.113.2"); rec.Code != http.StatusNoContent {
		t.Fatalf("other ip: %d", rec.Code)
	}
}

func TestRedactPath(t *testing.T) {
	for in, want := range map[string]string{
		"/s/AbCdEfGhIjKlMnOpQrStUv":             "/s/[token]",
		"/s/AbCdEfGhIjKlMnOpQrStUv/info":        "/s/[token]/info",
		"/s/AbCdEfGhIjKlMnOpQrStUv/windows/x64": "/s/[token]/windows/x64",
		"/s/assets/share-abc.js":                "/s/assets/share-abc.js",
		"/s/favicon.svg":                        "/s/favicon.svg",
		"/s/":                                   "/s/",
		"/api/v1/admin/share-links":             "/api/v1/admin/share-links",
		"/settings":                             "/settings",
	} {
		if got := RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q, want %q", in, got, want)
		}
	}
}
