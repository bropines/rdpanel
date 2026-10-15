package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Login methods and results (login_events.method / .result).
const (
	LoginMethodPassword = "password"
	LoginMethodOIDC     = "oidc"
	LoginMethodWebAuth  = "webauth"

	LoginSuccess = "success"
	LoginFailure = "failure"
)

// LoginEvent is a row of login_events.
type LoginEvent struct {
	ID                int64
	UserID            *int64
	UsernameAttempted string
	TokenID           *int64
	Method            string
	Result            string
	FailureReason     string
	// ClientKind: webadmin | client | webclient | RustDesk deviceInfo.type.
	ClientKind       string
	DeviceRustdeskID string
	DeviceUUID       string
	Platform         string
	IP               string
	UserAgent        string
	HiddenByUser     bool
	CreatedAt        time.Time
}

// LoginEvents is the login_events repository.
type LoginEvents struct {
	q   db.Querier
	now func() time.Time
}

// Record inserts e; ID and CreatedAt are set. An empty Result is "success".
func (r *LoginEvents) Record(ctx context.Context, e *LoginEvent) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = r.now()
	}
	if e.Result == "" {
		e.Result = LoginSuccess
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO login_events (user_id, username_attempted, token_id, method,
		result, failure_reason, client_kind, device_rustdesk_id, device_uuid, platform, ip, user_agent,
		hidden_by_user, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		nullID(e.UserID), nullStr(e.UsernameAttempted), nullID(e.TokenID), e.Method, e.Result,
		nullStr(e.FailureReason), nullStr(e.ClientKind), nullStr(e.DeviceRustdeskID), nullStr(e.DeviceUUID),
		nullStr(e.Platform), nullStr(e.IP), nullStr(e.UserAgent), boolInt(e.HiddenByUser),
		toMs(e.CreatedAt)).Scan(&e.ID))
}

// LoginEventFilter selects login events (newest first, keyset paging).
type LoginEventFilter struct {
	UserID        *int64
	Result        string
	IncludeHidden bool
	BeforeID      int64
	Limit         int
}

// List returns login events matching f.
func (r *LoginEvents) List(ctx context.Context, f LoginEventFilter) ([]*LoginEvent, error) {
	var (
		where []string
		args  []any
	)
	if f.UserID != nil {
		where, args = append(where, "user_id = ?"), append(args, *f.UserID)
	}
	if f.Result != "" {
		where, args = append(where, "result = ?"), append(args, f.Result)
	}
	if !f.IncludeHidden {
		where = append(where, "hidden_by_user = 0")
	}
	if f.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, f.BeforeID)
	}
	q := `SELECT id, user_id, username_attempted, token_id, method, result, failure_reason, client_kind,
		device_rustdesk_id, device_uuid, platform, ip, user_agent, hidden_by_user, created_at FROM login_events`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := r.q.QueryContext(ctx, q+" ORDER BY id DESC LIMIT ?", append(args, clampLimit(f.Limit, 100, 1000))...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*LoginEvent
	for rows.Next() {
		var (
			e                                       LoginEvent
			userID, tokenID                         sql.NullInt64
			attempted, reason, kind, devID, devUUID sql.NullString
			platform, ip, ua                        sql.NullString
			hidden                                  int
			createdAt                               int64
		)
		if err := rows.Scan(&e.ID, &userID, &attempted, &tokenID, &e.Method, &e.Result, &reason, &kind, &devID,
			&devUUID, &platform, &ip, &ua, &hidden, &createdAt); err != nil {
			return nil, mapErr(err)
		}
		e.UserID, e.TokenID = ptrID(userID), ptrID(tokenID)
		e.UsernameAttempted, e.FailureReason, e.ClientKind = attempted.String, reason.String, kind.String
		e.DeviceRustdeskID, e.DeviceUUID, e.Platform = devID.String, devUUID.String, platform.String
		e.IP, e.UserAgent, e.HiddenByUser = ip.String, ua.String, hidden == 1
		e.CreatedAt = fromMs(createdAt)
		out = append(out, &e)
	}
	return out, mapErr(rows.Err())
}

// Hide marks an event hidden for its user (legacy soft delete).
func (r *LoginEvents) Hide(ctx context.Context, id, userID int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE login_events SET hidden_by_user = 1 WHERE id = ? AND user_id = ?`,
		id, userID))
}

// PurgeBefore deletes events older than cutoff (retention).
func (r *LoginEvents) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM login_events WHERE created_at < ?`, toMs(cutoff))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
