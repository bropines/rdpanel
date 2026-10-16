package adminapi

// Stream B2b: address books, audit and server (hbbs/hbbr) endpoints.
// Shared plumbing lives here; handlers are in b2b_books.go, b2b_audit.go and
// b2b_server.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/clientopts"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/presence"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// PresenceSource is the live device presence (implemented by *presence.Poller).
type PresenceSource interface {
	IsOnline(rustdeskID string) bool
	Health() presence.Health
}

// ServerControl is the hbbs/hbbr admin channel (implemented by *control.Client).
type ServerControl interface {
	Addr(t control.Target) string
	ExecLine(ctx context.Context, t control.Target, line string) (string, error)
	IPBlocker(ctx context.Context, start int) (control.IPBlockerPage, error)
	IPBlockerLookup(ctx context.Context, ip string, remove bool) (*control.IPBlockerEntry, error)
	IPChanges(ctx context.Context, start int) (control.IPChangesPage, error)
	IPChangesLookup(ctx context.Context, id string, remove bool) (*control.IPChange, error)
	Usage(ctx context.Context) ([]control.RelayUsage, error)
}

// SettingsReconciler owns the desired hbbs/hbbr settings (implemented by
// *reconcile.Reconciler).
type SettingsReconciler interface {
	Status() reconcile.Status
	Desired(ctx context.Context) (reconcile.Settings, error)
	KeyStatus(ctx context.Context) ([]reconcile.KeyStatus, error)
	Current(ctx context.Context) (reconcile.Observed, error)
	Apply(ctx context.Context, s reconcile.Settings, updatedBy *int64) error
	Reconcile(ctx context.Context) error
}

// b2bCore bundles the B2b handlers' collaborators.
type b2bCore struct {
	d       Deps
	log     *slog.Logger
	console *windowLimiter
}

// registerB2b mounts the address-book, audit and server routes.
func registerB2b(r chi.Router, d Deps) {
	limit := d.ConsoleLimit
	if limit <= 0 {
		limit = 30
	}
	if d.ClientOptions == nil {
		d.ClientOptions = clientopts.New(d.Store, d.AEAD, d.Store.Now)
	}
	c := &b2bCore{d: d, log: d.Log, console: newWindowLimiter(limit, time.Minute, d.Store.Now)}
	if c.log == nil {
		c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	c.registerBooks(r)
	c.registerAudit(r)
	c.registerServer(r)
	c.registerRelays(r)
	c.registerAbout(r)
}

// recordAudit appends an admin_audit row for the request's user. details must
// never contain secrets.
func recordAudit(ctx context.Context, d Deps, r *http.Request, action, targetType, targetID string, details map[string]any) {
	e := &store.AuditEntry{Action: action, TargetType: targetType, TargetID: targetID, Details: details,
		ActorIP: httpx.ClientIPString(r)}
	if s := CurrentSession(ctx); s != nil {
		e.ActorUserID, e.ActorName = &s.User.ID, s.User.Username
	}
	if err := d.Store.AdminAudit.Record(ctx, e); err != nil && d.Log != nil {
		d.Log.WarnContext(ctx, "admin audit", "action", action, "err", err)
	}
}

func (c *b2bCore) audit(r *http.Request, action, targetType string, targetID int64, details map[string]any) {
	recordAudit(r.Context(), c.d, r, action, targetType, strconv.FormatInt(targetID, 10), details)
}

func (c *b2bCore) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "conflict", "already exists")
	default:
		c.log.ErrorContext(r.Context(), what, slog.Any("err", err))
		internalError(w)
	}
}

// decodeBody is decodeJSON with a custom size limit.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body")
		return false
	}
	return true
}

// optString distinguishes an absent field (Set false) from null (Val nil).
type optString struct {
	Set bool
	Val *string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Val = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Val = &s
	return nil
}

// ---- list parameters ----

// b2bPaging is the parsed page / page_size of a list request.
type b2bPaging struct{ Page, Size int }

func (p b2bPaging) offset() int { return (p.Page - 1) * p.Size }

// pagedList is the {"items","total","page","page_size"} envelope.
type pagedList[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

func newPagedList[T any](items []T, total int, p b2bPaging) pagedList[T] {
	if items == nil {
		items = []T{}
	}
	return pagedList[T]{Items: items, Total: total, Page: p.Page, PageSize: p.Size}
}

// listQuery parses and validates common list query parameters. Problems are
// collected; check ok() and write the error once.
type listQuery struct {
	v   map[string][]string
	err string
}

func newListQuery(r *http.Request) *listQuery { return &listQuery{v: r.URL.Query()} }

func (q *listQuery) fail(msg string) {
	if q.err == "" {
		q.err = msg
	}
}

// ok writes a 400 when a parameter was invalid.
func (q *listQuery) ok(w http.ResponseWriter) bool {
	if q.err != "" {
		badRequest(w, q.err)
		return false
	}
	return true
}

func (q *listQuery) str(name string) string { return strings.TrimSpace(firstVal(q.v[name])) }

func firstVal(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (q *listQuery) b2bPaging() b2bPaging {
	p := b2bPaging{Page: 1, Size: 50}
	if s := q.str("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1_000_000 {
			q.fail("page must be an integer >= 1")
		} else {
			p.Page = n
		}
	}
	if s := q.str("page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			q.fail("page_size must be an integer between 1 and 500")
		} else {
			p.Size = n
		}
	}
	return p
}

func (q *listQuery) search() string {
	s := q.str("q")
	if utf8.RuneCountInString(s) > 200 {
		q.fail("q is too long")
		return ""
	}
	return s
}

// sort returns the sort value when it is one of allowed (def when absent).
func (q *listQuery) sort(def string, allowed ...string) string {
	s := q.str("sort")
	if s == "" {
		return def
	}
	for _, a := range allowed {
		if s == a || s == "-"+a {
			return s
		}
	}
	q.fail("unsupported sort " + strconv.Quote(s))
	return def
}

func (q *listQuery) int64Ptr(name string) *int64 {
	s := q.str(name)
	if s == "" {
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		q.fail(name + " must be a non-negative integer")
		return nil
	}
	return &n
}

func (q *listQuery) intPtr(name string, minV, maxV int) *int {
	s := q.str(name)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minV || n > maxV {
		q.fail(name + " is out of range")
		return nil
	}
	return &n
}

func (q *listQuery) boolPtr(name string) *bool {
	s := q.str(name)
	if s == "" {
		return nil
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		q.fail(name + " must be true or false")
		return nil
	}
	return &b
}

func (q *listQuery) enum(name string, allowed ...string) string {
	s := q.str(name)
	if s == "" {
		return ""
	}
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	q.fail(name + " must be one of " + strings.Join(allowed, ", "))
	return ""
}

func (q *listQuery) time(name string) *time.Time {
	s := q.str(name)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		q.fail(name + " must be an RFC 3339 timestamp")
		return nil
	}
	t = t.UTC()
	return &t
}

// auditQuery parses q, from, to, page, page_size and the created_at-style sort.
func (q *listQuery) auditQuery(p b2bPaging, desc bool) store.AuditQuery {
	return store.AuditQuery{Q: q.search(), Since: q.time("from"), Until: q.time("to"), Desc: desc,
		Limit: p.Size, Offset: p.offset()}
}

// ---- console rate limit ----

// windowLimiter allows at most n events per key within a sliding window.
type windowLimiter struct {
	n      int
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	hits   map[int64][]time.Time
}

func newWindowLimiter(n int, window time.Duration, now func() time.Time) *windowLimiter {
	if now == nil {
		now = time.Now
	}
	return &windowLimiter{n: n, window: window, now: now, hits: map[int64][]time.Time{}}
}

// allow records an event for key, or reports how long to wait.
func (l *windowLimiter) allow(key int64) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.n {
		l.hits[key] = keep
		return false, l.window - now.Sub(keep[0])
	}
	l.hits[key] = append(keep, now)
	return true, 0
}
