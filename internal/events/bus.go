package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Type names an event kind. It is used as the SSE "event:" field.
type Type string

// Event kinds published by the panel.
const (
	// PresenceChanged: a device went online or offline (data: Presence).
	PresenceChanged Type = "presence.changed"
	// DeviceUpdated: a device row changed (data: DeviceUpdate).
	DeviceUpdated Type = "device.updated"
	// AuditConnection: a connection audit row was recorded (data: free-form object).
	AuditConnection Type = "audit.connection"
	// HBBSHealth: reachability of hbbs/hbbr changed (data: Health).
	HBBSHealth Type = "hbbs.health"
	// RelayHealth: a relay's health, agent connection or config state
	// changed (data: RelayState). Admins only.
	RelayHealth Type = "relay.health"
)

// Audience restricts who may receive an event.
type Audience struct {
	// AdminOnly events are delivered to admins only.
	AdminOnly bool
	// UserIDs, when non-empty, limits delivery of a non-admin event to these
	// users (admins always receive it). Empty means every authenticated user.
	UserIDs []int64
}

// Admins is the audience of admin-only events.
var Admins = Audience{AdminOnly: true}

// Event is one published message.
type Event struct {
	// ID is a process-local, monotonically increasing sequence number.
	ID       uint64    `json:"id"`
	Type     Type      `json:"type"`
	Time     time.Time `json:"time"`
	Data     any       `json:"data"`
	Audience Audience  `json:"-"`
}

// VisibleTo reports whether p may receive e.
func (e Event) VisibleTo(p Principal) bool {
	if p.IsAdmin() {
		return true
	}
	if e.Audience.AdminOnly {
		return false
	}
	if len(e.Audience.UserIDs) == 0 {
		return true
	}
	for _, id := range e.Audience.UserIDs {
		if id == p.UserID {
			return true
		}
	}
	return false
}

// Presence is the payload of PresenceChanged.
type Presence struct {
	ID     string `json:"id"`
	Online bool   `json:"online"`
	// Since is when the device entered its current state (as observed by the panel).
	Since time.Time `json:"since"`
	// LastSeen is the last time the device was observed online (zero if never).
	LastSeen time.Time `json:"last_seen,omitempty"`
	// Source is "hbbs" (OnlineRequest) or "heartbeat" (fallback while hbbs is down).
	Source string `json:"source"`
}

// DeviceUpdate is the payload of DeviceUpdated.
type DeviceUpdate struct {
	ID     string   `json:"id"`
	Fields []string `json:"fields,omitempty"`
}

// Health is the payload of HBBSHealth.
type Health struct {
	// Component: "hbbs" (rendezvous port, presence poller), "hbbs-admin" or "hbbr-admin".
	Component string `json:"component"`
	Up        bool   `json:"up"`
	// RTTMillis is the round-trip time of the probe that observed the state.
	RTTMillis float64 `json:"rtt_ms,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// RelayState is the payload of RelayHealth (internal/relays).
type RelayState struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Health    string `json:"health"`
	Previous  string `json:"previous,omitempty"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// DefaultBuffer is the per-subscriber queue length.
const DefaultBuffer = 256

// Bus is an in-process fan-out pub/sub. Publish never blocks: each subscriber
// has a bounded queue and, when it is full, the oldest queued event is dropped
// and counted.
type Bus struct {
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
	seq  atomic.Uint64
	now  func() time.Time

	published *prometheus.CounterVec
	dropped   prometheus.Counter
	nsubs     prometheus.Gauge
}

// NewBus creates a bus; reg may be nil.
func NewBus(reg prometheus.Registerer) *Bus {
	b := &Bus{
		subs: map[*Subscription]struct{}{},
		now:  time.Now,
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "events", Name: "published_total",
			Help: "Events published on the in-process bus, by type.",
		}, []string{"type"}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "events", Name: "dropped_total",
			Help: "Events dropped because a subscriber queue was full.",
		}),
		nsubs: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "rdpanel", Subsystem: "events", Name: "subscribers",
			Help: "Current event bus subscribers (SSE streams and internal consumers).",
		}),
	}
	if reg != nil {
		reg.MustRegister(b.published, b.dropped, b.nsubs)
	}
	return b
}

// Publish stamps and delivers an event to every matching subscriber.
// A nil *Bus is a valid no-op publisher.
func (b *Bus) Publish(t Type, data any, aud Audience) {
	if b == nil {
		return
	}
	e := Event{ID: b.seq.Add(1), Type: t, Time: b.now().UTC(), Data: data, Audience: aud}
	b.published.WithLabelValues(string(t)).Inc()
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if s.filter == nil || s.filter(e) {
			if s.push(e) {
				b.dropped.Inc()
			}
		}
	}
}

// Subscribe registers a subscriber. filter (may be nil) selects events;
// buffer <= 0 means DefaultBuffer. Call Close when done.
func (b *Bus) Subscribe(filter func(Event) bool, buffer int) *Subscription {
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	s := &Subscription{
		bus:    b,
		filter: filter,
		buf:    make([]Event, buffer),
		ready:  make(chan struct{}, 1),
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	b.nsubs.Inc()
	return s
}

// Subscribers returns the current number of subscribers.
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Subscription is one consumer's bounded queue (a ring buffer).
type Subscription struct {
	bus    *Bus
	filter func(Event) bool

	mu      sync.Mutex
	buf     []Event
	head, n int
	dropped uint64 // since the last Drain
	total   uint64 // over the subscription's lifetime
	closed  bool
	ready   chan struct{}
}

// push enqueues e; it reports whether an old event had to be dropped.
func (s *Subscription) push(e Event) (dropped bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if s.n == len(s.buf) {
		s.head = (s.head + 1) % len(s.buf)
		s.n--
		s.dropped++
		s.total++
		dropped = true
	}
	s.buf[(s.head+s.n)%len(s.buf)] = e
	s.n++
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
	return dropped
}

// Ready is signalled (level-triggered, coalesced) when events may be queued.
func (s *Subscription) Ready() <-chan struct{} { return s.ready }

// Drain appends all queued events to dst and returns it together with the
// number of events dropped since the previous Drain.
func (s *Subscription) Drain(dst []Event) ([]Event, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < s.n; i++ {
		j := (s.head + i) % len(s.buf)
		dst = append(dst, s.buf[j])
		s.buf[j] = Event{}
	}
	s.head, s.n = 0, 0
	d := s.dropped
	s.dropped = 0
	return dst, d
}

// Next blocks until an event is available or ctx ends.
func (s *Subscription) Next(ctx context.Context) (Event, error) {
	for {
		s.mu.Lock()
		if s.n > 0 {
			e := s.buf[s.head]
			s.buf[s.head] = Event{}
			s.head = (s.head + 1) % len(s.buf)
			s.n--
			s.mu.Unlock()
			return e, nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Event{}, ctx.Err()
		case <-s.ready:
		}
	}
}

// Dropped returns the total number of events dropped for this subscriber.
func (s *Subscription) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Close unregisters the subscription. It is idempotent.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	_, ok := s.bus.subs[s]
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if ok {
		s.bus.nsubs.Dec()
	}
}
