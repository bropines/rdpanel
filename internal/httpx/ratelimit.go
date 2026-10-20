package httpx

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter is an in-memory token bucket per key (usually the client IP):
// Rate requests per second with bursts of Burst. Idle keys are dropped after
// ten minutes. A Rate <= 0 allows everything.
type RateLimiter struct {
	rate      float64
	burst     float64
	now       func() time.Time
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	lastPrune time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

const bucketIdle = 10 * time.Minute

// NewRateLimiter returns a limiter of rate requests per second with bursts of
// burst (at least 1); now nil means time.Now.
func NewRateLimiter(rate float64, burst int, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{rate: rate, burst: float64(max(burst, 1)), now: now,
		buckets: map[string]*tokenBucket{}, lastPrune: now()}
}

// Allow takes a token for key and reports whether one was available.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil || l.rate <= 0 {
		return true
	}
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
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(l.burst, b.tokens+el*l.rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RateLimit returns middleware answering 429 (Retry-After: retryAfter
// seconds, body from reject) to clients over l's limit, keyed by ClientIP.
func RateLimit(l *RateLimiter, retryAfter string, reject http.HandlerFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(ClientIPString(r)) {
				w.Header().Set("Retry-After", retryAfter)
				reject(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
