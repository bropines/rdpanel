package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// AuditEntry is a row of admin_audit.
type AuditEntry struct {
	ID          int64
	ActorUserID *int64
	// ActorName is a snapshot ("cli" for command-line actions).
	ActorName  string
	ActorIP    string
	Action     string
	TargetType string
	TargetID   string
	// Details must never contain secrets.
	Details   map[string]any
	CreatedAt time.Time
}

// AdminAudit is the admin_audit repository.
type AdminAudit struct {
	q   db.Querier
	now func() time.Time
}

// Record appends e; ID and CreatedAt are set.
func (r *AdminAudit) Record(ctx context.Context, e *AuditEntry) error {
	details := []byte("{}")
	if len(e.Details) > 0 {
		var err error
		if details, err = json.Marshal(e.Details); err != nil {
			return fmt.Errorf("store: audit details: %w", err)
		}
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO admin_audit (actor_user_id, actor_name, actor_ip, action,
		target_type, target_id, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		nullID(e.ActorUserID), e.ActorName, nullStr(e.ActorIP), e.Action, nullStr(e.TargetType),
		nullStr(e.TargetID), string(details), toMs(e.CreatedAt)).Scan(&e.ID))
}

// AuditFilter selects audit entries. Results are newest first; page with
// BeforeID (keyset pagination: pass the last ID of the previous page).
type AuditFilter struct {
	ActorUserID *int64
	// ActionPrefix matches e.g. "user." for all user actions.
	ActionPrefix string
	TargetType   string
	TargetID     string
	Since        *time.Time
	Until        *time.Time
	BeforeID     int64
	Limit        int
}

// List returns entries matching f.
func (r *AdminAudit) List(ctx context.Context, f AuditFilter) ([]*AuditEntry, error) {
	var (
		where []string
		args  []any
	)
	if f.ActorUserID != nil {
		where, args = append(where, "actor_user_id = ?"), append(args, *f.ActorUserID)
	}
	if f.ActionPrefix != "" {
		where, args = append(where, `action LIKE ? ESCAPE '\'`), append(args, escapeLike(f.ActionPrefix)+"%")
	}
	if f.TargetType != "" {
		where, args = append(where, "target_type = ?"), append(args, f.TargetType)
	}
	if f.TargetID != "" {
		where, args = append(where, "target_id = ?"), append(args, f.TargetID)
	}
	if f.Since != nil {
		where, args = append(where, "created_at >= ?"), append(args, toMs(*f.Since))
	}
	if f.Until != nil {
		where, args = append(where, "created_at < ?"), append(args, toMs(*f.Until))
	}
	if f.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, f.BeforeID)
	}
	q := `SELECT id, actor_user_id, actor_name, actor_ip, action, target_type, target_id, details, created_at
		FROM admin_audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC LIMIT ?"
	rows, err := r.q.QueryContext(ctx, q, append(args, clampLimit(f.Limit, 100, 1000))...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*AuditEntry
	for rows.Next() {
		var (
			e              AuditEntry
			actor          sql.NullInt64
			ip, ttype, tid sql.NullString
			details        string
			createdAt      int64
		)
		if err := rows.Scan(&e.ID, &actor, &e.ActorName, &ip, &e.Action, &ttype, &tid, &details, &createdAt); err != nil {
			return nil, mapErr(err)
		}
		e.ActorUserID = ptrID(actor)
		e.ActorIP, e.TargetType, e.TargetID = ip.String, ttype.String, tid.String
		e.CreatedAt = fromMs(createdAt)
		if err := json.Unmarshal([]byte(details), &e.Details); err != nil {
			e.Details = map[string]any{"_raw": details}
		}
		out = append(out, &e)
	}
	return out, mapErr(rows.Err())
}

// PurgeBefore deletes entries older than cutoff (retention).
func (r *AdminAudit) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM admin_audit WHERE created_at < ?`, toMs(cutoff))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
