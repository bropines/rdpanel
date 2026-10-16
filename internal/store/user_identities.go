package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// UserIdentity is a row of user_identities: a local user linked to an IdP subject.
type UserIdentity struct {
	ID            int64
	UserID        int64
	ProviderID    int64
	Subject       string
	Email         string
	EmailVerified bool
	DisplayName   string
	Username      string
	AvatarURL     string
	LegacyUnionID string
	LastLoginAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UserIdentities is the user_identities repository.
type UserIdentities struct {
	q   db.Querier
	now func() time.Time
}

const userIdentityColumns = `id, user_id, provider_id, subject, email, email_verified, display_name, username,
	avatar_url, legacy_union_id, last_login_at, created_at, updated_at`

func scanUserIdentity(row interface{ Scan(...any) error }) (*UserIdentity, error) {
	var (
		u                    UserIdentity
		email, union         sql.NullString
		verified             int
		lastLogin            sql.NullInt64
		createdAt, updatedAt int64
	)
	err := row.Scan(&u.ID, &u.UserID, &u.ProviderID, &u.Subject, &email, &verified, &u.DisplayName, &u.Username,
		&u.AvatarURL, &union, &lastLogin, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Email, u.LegacyUnionID, u.EmailVerified = email.String, union.String, verified == 1
	u.LastLoginAt = ptrMs(lastLogin)
	u.CreatedAt, u.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &u, nil
}

// Create inserts u; ErrConflict when (provider, subject) is already linked.
func (r *UserIdentities) Create(ctx context.Context, u *UserIdentity) error {
	now := r.now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO user_identities (user_id, provider_id, subject, email,
		email_verified, display_name, username, avatar_url, legacy_union_id, last_login_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		u.UserID, u.ProviderID, u.Subject, nullStr(strings.ToLower(u.Email)), boolInt(u.EmailVerified),
		u.DisplayName, u.Username, u.AvatarURL, nullStr(u.LegacyUnionID), nullMs(u.LastLoginAt),
		toMs(u.CreatedAt), toMs(u.UpdatedAt)).Scan(&u.ID))
}

// GetByProviderSubject returns the identity for (providerID, subject).
func (r *UserIdentities) GetByProviderSubject(ctx context.Context, providerID int64, subject string) (*UserIdentity, error) {
	return scanUserIdentity(r.q.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities
		WHERE provider_id = ? AND subject = ?`, providerID, subject))
}

// Get returns an identity by id.
func (r *UserIdentities) Get(ctx context.Context, id int64) (*UserIdentity, error) {
	return scanUserIdentity(r.q.QueryRowContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities
		WHERE id = ?`, id))
}

// ListForUser returns a user's identities ordered by id.
func (r *UserIdentities) ListForUser(ctx context.Context, userID int64) ([]*UserIdentity, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+userIdentityColumns+` FROM user_identities
		WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*UserIdentity
	for rows.Next() {
		u, err := scanUserIdentity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, mapErr(rows.Err())
}

// CountForUser returns how many identities a user has.
func (r *UserIdentities) CountForUser(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM user_identities WHERE user_id = ?`, userID).Scan(&n)
	return n, mapErr(err)
}

// RecordLogin refreshes the IdP profile snapshot and last_login_at.
func (r *UserIdentities) RecordLogin(ctx context.Context, u *UserIdentity) error {
	now := r.now()
	u.LastLoginAt, u.UpdatedAt = &now, now
	return expectOne(r.q.ExecContext(ctx, `UPDATE user_identities SET email = ?, email_verified = ?,
		display_name = ?, username = ?, avatar_url = ?, last_login_at = ?, updated_at = ? WHERE id = ?`,
		nullStr(strings.ToLower(u.Email)), boolInt(u.EmailVerified), u.DisplayName, u.Username, u.AvatarURL,
		toMs(now), toMs(now), u.ID))
}

// DeleteForUser removes identity id only if it belongs to userID.
func (r *UserIdentities) DeleteForUser(ctx context.Context, userID, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM user_identities WHERE id = ? AND user_id = ?`, id, userID))
}

// ListByEmail returns users whose email equals email (case-insensitive).
// Used for OIDC linking by verified email; callers must refuse ambiguous matches.
func (r *Users) ListByEmail(ctx context.Context, email string) ([]*User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ? ORDER BY id LIMIT 10`, email)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, mapErr(rows.Err())
}
