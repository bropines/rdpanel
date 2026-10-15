package adminapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/loginlog"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Audit lists (/audit/*). Connections and file transfers are scoped to the
// caller's own devices for the user role; logins to the caller's own events.

func (c *b2bCore) registerAudit(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireUser)
		r.Get("/audit/connections", c.handleAuditConnections)
		r.Get("/audit/files", c.handleAuditFiles)
		r.Get("/audit/logins", c.handleAuditLogins)
	})
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/audit/alarms", c.handleAuditAlarms)
		r.Get("/audit/admin", c.handleAuditAdmin)
	})
}

// deviceScope returns the device restriction of the caller.
func deviceScope(u *store.User, deviceID *int64) store.DeviceAuditScope {
	s := store.DeviceAuditScope{DeviceRowID: deviceID}
	if !u.IsAdmin() {
		s.OwnerUserID = &u.ID
	}
	return s
}

// jsonObject returns stored JSON as an object (non-objects are wrapped).
func jsonObject(s string) json.RawMessage {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{") && json.Valid([]byte(t)) {
		return json.RawMessage(t)
	}
	if t == "" {
		return json.RawMessage("{}")
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"value": json.RawMessage(jsonOrString(t))})
	return b
}

func jsonOrString(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// connAuditDTO is components.schemas.ConnectionAudit.
type connAuditDTO struct {
	ID                   int64      `json:"id"`
	GUID                 string     `json:"guid"`
	DeviceID             *int64     `json:"device_id"`
	DeviceRustdeskID     string     `json:"device_rustdesk_id"`
	DeviceAlias          *string    `json:"device_alias"`
	ControllerRustdeskID *string    `json:"controller_rustdesk_id"`
	ControllerName       *string    `json:"controller_name"`
	ControllerIP         *string    `json:"controller_ip"`
	ConnType             int        `json:"conn_type"`
	SessionID            *string    `json:"session_id"`
	Note                 *string    `json:"note"`
	StartedAt            time.Time  `json:"started_at"`
	EndedAt              *time.Time `json:"ended_at"`
}

func (c *b2bCore) handleAuditConnections(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	q := newListQuery(r)
	p := q.paging()
	sort := q.sort("-started_at", "started_at", "ended_at")
	f := store.ConnAuditQuery{AuditQuery: q.auditQuery(p, strings.HasPrefix(sort, "-")),
		Scope: deviceScope(u, q.int64Ptr("device_id")), ConnType: q.intPtr("conn_type", 0, 4),
		Active: q.boolPtr("active"), SortEnded: strings.TrimPrefix(sort, "-") == "ended_at"}
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.ConnectionAudits.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list connection audits", err)
		return
	}
	out := make([]connAuditDTO, 0, len(rows))
	for _, a := range rows {
		out = append(out, connAuditDTO{ID: a.ID, GUID: a.GUID, DeviceID: a.Device.ID, DeviceRustdeskID: a.DeviceID,
			DeviceAlias: a.Device.Alias, ControllerRustdeskID: strPtr(a.ControllerRustdeskID),
			ControllerName: strPtr(a.ControllerName), ControllerIP: strPtr(a.ControllerIP), ConnType: a.ConnType,
			SessionID: strPtr(a.SessionID), Note: strPtr(a.Note), StartedAt: a.StartedAt, EndedAt: a.EndedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// fileAuditDTO is components.schemas.FileTransferAudit.
type fileAuditDTO struct {
	ID                   int64           `json:"id"`
	DeviceID             *int64          `json:"device_id"`
	DeviceRustdeskID     string          `json:"device_rustdesk_id"`
	DeviceAlias          *string         `json:"device_alias"`
	ControllerRustdeskID *string         `json:"controller_rustdesk_id"`
	ControllerName       *string         `json:"controller_name"`
	ControllerIP         *string         `json:"controller_ip"`
	Direction            int             `json:"direction"`
	IsFile               bool            `json:"is_file"`
	Path                 string          `json:"path"`
	FileCount            int64           `json:"file_count"`
	Info                 json.RawMessage `json:"info"`
	CreatedAt            time.Time       `json:"created_at"`
}

func (c *b2bCore) handleAuditFiles(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	q := newListQuery(r)
	p := q.paging()
	sort := q.sort("-created_at", "created_at")
	f := store.FileAuditQuery{AuditQuery: q.auditQuery(p, strings.HasPrefix(sort, "-")),
		Scope: deviceScope(u, q.int64Ptr("device_id")), Direction: q.intPtr("direction", 0, 1)}
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.FileTransferAudits.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list file audits", err)
		return
	}
	out := make([]fileAuditDTO, 0, len(rows))
	for _, a := range rows {
		out = append(out, fileAuditDTO{ID: a.ID, DeviceID: a.Device.ID, DeviceRustdeskID: a.DeviceID,
			DeviceAlias: a.Device.Alias, ControllerRustdeskID: strPtr(a.ControllerRustdeskID),
			ControllerName: strPtr(a.ControllerName), ControllerIP: strPtr(a.ControllerIP), Direction: a.Direction,
			IsFile: a.IsFile, Path: a.Path, FileCount: a.FileCount, Info: jsonObject(a.Info), CreatedAt: a.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// alarmAuditDTO is components.schemas.AlarmAudit.
type alarmAuditDTO struct {
	ID               int64           `json:"id"`
	DeviceID         *int64          `json:"device_id"`
	DeviceRustdeskID string          `json:"device_rustdesk_id"`
	AlarmType        int             `json:"alarm_type"`
	ConnID           *int64          `json:"conn_id"`
	IP               *string         `json:"ip"`
	Info             json.RawMessage `json:"info"`
	CreatedAt        time.Time       `json:"created_at"`
}

func alarmIP(info json.RawMessage) *string {
	var m map[string]any
	if json.Unmarshal(info, &m) != nil {
		return nil
	}
	if s, ok := m["ip"].(string); ok && s != "" {
		return &s
	}
	return nil
}

func (c *b2bCore) handleAuditAlarms(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.paging()
	sort := q.sort("-created_at", "created_at")
	f := store.AlarmAuditQuery{AuditQuery: q.auditQuery(p, strings.HasPrefix(sort, "-")),
		AlarmType: q.intPtr("alarm_type", 0, 1<<30)}
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.AlarmAudits.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list alarm audits", err)
		return
	}
	out := make([]alarmAuditDTO, 0, len(rows))
	for _, a := range rows {
		info := jsonObject(a.Info)
		out = append(out, alarmAuditDTO{ID: a.ID, DeviceID: a.Device.ID, DeviceRustdeskID: a.DeviceID,
			AlarmType: a.AlarmType, ConnID: a.ConnID, IP: alarmIP(info), Info: info, CreatedAt: a.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// loginEventDTO is components.schemas.LoginEvent.
type loginEventDTO struct {
	ID                int64         `json:"id"`
	User              *bookOwnerDTO `json:"user"`
	UsernameAttempted *string       `json:"username_attempted"`
	Method            string        `json:"method"`
	ProviderSlug      *string       `json:"provider_slug"`
	Result            string        `json:"result"`
	FailureReason     *string       `json:"failure_reason"`
	ClientKind        string        `json:"client_kind"`
	DeviceRustdeskID  *string       `json:"device_rustdesk_id"`
	Platform          *string       `json:"platform"`
	IP                *string       `json:"ip"`
	UserAgent         *string       `json:"user_agent"`
	CreatedAt         time.Time     `json:"created_at"`
}

func (c *b2bCore) handleAuditLogins(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	q := newListQuery(r)
	p := q.paging()
	sort := q.sort("-created_at", "created_at")
	f := store.LoginEventQuery{AuditQuery: q.auditQuery(p, strings.HasPrefix(sort, "-")),
		UserID: q.int64Ptr("user_id"), Result: q.enum("result", store.LoginSuccess, store.LoginFailure),
		Method:        q.enum("method", store.LoginMethodPassword, store.LoginMethodOIDC, store.LoginMethodWebAuth),
		IncludeHidden: u.IsAdmin()}
	switch q.enum("client_kind", "web", "client") {
	case "web":
		f.WebOnly = ptrBool(true)
	case "client":
		f.WebOnly = ptrBool(false)
	}
	if !q.ok(w) {
		return
	}
	if !u.IsAdmin() {
		if f.UserID != nil && *f.UserID != u.ID {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "users can only see their own login events")
			return
		}
		f.UserID = &u.ID
	}
	rows, total, err := c.d.Store.LoginEvents.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list login events", err)
		return
	}
	out := make([]loginEventDTO, 0, len(rows))
	for _, e := range rows {
		d := loginEventDTO{ID: e.ID, UsernameAttempted: strPtr(e.UsernameAttempted), Method: e.Method,
			Result: e.Result, FailureReason: strPtr(e.FailureReason), ClientKind: "client",
			DeviceRustdeskID: strPtr(e.DeviceRustdeskID), Platform: strPtr(e.Platform), IP: strPtr(e.IP),
			UserAgent: strPtr(e.UserAgent), CreatedAt: e.CreatedAt}
		if e.ClientKind == loginlog.ClientWebAdmin {
			d.ClientKind = "web"
		}
		if e.UserID != nil && e.Username != "" {
			d.User = &bookOwnerDTO{ID: *e.UserID, Username: e.Username, DisplayName: e.UserDisplayName}
		}
		out = append(out, d)
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

func ptrBool(b bool) *bool { return &b }

// adminAuditDTO is components.schemas.AdminAuditEvent.
type adminAuditDTO struct {
	ID          int64          `json:"id"`
	ActorUserID *int64         `json:"actor_user_id"`
	ActorName   string         `json:"actor_name"`
	ActorIP     *string        `json:"actor_ip"`
	Action      string         `json:"action"`
	TargetType  *string        `json:"target_type"`
	TargetID    *string        `json:"target_id"`
	TargetLabel *string        `json:"target_label"`
	Details     map[string]any `json:"details"`
	CreatedAt   time.Time      `json:"created_at"`
}

// targetLabel picks a human-readable target from the details.
func targetLabel(d map[string]any) *string {
	for _, k := range []string{"label", "username", "name", "rustdesk_id", "slug"} {
		if s, ok := d[k].(string); ok && s != "" {
			return &s
		}
	}
	return nil
}

func (c *b2bCore) handleAuditAdmin(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.paging()
	sort := q.sort("-created_at", "created_at")
	f := store.AdminAuditQuery{AuditQuery: q.auditQuery(p, strings.HasPrefix(sort, "-")),
		ActorUserID: q.int64Ptr("actor_user_id"), ActionPrefix: q.str("action"), TargetType: q.str("target_type")}
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.AdminAudit.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list admin audit", err)
		return
	}
	out := make([]adminAuditDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, adminAuditDTO{ID: e.ID, ActorUserID: e.ActorUserID, ActorName: e.ActorName,
			ActorIP: strPtr(e.ActorIP), Action: e.Action, TargetType: strPtr(e.TargetType), TargetID: strPtr(e.TargetID),
			TargetLabel: targetLabel(e.Details), Details: e.Details, CreatedAt: e.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}
