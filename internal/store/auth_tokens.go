package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// TokenKind distinguishes client tokens from panel sessions.
type TokenKind string

// Token kinds.
const (
	// TokenClient is a RustDesk client JWT (Authorization: Bearer).
	TokenClient TokenKind = "client"
	// TokenWeb is a panel session (opaque cookie token).
	TokenWeb TokenKind = "web"
)

// AuthToken is a row of auth_tokens. The plain token is never stored.
type AuthToken struct {
	ID          int64
	UserID      int64
	Kind        TokenKind
	TokenSHA256 []byte
	// JWTExpiresAt is the fixed JWT exp (client tokens only).
	JWTExpiresAt *time.Time
	// ExpiresAt is the sliding (client) or idle (web) expiry.
	ExpiresAt time.Time
	// AbsoluteExpiresAt caps web sessions regardless of activity.
	AbsoluteExpiresAt *time.Time
	DeviceRustdeskID  string
	DeviceUUID        string
	ClientPlatform    string
	UserAgent         string
	IP                string
	MFAPending        bool
	CreatedAt         time.Time
	LastUsedAt        *time.Time
	RevokedAt         *time.Time
	RevokeReason      string
	LegacyID          *int64
}

// AuthTokens is the auth_tokens repository.
type AuthTokens struct {
	q   db.Querier
	now func() time.Time
}

const authTokenColumns = `id, user_id, kind, token_sha256, jwt_expires_at, expires_at, absolute_expires_at,
	device_rustdesk_id, device_uuid, client_platform, user_agent, ip, mfa_pending, created_at, last_used_at,
	revoked_at, revoke_reason, legacy_id`

func scanAuthToken(row interface{ Scan(...any) error }) (*AuthToken, error) {
	var (
		t                                    AuthToken
		jwtExp, absExp, lastUsed, revokedAt  sql.NullInt64
		legacyID                             sql.NullInt64
		devID, devUUID, platform, ua, ip, rr sql.NullString
		mfa                                  int
		expiresAt, createdAt                 int64
	)
	err := row.Scan(&t.ID, &t.UserID, &t.Kind, &t.TokenSHA256, &jwtExp, &expiresAt, &absExp,
		&devID, &devUUID, &platform, &ua, &ip, &mfa, &createdAt, &lastUsed, &revokedAt, &rr, &legacyID)
	if err != nil {
		return nil, mapErr(err)
	}
	t.JWTExpiresAt, t.AbsoluteExpiresAt = ptrMs(jwtExp), ptrMs(absExp)
	t.ExpiresAt, t.CreatedAt = fromMs(expiresAt), fromMs(createdAt)
	t.LastUsedAt, t.RevokedAt = ptrMs(lastUsed), ptrMs(revokedAt)
	t.DeviceRustdeskID, t.DeviceUUID, t.ClientPlatform = devID.String, devUUID.String, platform.String
	t.UserAgent, t.IP, t.RevokeReason = ua.String, ip.String, rr.String
	t.MFAPending = mfa == 1
	t.LegacyID = ptrID(legacyID)
	return &t, nil
}

// Create inserts t; ID and CreatedAt are set.
func (r *AuthTokens) Create(ctx context.Context, t *AuthToken) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, jwt_expires_at,
		expires_at, absolute_expires_at, device_rustdesk_id, device_uuid, client_platform, user_agent, ip,
		mfa_pending, created_at, last_used_at, revoked_at, revoke_reason, legacy_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		t.UserID, t.Kind, t.TokenSHA256, nullMs(t.JWTExpiresAt), toMs(t.ExpiresAt), nullMs(t.AbsoluteExpiresAt),
		nullStr(t.DeviceRustdeskID), nullStr(t.DeviceUUID), nullStr(t.ClientPlatform), nullStr(t.UserAgent),
		nullStr(t.IP), boolInt(t.MFAPending), toMs(t.CreatedAt), nullMs(t.LastUsedAt), nullMs(t.RevokedAt),
		nullStr(t.RevokeReason), nullID(t.LegacyID)).Scan(&t.ID))
}

// GetActive returns the non-revoked, non-expired token of the given kind with
// digest hash. Expired, revoked or unknown tokens yield ErrNotFound.
func (r *AuthTokens) GetActive(ctx context.Context, kind TokenKind, hash []byte) (*AuthToken, error) {
	now := toMs(r.now())
	return scanAuthToken(r.q.QueryRowContext(ctx, `SELECT `+authTokenColumns+` FROM auth_tokens
		WHERE token_sha256 = ? AND kind = ? AND revoked_at IS NULL AND expires_at > ?
		  AND (absolute_expires_at IS NULL OR absolute_expires_at > ?)
		  AND (jwt_expires_at IS NULL OR jwt_expires_at > ?)`,
		hash, kind, now, now, now))
}

// Get returns a token by id regardless of state.
func (r *AuthTokens) Get(ctx context.Context, id int64) (*AuthToken, error) {
	return scanAuthToken(r.q.QueryRowContext(ctx, `SELECT `+authTokenColumns+` FROM auth_tokens WHERE id = ?`, id))
}

// Touch records use of a token and, when newExpiry is non-nil, slides its
// expiry (never beyond absolute_expires_at for web sessions).
func (r *AuthTokens) Touch(ctx context.Context, id int64, newExpiry *time.Time) error {
	now := toMs(r.now())
	if newExpiry == nil {
		return expectOne(r.q.ExecContext(ctx, `UPDATE auth_tokens SET last_used_at = ? WHERE id = ?`, now, id))
	}
	return expectOne(r.q.ExecContext(ctx, `UPDATE auth_tokens SET last_used_at = ?,
		expires_at = CASE WHEN absolute_expires_at IS NOT NULL AND absolute_expires_at < ? THEN absolute_expires_at ELSE ? END
		WHERE id = ?`, now, toMs(*newExpiry), toMs(*newExpiry), id))
}

// SetMFAPending flips the pending-second-factor flag of a web session.
func (r *AuthTokens) SetMFAPending(ctx context.Context, id int64, pending bool) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE auth_tokens SET mfa_pending = ? WHERE id = ?`, boolInt(pending), id))
}

// Revoke marks one token revoked. Revoking an already revoked token is a no-op
// that still returns nil; unknown ids return ErrNotFound.
func (r *AuthTokens) Revoke(ctx context.Context, id int64, reason string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE auth_tokens SET revoked_at = coalesce(revoked_at, ?),
		revoke_reason = coalesce(revoke_reason, ?) WHERE id = ?`, toMs(r.now()), nullStr(reason), id))
}

// RevokeForUser revokes all active tokens of a user (of kind, or all kinds when
// kind is "") and returns how many were revoked.
func (r *AuthTokens) RevokeForUser(ctx context.Context, userID int64, kind TokenKind, reason string) (int64, error) {
	res, err := r.q.ExecContext(ctx, `UPDATE auth_tokens SET revoked_at = ?, revoke_reason = ?
		WHERE user_id = ? AND revoked_at IS NULL AND (? = '' OR kind = ?)`,
		toMs(r.now()), nullStr(reason), userID, kind, kind)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// RevokeForDevice revokes all active client tokens bound to a device uuid
// (device deletion in the panel).
func (r *AuthTokens) RevokeForDevice(ctx context.Context, deviceUUID string, reason string) (int64, error) {
	if deviceUUID == "" {
		return 0, nil
	}
	res, err := r.q.ExecContext(ctx, `UPDATE auth_tokens SET revoked_at = ?, revoke_reason = ?
		WHERE device_uuid = ? AND kind = 'client' AND revoked_at IS NULL`, toMs(r.now()), nullStr(reason), deviceUUID)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// ListActiveForUser returns a user's active tokens, newest first (kind "" = all).
func (r *AuthTokens) ListActiveForUser(ctx context.Context, userID int64, kind TokenKind) ([]*AuthToken, error) {
	now := toMs(r.now())
	rows, err := r.q.QueryContext(ctx, `SELECT `+authTokenColumns+` FROM auth_tokens
		WHERE user_id = ? AND (? = '' OR kind = ?) AND revoked_at IS NULL AND expires_at > ?
		  AND (absolute_expires_at IS NULL OR absolute_expires_at > ?)
		ORDER BY created_at DESC, id DESC`, userID, kind, kind, now, now)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*AuthToken
	for rows.Next() {
		t, err := scanAuthToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// PurgeInactive deletes tokens that expired or were revoked before cutoff and
// returns the number deleted (janitor).
func (r *AuthTokens) PurgeInactive(ctx context.Context, cutoff time.Time) (int64, error) {
	c := toMs(cutoff)
	res, err := r.q.ExecContext(ctx, `DELETE FROM auth_tokens
		WHERE (revoked_at IS NOT NULL AND revoked_at < ?)
		   OR expires_at < ?
		   OR (absolute_expires_at IS NOT NULL AND absolute_expires_at < ?)
		   OR (jwt_expires_at IS NOT NULL AND jwt_expires_at < ?)`, c, c, c, c)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}
