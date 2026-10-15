package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// ConnectionAudit is a row of connection_audits (POST /api/audit/conn).
type ConnectionAudit struct {
	ID         int64
	GUID       string
	DeviceID   string // controlled device RustDesk ID
	DeviceUUID string
	ConnID     int64
	// SessionID is the client's u64 session id in decimal ("" = unknown).
	SessionID            string
	ConnType             int
	PrimaryAuth          *int
	TwoFactor            *int
	ConnAuditRef         string
	ControllerRustdeskID string
	ControllerName       string
	ControllerIP         string
	Note                 string
	StartedAt            time.Time
	EndedAt              *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ConnectionAudits is the connection_audits repository.
type ConnectionAudits struct {
	q     db.Querier
	now   func() time.Time
	newID func() string
}

const connAuditColumns = `id, guid, device_rustdesk_id, device_uuid, conn_id, session_id, conn_type, primary_auth,
	two_factor, conn_audit_ref, controller_rustdesk_id, controller_name, controller_ip, note, started_at, ended_at,
	created_at, updated_at`

func scanConnAudit(row interface{ Scan(...any) error }) (*ConnectionAudit, error) {
	var (
		a                               ConnectionAudit
		uuid, ref                       sql.NullString
		primary, twoFactor, ended       sql.NullInt64
		startedAt, createdAt, updatedAt int64
	)
	err := row.Scan(&a.ID, &a.GUID, &a.DeviceID, &uuid, &a.ConnID, &a.SessionID, &a.ConnType, &primary, &twoFactor,
		&ref, &a.ControllerRustdeskID, &a.ControllerName, &a.ControllerIP, &a.Note, &startedAt, &ended,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	a.DeviceUUID, a.ConnAuditRef = uuid.String, ref.String
	a.PrimaryAuth, a.TwoFactor = ptrInt(primary), ptrInt(twoFactor)
	a.StartedAt, a.EndedAt = fromMs(startedAt), ptrMs(ended)
	a.CreatedAt, a.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &a, nil
}

func ptrInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// Create inserts a; an empty GUID is generated, a zero StartedAt is now.
func (r *ConnectionAudits) Create(ctx context.Context, a *ConnectionAudit) error {
	now := r.now()
	if a.GUID == "" {
		a.GUID = r.newID()
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = now
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO connection_audits (guid, device_rustdesk_id, device_uuid,
		conn_id, session_id, conn_type, primary_auth, two_factor, conn_audit_ref, controller_rustdesk_id,
		controller_name, controller_ip, note, started_at, ended_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.GUID, a.DeviceID, nullStr(a.DeviceUUID), a.ConnID, a.SessionID, a.ConnType, nullInt(a.PrimaryAuth),
		nullInt(a.TwoFactor), nullStr(a.ConnAuditRef), a.ControllerRustdeskID, a.ControllerName, a.ControllerIP,
		a.Note, toMs(a.StartedAt), nullMs(a.EndedAt), toMs(a.CreatedAt), toMs(a.UpdatedAt)).Scan(&a.ID))
}

// Update writes every mutable column of a.
func (r *ConnectionAudits) Update(ctx context.Context, a *ConnectionAudit) error {
	a.UpdatedAt = r.now()
	return expectOne(r.q.ExecContext(ctx, `UPDATE connection_audits SET session_id = ?, conn_type = ?,
		primary_auth = ?, two_factor = ?, conn_audit_ref = ?, controller_rustdesk_id = ?, controller_name = ?,
		controller_ip = ?, note = ?, ended_at = ?, updated_at = ? WHERE id = ?`,
		a.SessionID, a.ConnType, nullInt(a.PrimaryAuth), nullInt(a.TwoFactor), nullStr(a.ConnAuditRef),
		a.ControllerRustdeskID, a.ControllerName, a.ControllerIP, a.Note, nullMs(a.EndedAt), toMs(a.UpdatedAt), a.ID))
}

// GetByGUID returns the record with guid.
func (r *ConnectionAudits) GetByGUID(ctx context.Context, guid string) (*ConnectionAudit, error) {
	return scanConnAudit(r.q.QueryRowContext(ctx, `SELECT `+connAuditColumns+` FROM connection_audits WHERE guid = ?`, guid))
}

// FindOpen returns the newest record of (device, connID) that has not ended.
// conn_id is a client-local counter, so only open records started within
// maxAge are considered.
func (r *ConnectionAudits) FindOpen(ctx context.Context, deviceID string, connID int64, maxAge time.Duration) (*ConnectionAudit, error) {
	return scanConnAudit(r.q.QueryRowContext(ctx, `SELECT `+connAuditColumns+` FROM connection_audits
		WHERE device_rustdesk_id = ? AND conn_id = ? AND ended_at IS NULL AND started_at >= ?
		ORDER BY id DESC LIMIT 1`, deviceID, connID, toMs(r.now().Add(-maxAge))))
}

// FindBySession returns the newest record of (device, sessionID), optionally
// restricted to connType.
func (r *ConnectionAudits) FindBySession(ctx context.Context, deviceID, sessionID string, connType *int) (*ConnectionAudit, error) {
	q := `SELECT ` + connAuditColumns + ` FROM connection_audits WHERE device_rustdesk_id = ? AND session_id = ?`
	args := []any{deviceID, sessionID}
	if connType != nil {
		q, args = q+` AND conn_type = ?`, append(args, *connType)
	}
	return scanConnAudit(r.q.QueryRowContext(ctx, q+` ORDER BY id DESC LIMIT 1`, args...))
}

// SetNote sets the note of the record with guid.
func (r *ConnectionAudits) SetNote(ctx context.Context, guid, note string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE connection_audits SET note = ?, updated_at = ? WHERE guid = ?`,
		note, toMs(r.now()), guid))
}

// ClientAuditFilter selects client audit rows (newest first, keyset paging).
type ClientAuditFilter struct {
	DeviceID string
	Since    *time.Time
	Until    *time.Time
	BeforeID int64
	Limit    int
}

func (f ClientAuditFilter) build(timeCol string) (string, []any) {
	var (
		where []string
		args  []any
	)
	if f.DeviceID != "" {
		where, args = append(where, "device_rustdesk_id = ?"), append(args, f.DeviceID)
	}
	if f.Since != nil {
		where, args = append(where, timeCol+" >= ?"), append(args, toMs(*f.Since))
	}
	if f.Until != nil {
		where, args = append(where, timeCol+" < ?"), append(args, toMs(*f.Until))
	}
	if f.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, f.BeforeID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	return cond + " ORDER BY id DESC LIMIT ?", append(args, clampLimit(f.Limit, 100, 1000))
}

// List returns connection audits matching f.
func (r *ConnectionAudits) List(ctx context.Context, f ClientAuditFilter) ([]*ConnectionAudit, error) {
	cond, args := f.build("started_at")
	rows, err := r.q.QueryContext(ctx, `SELECT `+connAuditColumns+` FROM connection_audits`+cond, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*ConnectionAudit
	for rows.Next() {
		a, err := scanConnAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, mapErr(rows.Err())
}

// FileTransferAudit is a row of file_transfer_audits (POST /api/audit/file).
type FileTransferAudit struct {
	ID                   int64
	DeviceID             string
	DeviceUUID           string
	ControllerRustdeskID string
	ControllerName       string
	ControllerIP         string
	// Direction: 0 controlled->controller, 1 controller->controlled.
	Direction int
	IsFile    bool
	Path      string
	FileCount int64
	// Info is the client JSON (must be valid JSON; "" stores "{}").
	Info      string
	CreatedAt time.Time
}

// FileTransferAudits is the file_transfer_audits repository.
type FileTransferAudits struct {
	q   db.Querier
	now func() time.Time
}

// Create inserts a.
func (r *FileTransferAudits) Create(ctx context.Context, a *FileTransferAudit) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO file_transfer_audits (device_rustdesk_id, device_uuid,
		controller_rustdesk_id, controller_name, controller_ip, direction, is_file, path, file_count, info, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.DeviceID, nullStr(a.DeviceUUID), a.ControllerRustdeskID, a.ControllerName, a.ControllerIP, a.Direction,
		boolInt(a.IsFile), a.Path, a.FileCount, jsonOrEmpty(a.Info), toMs(a.CreatedAt)).Scan(&a.ID))
}

// List returns file transfer audits matching f.
func (r *FileTransferAudits) List(ctx context.Context, f ClientAuditFilter) ([]*FileTransferAudit, error) {
	cond, args := f.build("created_at")
	rows, err := r.q.QueryContext(ctx, `SELECT id, device_rustdesk_id, device_uuid, controller_rustdesk_id,
		controller_name, controller_ip, direction, is_file, path, file_count, info, created_at
		FROM file_transfer_audits`+cond, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*FileTransferAudit
	for rows.Next() {
		var (
			a         FileTransferAudit
			uuid      sql.NullString
			isFile    int
			createdAt int64
		)
		if err := rows.Scan(&a.ID, &a.DeviceID, &uuid, &a.ControllerRustdeskID, &a.ControllerName, &a.ControllerIP,
			&a.Direction, &isFile, &a.Path, &a.FileCount, &a.Info, &createdAt); err != nil {
			return nil, mapErr(err)
		}
		a.DeviceUUID, a.IsFile, a.CreatedAt = uuid.String, isFile == 1, fromMs(createdAt)
		out = append(out, &a)
	}
	return out, mapErr(rows.Err())
}

// AlarmAudit is a row of alarm_audits (POST /api/audit/alarm).
type AlarmAudit struct {
	ID           int64
	DeviceID     string
	DeviceUUID   string
	AlarmType    int
	ConnID       *int64
	ConnAuditRef string
	Info         string
	CreatedAt    time.Time
}

// AlarmAudits is the alarm_audits repository.
type AlarmAudits struct {
	q   db.Querier
	now func() time.Time
}

// Create inserts a.
func (r *AlarmAudits) Create(ctx context.Context, a *AlarmAudit) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO alarm_audits (device_rustdesk_id, device_uuid, alarm_type,
		conn_id, conn_audit_ref, info, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		a.DeviceID, nullStr(a.DeviceUUID), a.AlarmType, nullID(a.ConnID), nullStr(a.ConnAuditRef),
		jsonOrEmpty(a.Info), toMs(a.CreatedAt)).Scan(&a.ID))
}

// List returns alarm audits matching f.
func (r *AlarmAudits) List(ctx context.Context, f ClientAuditFilter) ([]*AlarmAudit, error) {
	cond, args := f.build("created_at")
	rows, err := r.q.QueryContext(ctx, `SELECT id, device_rustdesk_id, device_uuid, alarm_type, conn_id,
		conn_audit_ref, info, created_at FROM alarm_audits`+cond, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*AlarmAudit
	for rows.Next() {
		var (
			a         AlarmAudit
			uuid, ref sql.NullString
			connID    sql.NullInt64
			createdAt int64
		)
		if err := rows.Scan(&a.ID, &a.DeviceID, &uuid, &a.AlarmType, &connID, &ref, &a.Info, &createdAt); err != nil {
			return nil, mapErr(err)
		}
		a.DeviceUUID, a.ConnAuditRef, a.ConnID, a.CreatedAt = uuid.String, ref.String, ptrID(connID), fromMs(createdAt)
		out = append(out, &a)
	}
	return out, mapErr(rows.Err())
}

// jsonOrEmpty returns s when it is valid JSON, "{}" for "", and otherwise
// wraps the text as {"raw": s}.
func jsonOrEmpty(s string) string {
	if s == "" {
		return "{}"
	}
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(map[string]string{"raw": s})
	return string(b)
}

// AuditNonces de-duplicates client audit retries (clients >= 1.5.0).
type AuditNonces struct {
	q   db.Querier
	now func() time.Time
}

// Claim records nonce and reports true, or reports false when it was already
// seen (a retry of a stored record). Run it in the same transaction as the
// audit insert so a failed write releases the nonce.
func (r *AuditNonces) Claim(ctx context.Context, nonce string) (bool, error) {
	res, err := r.q.ExecContext(ctx, `INSERT INTO audit_nonces (nonce, created_at) VALUES (?, ?)
		ON CONFLICT (nonce) DO NOTHING`, nonce, toMs(r.now()))
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PurgeBefore deletes nonces recorded before cutoff.
func (r *AuditNonces) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM audit_nonces WHERE created_at < ?`, toMs(cutoff))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
