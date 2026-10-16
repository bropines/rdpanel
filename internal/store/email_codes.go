package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// Email code purposes (email_codes.purpose).
const (
	EmailCodeVerify        = "verify_email"
	EmailCodeLogin         = "login"
	EmailCodePasswordReset = "password_reset"
)

// EmailCode is a row of email_codes: a one-time code or reset token sent by
// e-mail. Only its sha256 is stored.
type EmailCode struct {
	ID         int64
	UserID     int64
	Purpose    string
	Email      string
	CodeSHA256 []byte
	Attempts   int
	IP         string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

// EmailCodes is the email_codes repository.
type EmailCodes struct {
	q   db.Querier
	now func() time.Time
}

// EmailCodes returns the email_codes repository bound to s.
func (s *Store) EmailCodes() *EmailCodes { return &EmailCodes{q: s.q, now: s.opts.now} }

const emailCodeColumns = `id, user_id, purpose, email, code_sha256, attempts, ip, created_at, expires_at, consumed_at`

func scanEmailCode(row interface{ Scan(...any) error }) (*EmailCode, error) {
	var (
		c            EmailCode
		ip           sql.NullString
		created, exp int64
		consumed     sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.UserID, &c.Purpose, &c.Email, &c.CodeSHA256, &c.Attempts, &ip, &created, &exp,
		&consumed); err != nil {
		return nil, mapErr(err)
	}
	c.IP, c.CreatedAt, c.ExpiresAt, c.ConsumedAt = ip.String, fromMs(created), fromMs(exp), ptrMs(consumed)
	return &c, nil
}

// Create invalidates the user's unconsumed codes of the same purpose and
// inserts c (ID and CreatedAt are set). Run it in a transaction.
func (r *EmailCodes) Create(ctx context.Context, c *EmailCode) error {
	now := r.now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if _, err := r.q.ExecContext(ctx, `UPDATE email_codes SET consumed_at = ? WHERE user_id = ? AND purpose = ?
		AND consumed_at IS NULL`, toMs(now), c.UserID, c.Purpose); err != nil {
		return mapErr(err)
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO email_codes (user_id, purpose, email, code_sha256, attempts,
		ip, created_at, expires_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?) RETURNING id`,
		c.UserID, c.Purpose, c.Email, c.CodeSHA256, nullStr(c.IP), toMs(c.CreatedAt), toMs(c.ExpiresAt)).Scan(&c.ID))
}

// Latest returns the newest code of the user for purpose in any state
// (resend cooldown); ErrNotFound when none.
func (r *EmailCodes) Latest(ctx context.Context, userID int64, purpose string) (*EmailCode, error) {
	return scanEmailCode(r.q.QueryRowContext(ctx, `SELECT `+emailCodeColumns+` FROM email_codes
		WHERE user_id = ? AND purpose = ? ORDER BY created_at DESC, id DESC LIMIT 1`, userID, purpose))
}

// Active returns the newest unconsumed, unexpired code of the user for
// purpose; ErrNotFound when none.
func (r *EmailCodes) Active(ctx context.Context, userID int64, purpose string) (*EmailCode, error) {
	return scanEmailCode(r.q.QueryRowContext(ctx, `SELECT `+emailCodeColumns+` FROM email_codes
		WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, userID, purpose, toMs(r.now())))
}

// ActiveByHash returns the unconsumed, unexpired code with digest sum for
// purpose (reset tokens); ErrNotFound when none.
func (r *EmailCodes) ActiveByHash(ctx context.Context, purpose string, sum []byte) (*EmailCode, error) {
	return scanEmailCode(r.q.QueryRowContext(ctx, `SELECT `+emailCodeColumns+` FROM email_codes
		WHERE code_sha256 = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?
		ORDER BY id DESC LIMIT 1`, sum, purpose, toMs(r.now())))
}

// Consume marks code id used; ErrNotFound when it was already consumed
// (a concurrent use won).
func (r *EmailCodes) Consume(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE email_codes SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		toMs(r.now()), id))
}

// ConsumeAll invalidates every unconsumed code of the user for purpose.
func (r *EmailCodes) ConsumeAll(ctx context.Context, userID int64, purpose string) error {
	_, err := r.q.ExecContext(ctx, `UPDATE email_codes SET consumed_at = ? WHERE user_id = ? AND purpose = ?
		AND consumed_at IS NULL`, toMs(r.now()), userID, purpose)
	return mapErr(err)
}

// Fail records a wrong guess against code id and invalidates it once
// maxAttempts wrong guesses were made. It returns the remaining attempts.
func (r *EmailCodes) Fail(ctx context.Context, id int64, maxAttempts int) (int, error) {
	var attempts int
	err := r.q.QueryRowContext(ctx, `UPDATE email_codes SET attempts = attempts + 1,
		consumed_at = CASE WHEN attempts + 1 >= ? THEN ? ELSE consumed_at END
		WHERE id = ? RETURNING attempts`, maxAttempts, toMs(r.now()), id).Scan(&attempts)
	if err != nil {
		return 0, mapErr(err)
	}
	return max(maxAttempts-attempts, 0), nil
}

// CountForUserSince counts codes sent to the user (any purpose) since t.
func (r *EmailCodes) CountForUserSince(ctx context.Context, userID int64, t time.Time) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM email_codes WHERE user_id = ? AND created_at >= ?`,
		userID, toMs(t)).Scan(&n)
	return n, mapErr(err)
}

// CountForIPSince counts codes requested from ip (any user) since t.
func (r *EmailCodes) CountForIPSince(ctx context.Context, ip string, t time.Time) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM email_codes WHERE ip = ? AND created_at >= ?`,
		ip, toMs(t)).Scan(&n)
	return n, mapErr(err)
}

// PurgeBefore deletes codes that expired before t.
func (r *EmailCodes) PurgeBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := r.q.ExecContext(ctx, `DELETE FROM email_codes WHERE expires_at < ?`, toMs(t))
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
