// Package websessions remembers which panel user a browser (web client)
// session belongs to. The web client announces a per-session id as its
// RustDesk "my_id"; the controlled device reports that id back in its audit
// events, which lets connection rules apply to the panel user.
package websessions

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// Prefix marks web client ids; desktop clients have numeric ids.
const Prefix = "web-"

const (
	// TTL bounds how long an issued id maps to its user.
	TTL = 12 * time.Hour
	// maxEntries caps memory if the config endpoint is hammered.
	maxEntries = 20000
)

type entry struct {
	userID int64
	exp    time.Time
}

// Registry is safe for concurrent use.
type Registry struct {
	mu  sync.Mutex
	m   map[string]entry
	now func() time.Time
}

// New returns an empty registry; now may be nil (time.Now).
func New(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{m: make(map[string]entry), now: now}
}

// Issue returns a fresh id for userID.
func (r *Registry) Issue(userID int64) (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1e10))
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s%010d", Prefix, n.Int64())
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.m) >= maxEntries {
		r.pruneLocked(now)
		if len(r.m) >= maxEntries {
			// drop an arbitrary entry; ids are only an ACL hint
			for k := range r.m {
				delete(r.m, k)
				break
			}
		}
	}
	r.m[id] = entry{userID: userID, exp: now.Add(TTL)}
	return id, nil
}

// User returns the panel user behind a web client id.
func (r *Registry) User(id string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[id]
	if !ok {
		return 0, false
	}
	if r.now().After(e.exp) {
		delete(r.m, id)
		return 0, false
	}
	return e.userID, true
}

func (r *Registry) pruneLocked(now time.Time) {
	for k, e := range r.m {
		if now.After(e.exp) {
			delete(r.m, k)
		}
	}
}
