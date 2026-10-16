package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// PresenceHealth is the presence poller's view of hbbs (rendezvous port).
type PresenceHealth struct {
	// Polls counts poll cycles since start (0 = no observation yet).
	Polls     uint64
	Reachable bool
	// Since is when Reachable last changed.
	Since time.Time
	// LastOK is the last successful poll.
	LastOK    time.Time
	RTT       time.Duration
	LastError string
}

// PresenceReader exposes live device presence (implemented over
// hbbs/presence.Poller in internal/app).
type PresenceReader interface {
	// OnlineIDs returns the RustDesk IDs currently online.
	OnlineIDs() []string
	// DevicePresence returns the state of one device; known is false when the
	// poller has not observed it yet.
	DevicePresence(rustdeskID string) (online bool, since time.Time, known bool)
	PresenceHealth() PresenceHealth
}

// ChannelStatus is the state of one hbbs/hbbr loopback admin channel.
type ChannelStatus struct {
	// Known is false until the channel was probed once.
	Known     bool
	Reachable bool
	Since     time.Time
	RTT       time.Duration
	LastError string
}

// ServerStatusReader exposes the reconciler's view of hbbs/hbbr.
type ServerStatusReader interface {
	ChannelStatus() (hbbs, hbbr ChannelStatus)
	// SettingsInSync reports whether every desired setting was applied.
	SettingsInSync(ctx context.Context) (bool, error)
}

// DeviceKicker queues a heartbeat "disconnect" for a device (clientapi.KickQueue).
type DeviceKicker interface {
	Kick(rustdeskID string, connIDs ...int32)
}

// EventPublisher publishes live events (*events.Bus).
type EventPublisher interface {
	Publish(t events.Type, data any, aud events.Audience)
}

// heartbeatOnlineWindow is the presence fallback without hbbs (SPEC §5).
const heartbeatOnlineWindow = 45 * time.Second

// resources implements the resource endpoints (dashboard, devices, users,
// groups, settings, lookups).
type resources struct {
	*authCore
}

// registerResources mounts the resource routes (stream B2a).
func registerResources(r chi.Router, c *authCore) {
	a := &resources{authCore: c}
	// Public branding (login page).
	r.Get("/branding", a.handleBranding)
	r.Get("/branding/logo", a.handleBrandingLogo)

	r.Group(func(r chi.Router) {
		r.Use(RequireUser)
		r.Get("/dashboard", a.handleDashboard)
		r.Get("/lookup/users", a.handleLookupUsers)
		r.Get("/lookup/user-groups", a.handleLookupUserGroups)
		r.Get("/devices", a.handleListDevices)
		r.Get("/devices/facets", a.handleDeviceFacets)
		r.Get("/devices/export.csv", a.handleExportDevices)
		r.Get("/devices/{device_id}", a.handleGetDevice)
		r.Patch("/devices/{device_id}", a.handleUpdateDevice)
	})
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Post("/devices/bulk", a.handleBulkDevices)
		r.Delete("/devices/{device_id}", a.handleDeleteDevice)
		r.Post("/devices/{device_id}/kick", a.handleKickDevice)

		r.Get("/users", a.handleListUsers)
		r.Post("/users", a.handleCreateUser)
		r.Get("/users/{user_id}", a.handleGetUser)
		r.Patch("/users/{user_id}", a.handleUpdateUser)
		r.Delete("/users/{user_id}", a.handleDeleteUser)
		r.Post("/users/{user_id}/password", a.handleResetPassword)
		r.Post("/users/{user_id}/totp/reset", a.handleResetTOTP)
		r.Get("/users/{user_id}/sessions", a.handleListUserSessions)
		r.Post("/users/{user_id}/sessions/revoke", a.handleRevokeUserSessions)
		r.Delete("/users/{user_id}/sessions/{session_id}", a.handleRevokeUserSession)
		r.Delete("/users/{user_id}/identities/{identity_id}", a.handleUnlinkUserIdentity)
		r.Get("/users/{user_id}/passkeys", a.handleListUserPasskeys)
		r.Delete("/users/{user_id}/passkeys/{passkey_id}", a.handleDeleteUserPasskey)

		r.Get("/user-groups", a.handleListUserGroups)
		r.Post("/user-groups", a.handleCreateUserGroup)
		r.Patch("/user-groups/{group_id}", a.handleUpdateUserGroup)
		r.Delete("/user-groups/{group_id}", a.handleDeleteUserGroup)
		r.Get("/device-groups", a.handleListDeviceGroups)
		r.Post("/device-groups", a.handleCreateDeviceGroup)
		r.Patch("/device-groups/{group_id}", a.handleUpdateDeviceGroup)
		r.Delete("/device-groups/{group_id}", a.handleDeleteDeviceGroup)

		r.Get("/settings", a.handleGetSettings)
		r.Patch("/settings", a.handleUpdateSettings)
		r.Put("/settings/logo", a.handleUploadLogo)
		r.Delete("/settings/logo", a.handleDeleteLogo)
	})
}

// ---- request helpers ----

// Paging limits (api/openapi.yaml: page_size default 50, max 500).
const (
	defaultPageSize = 50
	maxPageSize     = 500
	maxQueryLen     = 200
)

// paging is the parsed page/page_size of a list request.
type paging struct {
	Page, Size int
}

func (p paging) offset() int { return (p.Page - 1) * p.Size }

// listPage is the page envelope {"items","total","page","page_size"}.
type listPage[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

func newListPage[T any](items []T, total int, p paging) listPage[T] {
	if items == nil {
		items = []T{}
	}
	return listPage[T]{Items: items, Total: total, Page: p.Page, PageSize: p.Size}
}

// itemsDTO is the {"items": [...]} envelope of unpaged lists.
type itemsDTO[T any] struct {
	Items []T `json:"items"`
}

// query collects validation errors while parsing query parameters.
type query struct {
	v    url.Values
	errs map[string]string
}

func newQuery(r *http.Request) *query { return &query{v: r.URL.Query(), errs: map[string]string{}} }

func (q *query) fail(name, msg string) { q.errs[name] = msg }

func (q *query) str(name string) string { return strings.TrimSpace(q.v.Get(name)) }

// search returns the q parameter (≤ 200 characters).
func (q *query) search() string {
	s := q.str("q")
	if utf8.RuneCountInString(s) > maxQueryLen {
		q.fail("q", "must be at most 200 characters")
		return ""
	}
	return s
}

func (q *query) intParam(name string, def, minV, maxV int) int {
	s := q.str(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < minV || n > maxV {
		q.fail(name, "must be an integer between "+strconv.Itoa(minV)+" and "+strconv.Itoa(maxV))
		return def
	}
	return n
}

func (q *query) paging() paging {
	return paging{Page: q.intParam("page", 1, 1, 1<<30), Size: q.intParam("page_size", defaultPageSize, 1, maxPageSize)}
}

// idParam parses an optional positive id query parameter.
func (q *query) id(name string) *int64 {
	s := q.str(name)
	if s == "" {
		return nil
	}
	id, ok := idParam(s)
	if !ok {
		q.fail(name, "must be a positive integer")
		return nil
	}
	return &id
}

func (q *query) boolean(name string) *bool {
	switch q.str(name) {
	case "":
		return nil
	case "true", "1":
		b := true
		return &b
	case "false", "0":
		b := false
		return &b
	}
	q.fail(name, "must be true or false")
	return nil
}

func (q *query) time(name string) *time.Time {
	s := q.str(name)
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		q.fail(name, "must be an RFC 3339 timestamp")
		return nil
	}
	return &t
}

func (q *query) enum(name string, allowed ...string) string {
	s := q.str(name)
	if s != "" && !slices.Contains(allowed, s) {
		q.fail(name, "must be one of "+strings.Join(allowed, ", "))
		return ""
	}
	return s
}

// sort parses "field" / "-field" against allowed fields.
func (q *query) sort(def string, allowed ...string) (field string, desc bool) {
	s := q.str("sort")
	if s == "" {
		s = def
	}
	desc = strings.HasPrefix(s, "-")
	field = strings.TrimPrefix(s, "-")
	if !slices.Contains(allowed, field) {
		q.fail("sort", "unsupported sort field")
		return "", false
	}
	return field, desc
}

// ok writes the validation error (if any) and reports whether parsing succeeded.
func (q *query) ok(w http.ResponseWriter) bool {
	if len(q.errs) == 0 {
		return true
	}
	validationFailed(w, "invalid query parameters", q.errs)
	return false
}

type errorFields struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// validationFailed writes 400 validation_failed with per-field messages.
func validationFailed(w http.ResponseWriter, msg string, fields map[string]string) {
	httpx.WriteJSON(w, http.StatusBadRequest, struct {
		Error errorFields `json:"error"`
	}{errorFields{Code: "validation_failed", Message: msg, Fields: fields}})
}

func conflict(w http.ResponseWriter, code, msg string) {
	httpx.WriteError(w, http.StatusConflict, code, msg)
}

func forbidden(w http.ResponseWriter, msg string) {
	httpx.WriteError(w, http.StatusForbidden, "forbidden", msg)
}

// pathID parses a positive integer path parameter; it writes 404 on failure.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, ok := idParam(chi.URLParam(r, name))
	if !ok {
		httpx.NotFound(w, r)
	}
	return id, ok
}

// opt is a JSON field that distinguishes "absent" from null.
type opt[T any] struct {
	Set  bool
	Null bool
	V    T
}

func (o *opt[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.V)
}

// ptr returns nil for null, else &V (only meaningful when Set).
func (o opt[T]) ptr() *T {
	if o.Null {
		return nil
	}
	v := o.V
	return &v
}

// runes counts characters for maxLength checks.
func runes(s string) int { return utf8.RuneCountInString(s) }

// ---- shared DTOs ----

// userRefDTO is components.schemas.UserRef.
type userRefDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// groupRefDTO is components.schemas.GroupRef.
type groupRefDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func newUserRef(u store.UserRef) userRefDTO {
	return userRefDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName}
}

// bulkErrorDTO is one entry of BulkResult.errors.
type bulkErrorDTO struct {
	ID      int64  `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// bulkResultDTO is components.schemas.BulkResult.
type bulkResultDTO struct {
	Affected int            `json:"affected"`
	Skipped  int            `json:"skipped"`
	Errors   []bulkErrorDTO `json:"errors,omitempty"`
}

// ---- audit helpers ----

// diff records changed fields for admin_audit: short values are kept as
// [old, new]; long text fields only as "changed".
type diff map[string]any

func (d diff) val(field string, old, new any) {
	if old != new {
		d[field] = []any{old, new}
	}
}

func (d diff) text(field, old, new string) {
	if old != new {
		d[field] = "changed"
	}
}

func idStr(id int64) string { return strconv.FormatInt(id, 10) }

func ptrVal(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func eqID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
