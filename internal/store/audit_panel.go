package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// Panel (admin API) audit queries: offset-paged, filtered, with totals.

// AuditQuery holds the filters shared by every panel audit list.
type AuditQuery struct {
	// Q is a case-insensitive substring search over the category's text columns.
	Q     string
	Since *time.Time // inclusive
	Until *time.Time // exclusive
	// Desc orders newest first.
	Desc   bool
	Limit  int
	Offset int
}

type whereBuilder struct {
	conds []string
	args  []any
}

func (w *whereBuilder) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

// like adds (c1 LIKE q OR c2 LIKE q ...) for a non-empty q.
func (w *whereBuilder) like(q string, cols ...string) {
	if q = strings.TrimSpace(q); q == "" {
		return
	}
	ors := make([]string, len(cols))
	for i, c := range cols {
		ors[i] = c + ` LIKE ? ESCAPE '\'`
		w.args = append(w.args, likeArg(q))
	}
	w.conds = append(w.conds, "("+strings.Join(ors, " OR ")+")")
}

func (w *whereBuilder) timeRange(col string, q AuditQuery) {
	if q.Since != nil {
		w.add(col+" >= ?", toMs(*q.Since))
	}
	if q.Until != nil {
		w.add(col+" < ?", toMs(*q.Until))
	}
}

func (w *whereBuilder) String() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

// orderBy orders by col then the row id (stable paging); idCol names the id.
func orderBy(col, idCol string, desc bool) string {
	if desc {
		return " ORDER BY " + col + " DESC, " + idCol + " DESC"
	}
	return " ORDER BY " + col + ", " + idCol
}

// count runs SELECT count(*) over from+where.
func countRows(ctx context.Context, q db.Querier, from string, w *whereBuilder) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+from+w.String(), w.args...).Scan(&n)
	return n, mapErr(err)
}

func pageArgs(w *whereBuilder, q AuditQuery) []any {
	return append(append([]any{}, w.args...), clampLimit(q.Limit, 50, 1000), max(q.Offset, 0))
}

// DeviceAuditScope restricts device audits.
type DeviceAuditScope struct {
	// OwnerUserID: only audits of devices owned by this user.
	OwnerUserID *int64
	// DeviceRowID: only audits of this device row.
	DeviceRowID *int64
}

func (s DeviceAuditScope) apply(w *whereBuilder) {
	if s.OwnerUserID != nil {
		w.add("a.device_rustdesk_id IN (SELECT rustdesk_id FROM devices WHERE owner_user_id = ?)", *s.OwnerUserID)
	}
	if s.DeviceRowID != nil {
		w.add("a.device_rustdesk_id = (SELECT rustdesk_id FROM devices WHERE id = ?)", *s.DeviceRowID)
	}
}

// DeviceRef is the device joined to an audit row (nil fields when unknown).
type DeviceRef struct {
	ID    *int64
	Alias *string
}

func deviceRef(id sql.NullInt64, alias sql.NullString) DeviceRef {
	r := DeviceRef{ID: ptrID(id)}
	if alias.Valid {
		a := alias.String
		r.Alias = &a
	}
	return r
}

// ConnAuditRow is a connection audit with its device.
type ConnAuditRow struct {
	*ConnectionAudit
	Device DeviceRef
}

// ConnAuditQuery filters ListPanel.
type ConnAuditQuery struct {
	AuditQuery
	Scope    DeviceAuditScope
	ConnType *int
	// Active: true = without ended_at, false = ended.
	Active *bool
	// SortEnded orders by ended_at instead of started_at.
	SortEnded bool
}

const connFrom = `connection_audits a LEFT JOIN devices d ON d.rustdesk_id = a.device_rustdesk_id`

// ListPanel returns a page of connection audits and the total.
func (r *ConnectionAudits) ListPanel(ctx context.Context, f ConnAuditQuery) ([]ConnAuditRow, int, error) {
	w := &whereBuilder{}
	f.Scope.apply(w)
	w.timeRange("a.started_at", f.AuditQuery)
	if f.ConnType != nil {
		w.add("a.conn_type = ?", *f.ConnType)
	}
	if f.Active != nil {
		if *f.Active {
			w.add("a.ended_at IS NULL")
		} else {
			w.add("a.ended_at IS NOT NULL")
		}
	}
	w.like(f.Q, "a.device_rustdesk_id", "a.controller_rustdesk_id", "a.controller_name", "a.controller_ip",
		"a.note", "coalesce(d.alias, '')", "coalesce(d.hostname, '')")
	total, err := countRows(ctx, r.q, connFrom, w)
	if err != nil {
		return nil, 0, err
	}
	col := "a.started_at"
	if f.SortEnded {
		col = "coalesce(a.ended_at, 0)" // open sessions sort as oldest on both engines
	}
	rows, err := r.q.QueryContext(ctx, `SELECT a.id, a.guid, a.device_rustdesk_id, a.device_uuid, a.conn_id,
		a.session_id, a.conn_type, a.primary_auth, a.two_factor, a.conn_audit_ref, a.controller_rustdesk_id,
		a.controller_name, a.controller_ip, a.note, a.started_at, a.ended_at, a.created_at, a.updated_at,
		d.id, d.alias FROM `+connFrom+w.String()+orderBy(col, "a.id", f.Desc)+` LIMIT ? OFFSET ?`,
		pageArgs(w, f.AuditQuery)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []ConnAuditRow{}
	for rows.Next() {
		var (
			devID sql.NullInt64
			alias sql.NullString
		)
		a, err := scanConnAudit(scanFunc(func(dest ...any) error { return rows.Scan(append(dest, &devID, &alias)...) }))
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ConnAuditRow{ConnectionAudit: a, Device: deviceRef(devID, alias)})
	}
	return out, total, mapErr(rows.Err())
}

// FileAuditRow is a file-transfer audit with its device.
type FileAuditRow struct {
	*FileTransferAudit
	Device DeviceRef
}

// FileAuditQuery filters ListPanel.
type FileAuditQuery struct {
	AuditQuery
	Scope     DeviceAuditScope
	Direction *int
}

// ListPanel returns a page of file transfer audits and the total.
func (r *FileTransferAudits) ListPanel(ctx context.Context, f FileAuditQuery) ([]FileAuditRow, int, error) {
	const from = `file_transfer_audits a LEFT JOIN devices d ON d.rustdesk_id = a.device_rustdesk_id`
	w := &whereBuilder{}
	f.Scope.apply(w)
	w.timeRange("a.created_at", f.AuditQuery)
	if f.Direction != nil {
		w.add("a.direction = ?", *f.Direction)
	}
	w.like(f.Q, "a.device_rustdesk_id", "a.controller_rustdesk_id", "a.controller_name", "a.controller_ip",
		"a.path", "coalesce(d.alias, '')")
	total, err := countRows(ctx, r.q, from, w)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT a.id, a.device_rustdesk_id, a.device_uuid, a.controller_rustdesk_id,
		a.controller_name, a.controller_ip, a.direction, a.is_file, a.path, a.file_count, a.info, a.created_at,
		d.id, d.alias FROM `+from+w.String()+orderBy("a.created_at", "a.id", f.Desc)+
		` LIMIT ? OFFSET ?`, pageArgs(w, f.AuditQuery)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []FileAuditRow{}
	for rows.Next() {
		var (
			a         FileTransferAudit
			uuid      sql.NullString
			isFile    int
			createdAt int64
			devID     sql.NullInt64
			alias     sql.NullString
		)
		if err := rows.Scan(&a.ID, &a.DeviceID, &uuid, &a.ControllerRustdeskID, &a.ControllerName, &a.ControllerIP,
			&a.Direction, &isFile, &a.Path, &a.FileCount, &a.Info, &createdAt, &devID, &alias); err != nil {
			return nil, 0, mapErr(err)
		}
		a.DeviceUUID, a.IsFile, a.CreatedAt = uuid.String, isFile == 1, fromMs(createdAt)
		out = append(out, FileAuditRow{FileTransferAudit: &a, Device: deviceRef(devID, alias)})
	}
	return out, total, mapErr(rows.Err())
}

// AlarmAuditRow is an alarm audit with its device.
type AlarmAuditRow struct {
	*AlarmAudit
	Device DeviceRef
}

// AlarmAuditQuery filters ListPanel.
type AlarmAuditQuery struct {
	AuditQuery
	AlarmType *int
}

// ListPanel returns a page of alarm audits and the total.
func (r *AlarmAudits) ListPanel(ctx context.Context, f AlarmAuditQuery) ([]AlarmAuditRow, int, error) {
	const from = `alarm_audits a LEFT JOIN devices d ON d.rustdesk_id = a.device_rustdesk_id`
	w := &whereBuilder{}
	w.timeRange("a.created_at", f.AuditQuery)
	if f.AlarmType != nil {
		w.add("a.alarm_type = ?", *f.AlarmType)
	}
	w.like(f.Q, "a.device_rustdesk_id", "a.info", "coalesce(d.alias, '')")
	total, err := countRows(ctx, r.q, from, w)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT a.id, a.device_rustdesk_id, a.device_uuid, a.alarm_type, a.conn_id,
		a.conn_audit_ref, a.info, a.created_at, d.id, d.alias FROM `+from+w.String()+
		orderBy("a.created_at", "a.id", f.Desc)+` LIMIT ? OFFSET ?`, pageArgs(w, f.AuditQuery)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []AlarmAuditRow{}
	for rows.Next() {
		var (
			a         AlarmAudit
			uuid, ref sql.NullString
			connID    sql.NullInt64
			createdAt int64
			devID     sql.NullInt64
			alias     sql.NullString
		)
		if err := rows.Scan(&a.ID, &a.DeviceID, &uuid, &a.AlarmType, &connID, &ref, &a.Info, &createdAt,
			&devID, &alias); err != nil {
			return nil, 0, mapErr(err)
		}
		a.DeviceUUID, a.ConnAuditRef, a.ConnID, a.CreatedAt = uuid.String, ref.String, ptrID(connID), fromMs(createdAt)
		out = append(out, AlarmAuditRow{AlarmAudit: &a, Device: deviceRef(devID, alias)})
	}
	return out, total, mapErr(rows.Err())
}

// LoginEventRow is a login event with the user joined in.
type LoginEventRow struct {
	*LoginEvent
	Username        string
	UserDisplayName string
}

// LoginEventQuery filters ListPanel.
type LoginEventQuery struct {
	AuditQuery
	UserID *int64
	// Result: success | failure | "".
	Result string
	Method string
	// WebOnly: true = panel logins (client_kind webadmin), false = client
	// logins, nil = both.
	WebOnly       *bool
	IncludeHidden bool
}

// ListPanel returns a page of login events and the total.
func (r *LoginEvents) ListPanel(ctx context.Context, f LoginEventQuery) ([]LoginEventRow, int, error) {
	const from = `login_events a LEFT JOIN users u ON u.id = a.user_id`
	w := &whereBuilder{}
	w.timeRange("a.created_at", f.AuditQuery)
	if f.UserID != nil {
		w.add("a.user_id = ?", *f.UserID)
	}
	if f.Result != "" {
		w.add("a.result = ?", f.Result)
	}
	if f.Method != "" {
		w.add("a.method = ?", f.Method)
	}
	if f.WebOnly != nil {
		if *f.WebOnly {
			w.add("a.client_kind = 'webadmin'")
		} else {
			w.add("coalesce(a.client_kind, '') <> 'webadmin'")
		}
	}
	if !f.IncludeHidden {
		w.add("a.hidden_by_user = 0")
	}
	w.like(f.Q, "coalesce(a.username_attempted, '')", "coalesce(u.username, '')", "coalesce(a.ip, '')",
		"coalesce(a.device_rustdesk_id, '')", "coalesce(a.failure_reason, '')")
	total, err := countRows(ctx, r.q, from, w)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT a.id, a.user_id, a.username_attempted, a.token_id, a.method, a.result,
		a.failure_reason, a.client_kind, a.device_rustdesk_id, a.device_uuid, a.platform, a.ip, a.user_agent,
		a.hidden_by_user, a.created_at, coalesce(u.username, ''), coalesce(u.display_name, '') FROM `+from+w.String()+
		orderBy("a.created_at", "a.id", f.Desc)+` LIMIT ? OFFSET ?`, pageArgs(w, f.AuditQuery)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []LoginEventRow{}
	for rows.Next() {
		var (
			e                                       LoginEvent
			row                                     LoginEventRow
			userID, tokenID                         sql.NullInt64
			attempted, reason, kind, devID, devUUID sql.NullString
			platform, ip, ua                        sql.NullString
			hidden                                  int
			createdAt                               int64
		)
		if err := rows.Scan(&e.ID, &userID, &attempted, &tokenID, &e.Method, &e.Result, &reason, &kind, &devID,
			&devUUID, &platform, &ip, &ua, &hidden, &createdAt, &row.Username, &row.UserDisplayName); err != nil {
			return nil, 0, mapErr(err)
		}
		e.UserID, e.TokenID = ptrID(userID), ptrID(tokenID)
		e.UsernameAttempted, e.FailureReason, e.ClientKind = attempted.String, reason.String, kind.String
		e.DeviceRustdeskID, e.DeviceUUID, e.Platform = devID.String, devUUID.String, platform.String
		e.IP, e.UserAgent, e.HiddenByUser = ip.String, ua.String, hidden == 1
		e.CreatedAt = fromMs(createdAt)
		row.LoginEvent = &e
		out = append(out, row)
	}
	return out, total, mapErr(rows.Err())
}

// AdminAuditQuery filters ListPanel.
type AdminAuditQuery struct {
	AuditQuery
	ActorUserID *int64
	// ActionPrefix matches e.g. "user." ; Action matches exactly.
	ActionPrefix string
	Action       string
	TargetType   string
}

// ListPanel returns a page of admin audit entries and the total.
func (r *AdminAudit) ListPanel(ctx context.Context, f AdminAuditQuery) ([]*AuditEntry, int, error) {
	w := &whereBuilder{}
	w.timeRange("created_at", f.AuditQuery)
	if f.ActorUserID != nil {
		w.add("actor_user_id = ?", *f.ActorUserID)
	}
	if f.ActionPrefix != "" {
		w.add(`action LIKE ? ESCAPE '\'`, escapeLike(f.ActionPrefix)+"%")
	}
	if f.Action != "" {
		w.add("action = ?", f.Action)
	}
	if f.TargetType != "" {
		w.add("target_type = ?", f.TargetType)
	}
	w.like(f.Q, "actor_name", "action", "coalesce(target_type, '')", "coalesce(target_id, '')", "details")
	total, err := countRows(ctx, r.q, "admin_audit", w)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, actor_user_id, actor_name, actor_ip, action, target_type, target_id,
		details, created_at FROM admin_audit`+w.String()+orderBy("created_at", "id", f.Desc)+` LIMIT ? OFFSET ?`,
		pageArgs(w, f.AuditQuery)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []*AuditEntry{}
	for rows.Next() {
		var (
			e              AuditEntry
			actor          sql.NullInt64
			ip, ttype, tid sql.NullString
			details        string
			createdAt      int64
		)
		if err := rows.Scan(&e.ID, &actor, &e.ActorName, &ip, &e.Action, &ttype, &tid, &details, &createdAt); err != nil {
			return nil, 0, mapErr(err)
		}
		e.ActorUserID = ptrID(actor)
		e.ActorIP, e.TargetType, e.TargetID = ip.String, ttype.String, tid.String
		e.CreatedAt = fromMs(createdAt)
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil || e.Details == nil {
			e.Details = map[string]any{}
		}
		out = append(out, &e)
	}
	return out, total, mapErr(rows.Err())
}

// ---- punch requests ----

// PunchRequest is a row of punch_requests (collected from hbbs "pr").
type PunchRequest struct {
	ID           int64
	At           time.Time
	FromIP       string
	ToRustdeskID string
	ToIP         string
	CreatedAt    time.Time
}

// PunchRequests is the punch_requests repository.
type PunchRequests struct {
	q   db.Querier
	d   *db.DB
	now func() time.Time
}

// Insert stores rows in one transaction.
func (r *PunchRequests) Insert(ctx context.Context, rows []PunchRequest) error {
	return atomic(ctx, r.d, r.q, func(q db.Querier) error {
		now := toMs(r.now())
		for _, p := range rows {
			if _, err := q.ExecContext(ctx, `INSERT INTO punch_requests (at, from_ip, to_rustdesk_id, to_ip, created_at)
				VALUES (?, ?, ?, ?, ?)`, toMs(p.At), p.FromIP, p.ToRustdeskID, nullStr(p.ToIP), now); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

// ListPanel returns a page of punch requests (newest first) and the total.
func (r *PunchRequests) ListPanel(ctx context.Context, f AuditQuery) ([]PunchRequest, int, error) {
	w := &whereBuilder{}
	w.timeRange("at", f)
	w.like(f.Q, "from_ip", "to_rustdesk_id", "coalesce(to_ip, '')")
	total, err := countRows(ctx, r.q, "punch_requests", w)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, at, from_ip, to_rustdesk_id, to_ip, created_at FROM punch_requests`+
		w.String()+orderBy("at", "id", true)+` LIMIT ? OFFSET ?`, pageArgs(w, f)...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []PunchRequest{}
	for rows.Next() {
		var (
			p             PunchRequest
			toIP          sql.NullString
			at, createdAt int64
		)
		if err := rows.Scan(&p.ID, &at, &p.FromIP, &p.ToRustdeskID, &toIP, &createdAt); err != nil {
			return nil, 0, mapErr(err)
		}
		p.At, p.ToIP, p.CreatedAt = fromMs(at), toIP.String, fromMs(createdAt)
		out = append(out, p)
	}
	return out, total, mapErr(rows.Err())
}

// ---- retention ----

// Retention categories (PanelSettings.retention keys without "_days").
const (
	RetentionLoginEvents        = "login_events"
	RetentionConnectionAudits   = "connection_audits"
	RetentionFileTransferAudits = "file_transfer_audits"
	RetentionAlarmAudits        = "alarm_audits"
	RetentionAdminAudit         = "admin_audit"
	RetentionPunchRequests      = "punch_requests"
)

// RetentionDefaults are the retention days of categories without a stored
// setting (retention.<category>_days); 0 keeps rows forever.
var RetentionDefaults = map[string]int{
	RetentionLoginEvents: 180, RetentionConnectionAudits: 180, RetentionFileTransferAudits: 180,
	RetentionAlarmAudits: 180, RetentionAdminAudit: 365, RetentionPunchRequests: 30,
}

// RetentionSettingKey is the settings key of a retention category
// (PanelSettings.retention.<category>_days).
func RetentionSettingKey(category string) string { return "retention." + category + "_days" }

var retentionQueries = map[string]string{
	RetentionLoginEvents:        `DELETE FROM login_events WHERE created_at < ?`,
	RetentionConnectionAudits:   `DELETE FROM connection_audits WHERE started_at < ? AND (ended_at IS NOT NULL OR started_at < ?)`,
	RetentionFileTransferAudits: `DELETE FROM file_transfer_audits WHERE created_at < ?`,
	RetentionAlarmAudits:        `DELETE FROM alarm_audits WHERE created_at < ?`,
	RetentionAdminAudit:         `DELETE FROM admin_audit WHERE created_at < ?`,
	RetentionPunchRequests:      `DELETE FROM punch_requests WHERE at < ?`,
}

// RetentionCategories lists the categories in a stable order.
func RetentionCategories() []string {
	return []string{RetentionLoginEvents, RetentionConnectionAudits, RetentionFileTransferAudits,
		RetentionAlarmAudits, RetentionAdminAudit, RetentionPunchRequests}
}

// RetentionDays returns the retention of every category: the
// retention.<category>_days settings written by PATCH /settings, else
// RetentionDefaults. 0 keeps rows forever. This is the single source of truth
// for the settings API and the janitor.
func (r *Settings) RetentionDays(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, c := range RetentionCategories() {
		v, err := GetOr(ctx, r, RetentionSettingKey(c), RetentionDefaults[c])
		if err != nil {
			return out, err
		}
		out[c] = max(v, 0)
	}
	return out, nil
}

// PurgeRetention deletes audit rows older than each category's retention
// (open connections are kept for twice the retention) and returns the number
// of deleted rows per category.
func (s *Store) PurgeRetention(ctx context.Context, now time.Time) (map[string]int64, error) {
	days, err := s.Settings.RetentionDays(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, c := range RetentionCategories() {
		d := days[c]
		if d <= 0 {
			continue
		}
		cutoff := now.Add(-time.Duration(d) * 24 * time.Hour)
		args := []any{toMs(cutoff)}
		if c == RetentionConnectionAudits {
			args = append(args, toMs(cutoff.Add(-time.Duration(d)*24*time.Hour)))
		}
		res, err := s.q.ExecContext(ctx, retentionQueries[c], args...)
		if err != nil {
			return out, mapErr(err)
		}
		out[c], _ = res.RowsAffected()
	}
	return out, nil
}
