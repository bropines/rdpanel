package auth

import (
	"context"
	"sync"
	"time"
)

// Limiter throttles repeated failures per key (e.g. "ip:1.2.3.4",
// "user:alice"). Implementations must be safe for concurrent use; the
// in-memory Backoff can later be swapped for a shared store.
type Limiter interface {
	// Allow reports whether an attempt for key may proceed now. When it may
	// not, retryAfter is how long the caller must wait.
	Allow(key string) (ok bool, retryAfter time.Duration)
	// Failure records a failed attempt, extending the backoff for key.
	Failure(key string)
	// Success clears the failure history for key.
	Success(key string)
}

// BackoffConfig parameterises Backoff.
type BackoffConfig struct {
	// Free is the number of failures tolerated before any delay applies.
	Free int
	// Base is the delay after the first failure beyond Free; it doubles with
	// every further failure.
	Base time.Duration
	// Max caps the delay.
	Max time.Duration
	// Forget drops a key's history after this long without failures.
	Forget time.Duration
}

// DefaultBackoffConfig: 3 free attempts, then 1s, 2s, 4s ... capped at 15 min.
var DefaultBackoffConfig = BackoffConfig{Free: 3, Base: time.Second, Max: 15 * time.Minute, Forget: time.Hour}

type backoffEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

// Backoff is an in-memory Limiter with per-key exponential backoff.
type Backoff struct {
	cfg     BackoffConfig
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*backoffEntry
}

var _ Limiter = (*Backoff)(nil)

// NewBackoff returns an in-memory exponential backoff limiter. now may be nil.
func NewBackoff(cfg BackoffConfig, now func() time.Time) *Backoff {
	if now == nil {
		now = time.Now
	}
	if cfg.Base <= 0 {
		cfg.Base = DefaultBackoffConfig.Base
	}
	if cfg.Max < cfg.Base {
		cfg.Max = cfg.Base
	}
	if cfg.Forget <= 0 {
		cfg.Forget = DefaultBackoffConfig.Forget
	}
	return &Backoff{cfg: cfg, now: now, entries: make(map[string]*backoffEntry)}
}

// Allow implements Limiter.
func (b *Backoff) Allow(key string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[key]
	if e == nil {
		return true, 0
	}
	now := b.now()
	if now.Sub(e.lastFailure) > b.cfg.Forget {
		delete(b.entries, key)
		return true, 0
	}
	if wait := e.lockedUntil.Sub(now); wait > 0 {
		return false, wait
	}
	return true, 0
}

// Failure implements Limiter.
func (b *Backoff) Failure(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	e := b.entries[key]
	if e == nil || now.Sub(e.lastFailure) > b.cfg.Forget {
		e = &backoffEntry{}
		b.entries[key] = e
	}
	e.failures++
	e.lastFailure = now
	if over := e.failures - b.cfg.Free; over > 0 {
		e.lockedUntil = now.Add(b.delay(over))
	}
}

// Success implements Limiter.
func (b *Backoff) Success(key string) {
	b.mu.Lock()
	delete(b.entries, key)
	b.mu.Unlock()
}

func (b *Backoff) delay(over int) time.Duration {
	d := b.cfg.Base
	for i := 1; i < over; i++ {
		d *= 2
		if d >= b.cfg.Max || d <= 0 {
			return b.cfg.Max
		}
	}
	return min(d, b.cfg.Max)
}

// Len returns the number of tracked keys.
func (b *Backoff) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// Prune removes keys whose history is older than Forget.
func (b *Backoff) Prune() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for k, e := range b.entries {
		if now.Sub(e.lastFailure) > b.cfg.Forget && !now.Before(e.lockedUntil) {
			delete(b.entries, k)
		}
	}
}

// RunPruner calls Prune every interval until ctx is done.
func (b *Backoff) RunPruner(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Prune()
		}
	}
}
