// Package loginlog is a minimal writer for the login_events table used by the
// panel login and the OIDC flows (stream B1). It is intentionally tiny so it
// can be replaced by a full login_events repository in internal/store later:
// only Record and Event are used by callers.
package loginlog

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Method values of login_events.method.
const (
	MethodPassword = "password"
	MethodOIDC     = "oidc"
	MethodWebAuth  = "webauth"
)

// Client kinds (login_events.client_kind).
const (
	ClientWebAdmin = "webadmin"
	ClientClient   = "client"
)

// Event is one login attempt. Never put secrets in FailureReason.
type Event struct {
	UserID            *int64
	UsernameAttempted string
	TokenID           *int64
	Method            string
	Success           bool
	FailureReason     string
	ClientKind        string
	DeviceRustdeskID  string
	DeviceUUID        string
	Platform          string
	IP                string
	UserAgent         string
	At                time.Time
}

// Recorder writes login events.
type Recorder interface {
	Record(ctx context.Context, e Event) error
}

// DB records events with plain SQL on q.
type DB struct {
	Q   db.Querier
	Now func() time.Time
}

func ns(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func nid(id *int64) sql.NullInt64 {
	if id == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *id, Valid: true}
}

// maxField bounds free-text columns (user agent, attempted username).
const maxField = 512

func clip(s string) string {
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

// Record implements Recorder.
func (d DB) Record(ctx context.Context, e Event) error {
	at := e.At
	if at.IsZero() {
		if d.Now != nil {
			at = d.Now()
		} else {
			at = time.Now()
		}
	}
	result := "failure"
	if e.Success {
		result = "success"
	}
	_, err := d.Q.ExecContext(ctx, `INSERT INTO login_events (user_id, username_attempted, token_id, method, result,
		failure_reason, client_kind, device_rustdesk_id, device_uuid, platform, ip, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nid(e.UserID), ns(clip(e.UsernameAttempted)), nid(e.TokenID), e.Method, result, ns(e.FailureReason),
		ns(e.ClientKind), ns(e.DeviceRustdeskID), ns(e.DeviceUUID), ns(e.Platform), ns(e.IP), ns(clip(e.UserAgent)),
		at.UnixMilli())
	return err
}
