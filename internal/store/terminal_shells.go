package store

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
)

// MaxTerminalShells bounds the remembered terminal sessions per user and device.
const MaxTerminalShells = 10

// TerminalShell is a row of terminal_shells: a set of shells a device keeps
// running for a web client user (its RustDesk terminal service id).
type TerminalShell struct {
	ID         int64
	UserID     int64
	RustdeskID string
	// ServiceID is the plain service id (sealed at rest).
	ServiceID string
	Hostname  string
	Tabs      int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TerminalShells is the terminal_shells repository.
type TerminalShells struct {
	q    db.Querier
	now  func() time.Time
	aead *crypto.AEAD
}

func terminalShellAD(userID int64, rustdeskID string) string {
	return "terminal_shells:" + rustdeskID + ":" + strconv.FormatInt(userID, 10)
}

func serviceHash(id string) []byte {
	h := sha256.Sum256([]byte(id))
	return h[:]
}

// List returns the user's remembered sessions on a device, newest first.
func (r *TerminalShells) List(ctx context.Context, userID int64, rustdeskID string) ([]TerminalShell, error) {
	if r.aead == nil {
		return nil, ErrNoSecrets
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, service_enc, hostname, tabs, created_at, updated_at FROM terminal_shells
		WHERE user_id = ? AND rustdesk_id = ? ORDER BY updated_at DESC, id DESC`, userID, rustdeskID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []TerminalShell{}
	for rows.Next() {
		var (
			t                = TerminalShell{UserID: userID, RustdeskID: rustdeskID}
			enc              []byte
			created, updated int64
		)
		if err := rows.Scan(&t.ID, &enc, &t.Hostname, &t.Tabs, &created, &updated); err != nil {
			return nil, mapErr(err)
		}
		if t.ServiceID, err = r.aead.OpenString(enc, terminalShellAD(userID, rustdeskID)); err != nil {
			continue // a row sealed with another key: skip it
		}
		t.CreatedAt, t.UpdatedAt = fromMs(created), fromMs(updated)
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// Save inserts or updates (by service id) a remembered session and keeps the
// newest MaxTerminalShells of the user's on that device.
func (r *TerminalShells) Save(ctx context.Context, t *TerminalShell) error {
	if r.aead == nil {
		return ErrNoSecrets
	}
	now := r.now()
	enc, err := r.aead.SealString(t.ServiceID, terminalShellAD(t.UserID, t.RustdeskID))
	if err != nil {
		return err
	}
	if err := mapErr(r.q.QueryRowContext(ctx, `INSERT INTO terminal_shells (user_id, rustdesk_id, service_hash, service_enc,
		hostname, tabs, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, rustdesk_id, service_hash) DO UPDATE SET hostname = excluded.hostname, tabs = excluded.tabs,
		updated_at = excluded.updated_at RETURNING id`,
		t.UserID, t.RustdeskID, serviceHash(t.ServiceID), enc, t.Hostname, t.Tabs, toMs(now), toMs(now)).Scan(&t.ID)); err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `DELETE FROM terminal_shells WHERE user_id = ? AND rustdesk_id = ? AND id NOT IN (
		SELECT id FROM terminal_shells WHERE user_id = ? AND rustdesk_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?)`,
		t.UserID, t.RustdeskID, t.UserID, t.RustdeskID, MaxTerminalShells)
	return mapErr(err)
}

// Delete forgets a remembered session (no error when it is unknown).
func (r *TerminalShells) Delete(ctx context.Context, userID int64, rustdeskID, serviceID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM terminal_shells WHERE user_id = ? AND rustdesk_id = ? AND service_hash = ?`,
		userID, rustdeskID, serviceHash(serviceID))
	return mapErr(err)
}
