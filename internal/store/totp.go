package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// TOTPSecret is a row of totp_secrets.
type TOTPSecret struct {
	UserID int64
	// SecretEnc is AEAD(base32 seed) with crypto.TOTPSecretAD(UserID).
	SecretEnc []byte
	// ConfirmedAt is nil while enrolment is pending.
	ConfirmedAt  *time.Time
	LastUsedStep int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Confirmed reports whether enrolment finished (2FA active).
func (t *TOTPSecret) Confirmed() bool { return t != nil && t.ConfirmedAt != nil }

// TOTPSecrets is the totp_secrets repository.
type TOTPSecrets struct {
	q   db.Querier
	now func() time.Time
}

// Get returns the TOTP row of a user (ErrNotFound when none).
func (r *TOTPSecrets) Get(ctx context.Context, userID int64) (*TOTPSecret, error) {
	var (
		t                    TOTPSecret
		confirmed, lastStep  sql.NullInt64
		createdAt, updatedAt int64
	)
	err := r.q.QueryRowContext(ctx, `SELECT user_id, secret_enc, confirmed_at, last_used_step, created_at, updated_at
		FROM totp_secrets WHERE user_id = ?`, userID).
		Scan(&t.UserID, &t.SecretEnc, &confirmed, &lastStep, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	t.ConfirmedAt = ptrMs(confirmed)
	t.LastUsedStep = lastStep.Int64
	t.CreatedAt, t.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &t, nil
}

// StartEnrolment stores a new unconfirmed seed, replacing an unconfirmed one.
// It returns ErrConflict when 2FA is already confirmed for the user.
func (r *TOTPSecrets) StartEnrolment(ctx context.Context, userID int64, secretEnc []byte) error {
	now := toMs(r.now())
	res, err := r.q.ExecContext(ctx, `INSERT INTO totp_secrets (user_id, secret_enc, confirmed_at, last_used_step,
		created_at, updated_at) VALUES (?, ?, NULL, NULL, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, last_used_step = NULL,
		created_at = excluded.created_at, updated_at = excluded.updated_at
		WHERE totp_secrets.confirmed_at IS NULL`, userID, secretEnc, now, now)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	return nil
}

// Confirm activates a pending enrolment and records the step that confirmed it.
func (r *TOTPSecrets) Confirm(ctx context.Context, userID, step int64) error {
	now := toMs(r.now())
	return expectOne(r.q.ExecContext(ctx, `UPDATE totp_secrets SET confirmed_at = ?, last_used_step = ?,
		updated_at = ? WHERE user_id = ? AND confirmed_at IS NULL`, now, step, now, userID))
}

// UseStep records step as used if it is newer than the last used one
// (compare-and-set). ErrNotFound means the code was replayed concurrently.
func (r *TOTPSecrets) UseStep(ctx context.Context, userID, step int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE totp_secrets SET last_used_step = ?, updated_at = ?
		WHERE user_id = ? AND (last_used_step IS NULL OR last_used_step < ?)`, step, toMs(r.now()), userID, step))
}

// Delete removes a user's TOTP row (disable 2FA / admin reset).
func (r *TOTPSecrets) Delete(ctx context.Context, userID int64) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM totp_secrets WHERE user_id = ?`, userID)
	return mapErr(err)
}

// RecoveryCodes is the recovery_codes repository.
type RecoveryCodes struct {
	q   db.Querier
	now func() time.Time
}

// Replace deletes a user's codes and stores the given digests.
func (r *RecoveryCodes) Replace(ctx context.Context, userID int64, hashes [][]byte) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return mapErr(err)
	}
	now := toMs(r.now())
	for _, h := range hashes {
		if _, err := r.q.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_sha256, used_at, created_at)
			VALUES (?, ?, NULL, ?)`, userID, h, now); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// Use marks the unused code with digest hash as used. ErrNotFound when no
// such unused code exists (wrong or already used).
func (r *RecoveryCodes) Use(ctx context.Context, userID int64, hash []byte) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ?
		WHERE user_id = ? AND code_sha256 = ? AND used_at IS NULL`, toMs(r.now()), userID, hash))
}

// CountUnused returns the number of remaining codes.
func (r *RecoveryCodes) CountUnused(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`,
		userID).Scan(&n)
	return n, mapErr(err)
}

// DeleteAll removes all codes of a user.
func (r *RecoveryCodes) DeleteAll(ctx context.Context, userID int64) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return mapErr(err)
}
