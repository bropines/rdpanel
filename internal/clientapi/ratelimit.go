package clientapi

import (
	"net/http"
	"sync"
	"time"

	"github.com/bropines/rdpanel/internal/httpx"
)

// rateLimiter is an in-memory per-key token bucket for the unauthenticated
// device endpoints. (auth.Limiter is a failure backoff for credentials; using
// it here would let one stale device lock out every device behind its NAT.)
type rateLimiter struct {
	cfg       RateConfig
	now       func() time.Time
	mu        sync.Mutex
	buckets   map[string]*bucket
	lastPrune time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const bucketIdle = 10 * time.Minute

func newRateLimiter(cfg RateConfig, now func() time.Time) *rateLimiter {
	return &rateLimiter{cfg: cfg, now: now, buckets: map[string]*bucket{}, lastPrune: now()}
}

func (l *rateLimiter) allow(key string) bool {
	if l.cfg.Rate <= 0 {
		return true
	}
	burst := float64(max(l.cfg.Burst, 1))
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastPrune) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > bucketIdle {
				delete(l.buckets, k)
			}
		}
		l.lastPrune = now
	}
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(burst, b.tokens+el*l.cfg.Rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// deviceGate applies the per-IP rate limit to a device endpoint. Rejections
// are 429, which audit uploads treat as retryable.
func (h *handler) deviceGate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.devRate.allow(httpx.ClientIPString(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "Too many requests")
			return
		}
		next(w, r)
	}
}
