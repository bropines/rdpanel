// Package logs keeps recent log lines of hbbs, hbbr and rdpanel in memory for
// the panel's Server -> Logs page (GET /api/v1/admin/server/logs*).
//
// Lines get in two ways:
//
//   - In the s6 image every service's log pipeline (prefix-log) copies each
//     line to the container's stdout and to an s6-log directory
//     <RDPANEL_LOGS_DIR>/<source>/current (rotated, bounded); a Tailer per
//     source follows that file. rdpanel never sits in the services' output
//     path, so a slow or stopped panel cannot block hbbs/hbbr, and the files
//     refill the buffer when rdpanel restarts.
//   - Without a logs directory (plain `rdpanel serve`), only the panel's own
//     lines are captured, by teeing the slog output (Writer).
//
// Every line is redacted (Redactor) before it is parsed and stored. A Buffer
// holds one fixed-size ring per source; live subscribers get new entries over
// bounded queues that drop (and count) instead of blocking.
package logs

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Known sources. Lines from other producers are filed under SourceOther so
// metric labels stay bounded.
const (
	SourceHBBS  = "hbbs"
	SourceHBBR  = "hbbr"
	SourcePanel = "panel"
	SourceOther = "other"
)

// FileSources are the s6 services whose output the image writes to the logs
// directory (deploy/rootfs/usr/libexec/rdpanel/prefix-log).
var FileSources = []string{SourceHBBS, SourceHBBR, SourcePanel}

func normSource(s string) string {
	switch s {
	case SourceHBBS, SourceHBBR, SourcePanel:
		return s
	}
	return SourceOther
}

// Level is a normalised log level; higher is more severe.
type Level int8

// Levels (trace folds into debug; fatal/panic/critical into error).
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	}
	return "info"
}

// ParseLevel maps a level name (any case, common aliases) to a Level.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "debug", "dbug", "trce":
		return LevelDebug, true
	case "info", "inf", "information", "notice":
		return LevelInfo, true
	case "warn", "warning", "wrn":
		return LevelWarn, true
	case "error", "err", "fatal", "panic", "crit", "critical", "eror":
		return LevelError, true
	}
	return LevelInfo, false
}

// Entry is one stored log line. Raw and Message are already redacted.
type Entry struct {
	// Seq is a process-local, strictly increasing id across all sources.
	Seq     uint64
	Time    time.Time // zero when unknown
	Source  string
	Level   Level
	Message string
	Raw     string
}

// MaxLineBytes bounds a stored line; longer lines are cut.
const MaxLineBytes = 8 << 10

// MaxCapacity bounds the per-source capacity.
const MaxCapacity = 100_000

type ring struct {
	buf     []Entry
	head, n int
}

func (r *ring) push(e Entry) {
	if r.n == len(r.buf) {
		r.buf[r.head] = e
		r.head = (r.head + 1) % len(r.buf)
		return
	}
	r.buf[(r.head+r.n)%len(r.buf)] = e
	r.n++
}

// at returns the i-th entry, oldest first.
func (r *ring) at(i int) *Entry { return &r.buf[(r.head+i)%len(r.buf)] }

// Buffer is the in-memory store. A nil *Buffer, or one with capacity 0, is
// disabled: writes are dropped and queries return nothing.
type Buffer struct {
	capacity int
	now      func() time.Time

	mu      sync.RWMutex
	seq     uint64
	rings   map[string]*ring
	parsers map[string]*parser
	subs    map[*Subscription]struct{}
	red     *Redactor

	lines   *prometheus.CounterVec
	dropped *prometheus.CounterVec
}

// NewBuffer creates a buffer keeping up to capacity lines per source
// (0 disables it; values above MaxCapacity are clamped).
func NewBuffer(capacity int) *Buffer {
	if capacity < 0 {
		capacity = 0
	}
	if capacity > MaxCapacity {
		capacity = MaxCapacity
	}
	b := &Buffer{
		capacity: capacity,
		now:      time.Now,
		rings:    map[string]*ring{},
		parsers:  map[string]*parser{},
		subs:     map[*Subscription]struct{}{},
		red:      NewRedactor(),
		lines: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "logs", Name: "lines_total",
			Help: "Log lines captured for the Server -> Logs page, by source.",
		}, []string{"source"}),
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "logs", Name: "dropped_total",
			Help: "Log lines not delivered to a live log stream because its queue was full (slow consumer), by source.",
		}, []string{"source"}),
	}
	for _, s := range []string{SourceHBBS, SourceHBBR, SourcePanel, SourceOther} {
		b.lines.WithLabelValues(s)
		b.dropped.WithLabelValues(s)
	}
	return b
}

// Enabled reports whether the buffer stores anything.
func (b *Buffer) Enabled() bool { return b != nil && b.capacity > 0 }

// Capacity is the per-source line limit.
func (b *Buffer) Capacity() int {
	if b == nil {
		return 0
	}
	return b.capacity
}

// Register adds the buffer's metrics to reg (nil reg: no-op).
func (b *Buffer) Register(reg prometheus.Registerer) {
	if b == nil || reg == nil {
		return
	}
	reg.MustRegister(b.lines, b.dropped)
}

// AddSecrets makes the redactor replace these literal values (keys, passwords)
// in every later line. Short values (< 8 bytes) are ignored.
func (b *Buffer) AddSecrets(secrets ...string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.red = b.red.With(secrets...)
	b.mu.Unlock()
}

// Write ingests one raw line of source (without the trailing newline). It
// never blocks on consumers.
func (b *Buffer) Write(source, line string) {
	if !b.Enabled() {
		return
	}
	source = normSource(source)
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return // hbbs prints blank lines around multi-line SQL logs
	}
	if len(line) > MaxLineBytes {
		line = truncateUTF8(line, MaxLineBytes) + "…"
	}
	now := b.now()

	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.parsers[source]
	if p == nil {
		p = &parser{source: source}
		b.parsers[source] = p
	}
	e := p.parse(line, now)
	e.Raw = b.red.Redact(line)
	e.Message = b.red.Redact(e.Message)
	b.seq++
	e.Seq = b.seq
	e.Source = source
	r := b.rings[source]
	if r == nil {
		r = &ring{buf: make([]Entry, b.capacity)}
		b.rings[source] = r
	}
	r.push(e)
	b.lines.WithLabelValues(source).Inc()
	for s := range b.subs {
		if s.f.match(&e) && !s.push(e) {
			b.dropped.WithLabelValues(source).Inc()
		}
	}
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// Filter selects entries.
type Filter struct {
	// Sources limits the sources (empty: all).
	Sources []string
	// MinLevel drops entries below it.
	MinLevel Level
	// Q is a case-insensitive substring of the raw line.
	Q string
	// Before/After select entries with Seq < Before / Seq > After (0: no bound).
	Before, After uint64
	// Limit caps the result (newest entries win); 0 means no limit.
	Limit int

	q string // lower-cased Q
}

func (f *Filter) prepare() { f.q = strings.ToLower(f.Q) }

func (f *Filter) match(e *Entry) bool {
	if e.Level < f.MinLevel {
		return false
	}
	if len(f.Sources) > 0 {
		ok := false
		for _, s := range f.Sources {
			if s == e.Source {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if (f.Before > 0 && e.Seq >= f.Before) || e.Seq <= f.After {
		return false
	}
	return f.q == "" || strings.Contains(strings.ToLower(e.Raw), f.q)
}

// Page is a query result: Items oldest first; More reports that older
// matching entries exist (continue with Before = Items[0].Seq).
type Page struct {
	Items []Entry
	More  bool
}

// Query returns the newest entries matching f, oldest first.
func (b *Buffer) Query(f Filter) Page {
	if !b.Enabled() {
		return Page{}
	}
	f.prepare()
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []Entry
	more := false
	for _, r := range b.rings {
		taken := 0
		for i := r.n - 1; i >= 0; i-- {
			e := r.at(i)
			if !f.match(e) {
				continue
			}
			if f.Limit > 0 && taken == f.Limit {
				more = true
				break
			}
			out = append(out, *e)
			taken++
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq > out[j].Seq })
	if f.Limit > 0 && len(out) > f.Limit {
		out, more = out[:f.Limit], true
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return Page{Items: out, More: more}
}

// SourceInfo describes one source's ring.
type SourceInfo struct {
	Name  string
	Lines int
}

// Sources lists the sources that have stored lines plus the always-present
// ones in want, sorted by name.
func (b *Buffer) Sources(want ...string) []SourceInfo {
	if !b.Enabled() {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	seen := map[string]int{}
	for _, s := range want {
		seen[s] = 0
	}
	for s, r := range b.rings {
		seen[s] = r.n
	}
	out := make([]SourceInfo, 0, len(seen))
	for s, n := range seen {
		out = append(out, SourceInfo{Name: s, Lines: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DefaultSubscriptionBuffer is the per-subscriber queue length.
const DefaultSubscriptionBuffer = 1024

// Subscription receives entries written after Subscribe that match its filter
// (Before/Limit are ignored). Close it when done.
type Subscription struct {
	b       *Buffer
	f       Filter
	ch      chan Entry
	mu      sync.Mutex
	dropped uint64
}

// Subscribe registers a live consumer; buffer <= 0 means
// DefaultSubscriptionBuffer. It returns nil when the buffer is disabled.
func (b *Buffer) Subscribe(f Filter, buffer int) *Subscription {
	if !b.Enabled() {
		return nil
	}
	if buffer <= 0 {
		buffer = DefaultSubscriptionBuffer
	}
	f.prepare()
	f.Before, f.Limit = 0, 0
	s := &Subscription{b: b, f: f, ch: make(chan Entry, buffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// push is called with b.mu held; it reports false when the queue was full.
func (s *Subscription) push(e Entry) bool {
	select {
	case s.ch <- e:
		return true
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return false
	}
}

// C delivers the entries.
func (s *Subscription) C() <-chan Entry { return s.ch }

// TakeDropped returns and resets the number of entries dropped for this
// subscriber since the previous call.
func (s *Subscription) TakeDropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dropped
	s.dropped = 0
	return d
}

// Close unregisters the subscription; it is idempotent.
func (s *Subscription) Close() {
	if s == nil {
		return
	}
	s.b.mu.Lock()
	delete(s.b.subs, s)
	s.b.mu.Unlock()
}

// Subscribers returns the number of live subscriptions.
func (b *Buffer) Subscribers() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
