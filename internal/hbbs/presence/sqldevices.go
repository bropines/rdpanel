package presence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// SQLDevices is a minimal DeviceLister / DeviceRefLister / LastSeenWriter on
// the panel's devices table. (The full devices repository lives in
// internal/store; this adapter only needs three columns.)
type SQLDevices struct {
	DB *db.DB
}

var (
	_ DeviceLister    = SQLDevices{}
	_ DeviceRefLister = SQLDevices{}
	_ LastSeenWriter  = SQLDevices{}
)

// ListRustdeskIDs implements DeviceLister.
func (s SQLDevices) ListRustdeskIDs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT rustdesk_id FROM devices ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("presence: list devices: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListDeviceRefs implements DeviceRefLister.
func (s SQLDevices) ListDeviceRefs(ctx context.Context) ([]DeviceRef, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT rustdesk_id, owner_user_id, last_heartbeat_at FROM devices ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("presence: list devices: %w", err)
	}
	defer rows.Close()
	var out []DeviceRef
	for rows.Next() {
		var (
			r     DeviceRef
			owner sql.NullInt64
			hb    sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &owner, &hb); err != nil {
			return nil, err
		}
		if owner.Valid {
			v := owner.Int64
			r.OwnerUserID = &v
		}
		if hb.Valid {
			r.LastHeartbeatAt = time.UnixMilli(hb.Int64)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TouchLastSeen implements LastSeenWriter. last_seen_at only moves forward
// (heartbeats may write it too); updated_at is not touched because presence
// is not an edit of the device.
func (s SQLDevices) TouchLastSeen(ctx context.Context, seen map[string]time.Time) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		q := s.DB.Wrap(tx)
		for id, ts := range seen {
			ms := ts.UnixMilli()
			if _, err := q.ExecContext(ctx, `UPDATE devices SET last_seen_at = CASE
				WHEN coalesce(last_seen_at, 0) > ? THEN coalesce(last_seen_at, 0) ELSE ? END
				WHERE rustdesk_id = ?`, ms, ms, id); err != nil {
				return fmt.Errorf("presence: touch %s: %w", id, err)
			}
		}
		return nil
	})
}
