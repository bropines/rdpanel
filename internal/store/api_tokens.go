package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// APITokenScope limits what a personal API token may do.
type APITokenScope string

// API token scopes.
const (
	// APITokenRead allows only safe methods (GET, HEAD).
	APITokenRead APITokenScope = "read"
	// APITokenWrite has the full rights of the owning user.
	APITokenWrite APITokenScope = "write"
)

// MaxAPITokensPerUser bounds the non-revoked api_tokens rows of one user.
const MaxAPITokensPerUser = 50

// APIToken is a row of api_tokens (a personal token for the admin API). The
// plain token is never stored, only its sha256.
type APIToken struct {
	ID          int64
	UserID      int64
	Name        string
	Prefix      string
	TokenSHA256 []byte
	Scope       APITokenScope
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	LastUsedIP  string
	RevokedAt   *time.Time
}

// Expired reports whether t has an expiry at or before now.
func (t *APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !t.ExpiresAt.After(now)
}

// APITokens is the api_tokens repository.
type APITokens struct {
	q   db.Querier
	now func() time.Time
}

const apiTokenColumns = `id, user_id, name, prefix, token_sha256, scope, expires_at, created_at,
	last_used_at, last_used_ip, revoked_at`

func scanAPIToken(row interface{ Scan(...any) error }) (*APIToken, error) {
	var (
		t                          APIToken
		expires, lastUsed, revoked sql.NullInt64
		ip                         sql.NullString
		createdAt                  int64
	)
	if err := row.Scan(&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.TokenSHA256, &t.Scope, &expires, &createdAt,
		&lastUsed, &ip, &revoked); err != nil {
		return nil, mapErr(err)
	}
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = ptrMs(expires), ptrMs(lastUsed), ptrMs(revoked)
	t.CreatedAt, t.LastUsedIP = fromMs(createdAt), ip.String
	return &t, nil
}

// Create inserts t; ID and CreatedAt are set.
func (r *APITokens) Create(ctx context.Context, t *APIToken) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO api_tokens (user_id, name, prefix, token_sha256, scope,
		expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		t.UserID, t.Name, t.Prefix, t.TokenSHA256, t.Scope, nullMs(t.ExpiresAt), toMs(t.CreatedAt)).Scan(&t.ID))
}

// GetActive returns the non-revoked, non-expired token with digest hash;
// anything else yields ErrNotFound.
func (r *APITokens) GetActive(ctx context.Context, hash []byte) (*APIToken, error) {
	return scanAPIToken(r.q.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens
		WHERE token_sha256 = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		hash, toMs(r.now())))
}

// Get returns a token by id regardless of state.
func (r *APITokens) Get(ctx context.Context, id int64) (*APIToken, error) {
	return scanAPIToken(r.q.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE id = ?`, id))
}

// ListForUser returns the non-revoked tokens of a user (expired ones
// included), newest first.
func (r *APITokens) ListForUser(ctx context.Context, userID int64) ([]*APIToken, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens
		WHERE user_id = ? AND revoked_at IS NULL ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// CountForUser counts the non-revoked tokens of a user.
func (r *APITokens) CountForUser(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL`,
		userID).Scan(&n)
	return n, mapErr(err)
}

// Touch records a use of the token from ip.
func (r *APITokens) Touch(ctx context.Context, id int64, ip string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`,
		toMs(r.now()), nullStr(ip), id))
}

// Revoke marks a token revoked (idempotent); unknown ids return ErrNotFound.
func (r *APITokens) Revoke(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = coalesce(revoked_at, ?) WHERE id = ?`,
		toMs(r.now()), id))
}
