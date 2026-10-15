package clientapi

import (
	"slices"
	"sync"
	"time"
)

// KickQueue holds pending "disconnect" requests for devices. The admin API
// (stream B) calls Kick; the heartbeat handler calls Take and answers with
// {"disconnect": [...]}, which makes the client close those connections
// ("Closed manually by web console").
type KickQueue interface {
	// Kick asks device rustdeskID to close connections connIDs (the client's
	// conn ids, as reported in heartbeat "conns" and audit "conn_id"); with
	// no ids every live connection is closed. Requests expire if the device
	// does not heartbeat in time.
	Kick(rustdeskID string, connIDs ...int32)
	// Take removes and returns the connection ids to close, given the live
	// connection ids the device reported in its heartbeat.
	Take(rustdeskID string, live []int32) []int32
}

// DefaultKickTTL bounds how long a kick waits for the device's heartbeat.
const DefaultKickTTL = 2 * time.Minute

// MemKickQueue is an in-memory KickQueue (kicks do not survive a restart,
// which is fine: connection ids are only meaningful while connected).
type MemKickQueue struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	pending map[string]*kick
}

type kick struct {
	all     bool
	ids     []int32
	expires time.Time
}

var _ KickQueue = (*MemKickQueue)(nil)

// NewMemKickQueue returns an empty queue (ttl <= 0: DefaultKickTTL; now nil: time.Now).
func NewMemKickQueue(ttl time.Duration, now func() time.Time) *MemKickQueue {
	if ttl <= 0 {
		ttl = DefaultKickTTL
	}
	if now == nil {
		now = time.Now
	}
	return &MemKickQueue{ttl: ttl, now: now, pending: map[string]*kick{}}
}

// Kick implements KickQueue.
func (q *MemKickQueue) Kick(rustdeskID string, connIDs ...int32) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.prune(now)
	k := q.pending[rustdeskID]
	if k == nil {
		k = &kick{}
		q.pending[rustdeskID] = k
	}
	k.expires = now.Add(q.ttl)
	if len(connIDs) == 0 {
		k.all, k.ids = true, nil
		return
	}
	if !k.all {
		for _, id := range connIDs {
			if !slices.Contains(k.ids, id) {
				k.ids = append(k.ids, id)
			}
		}
	}
}

// Take implements KickQueue. A kick-all with no live connections stays
// pending (until it expires); specific ids that are not live are dropped.
func (q *MemKickQueue) Take(rustdeskID string, live []int32) []int32 {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.prune(now)
	k := q.pending[rustdeskID]
	if k == nil {
		return nil
	}
	var out []int32
	if k.all {
		if len(live) == 0 {
			return nil
		}
		out = slices.Clone(live)
	} else {
		for _, id := range k.ids {
			if slices.Contains(live, id) {
				out = append(out, id)
			}
		}
	}
	delete(q.pending, rustdeskID)
	return out
}

// Len returns the number of devices with pending kicks.
func (q *MemKickQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune(q.now())
	return len(q.pending)
}

func (q *MemKickQueue) prune(now time.Time) {
	for id, k := range q.pending {
		if now.After(k.expires) {
			delete(q.pending, id)
		}
	}
}
