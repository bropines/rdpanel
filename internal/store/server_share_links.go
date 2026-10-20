package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// ShareLinkStatus is the effective state of a server share link.
type ShareLinkStatus string

// Server share link states.
const (
	ShareLinkActive  ShareLinkStatus = "active"
	ShareLinkExpired ShareLinkStatus = "expired"
	// ShareLinkExhausted: the installer download limit is reached.
	ShareLinkExhausted ShareLinkStatus = "exhausted"
	ShareLinkRevoked   ShareLinkStatus = "revoked"
	// ShareLinkSuspended: the owner is disabled or may no longer share
	// (neither an admin nor in a user group with can_share_links).
	ShareLinkSuspended ShareLinkStatus = "suspended"
)

// MaxServerShareLinksPerUser bounds the non-revoked, non-expired links of
// one user.
const MaxServerShareLinksPerUser = 100

// ServerShareLink is a row of server_share_links: a public link (/s/<token>)
// to this server's connection settings and preconfigured client. The token
// is stored as its sha256 (lookups) and sealed with the panel AEAD
// (TokenEnc, so the creator can copy the link again).
type ServerShareLink struct {
	ID     int64
	UserID int64
	// Username and DisplayName are the owner's (read only).
	Username    string
	DisplayName string
	// OwnerAllowed reports whether the owner is active and may share (an
	// admin or in a user group with can_share_links); read only.
	OwnerAllowed bool
	Label        string
	TokenSHA256  []byte
	TokenEnc     []byte
	// ExpiresAt nil: permanent until revoked.
	ExpiresAt *time.Time
	// MaxDownloads nil: unlimited installer downloads.
	MaxDownloads *int
	Views        int64
	Downloads    int64
	LastUsedAt   *time.Time
	CreatedAt    time.Time
	RevokedAt    *time.Time
}

// Status returns the link's state at now. Revoked wins over expired, which
// wins over exhausted, which wins over suspended.
func (l *ServerShareLink) Status(now time.Time) ShareLinkStatus {
	switch {
	case l.RevokedAt != nil:
		return ShareLinkRevoked
	case l.ExpiresAt != nil && !l.ExpiresAt.After(now):
		return ShareLinkExpired
	case l.MaxDownloads != nil && l.Downloads >= int64(*l.MaxDownloads):
		return ShareLinkExhausted
	case !l.OwnerAllowed:
		return ShareLinkSuspended
	}
	return ShareLinkActive
}

// ServerShareLinks is the server_share_links repository.
type ServerShareLinks struct {
	q   db.Querier
	now func() time.Time
}

// ownerAllowedExpr is 1 when the user u may share (alias g: their group).
const ownerAllowedExpr = `CASE WHEN u.status = 'active' AND (u.role = 'admin' OR coalesce(g.can_share_links, 0) = 1)
	THEN 1 ELSE 0 END`

const serverShareLinkSelect = `SELECT l.id, l.user_id, u.username, u.display_name, ` + ownerAllowedExpr + `, l.label,
	l.token_sha256, l.token_enc, l.expires_at, l.max_downloads, l.views, l.downloads, l.last_used_at,
	l.created_at, l.revoked_at
	FROM server_share_links l JOIN users u ON u.id = l.user_id LEFT JOIN user_groups g ON g.id = u.user_group_id`

func scanServerShareLink(row interface{ Scan(...any) error }) (*ServerShareLink, error) {
	var (
		l                          ServerShareLink
		allowed, createdAt         int64
		expires, maxDL, used, revk sql.NullInt64
	)
	if err := row.Scan(&l.ID, &l.UserID, &l.Username, &l.DisplayName, &allowed, &l.Label, &l.TokenSHA256, &l.TokenEnc, &expires,
		&maxDL, &l.Views, &l.Downloads, &used, &createdAt, &revk); err != nil {
		return nil, mapErr(err)
	}
	l.OwnerAllowed = allowed == 1
	l.ExpiresAt, l.LastUsedAt, l.RevokedAt = ptrMs(expires), ptrMs(used), ptrMs(revk)
	l.CreatedAt = fromMs(createdAt)
	if maxDL.Valid {
		n := int(maxDL.Int64)
		l.MaxDownloads = &n
	}
	return &l, nil
}

// Create inserts l (UserID, Label, TokenSHA256, ExpiresAt, MaxDownloads);
// ID and CreatedAt are set. TokenEnc is written with SetTokenEnc once the id
// is known (its AD contains the id).
func (r *ServerShareLinks) Create(ctx context.Context, l *ServerShareLink) error {
	if l.CreatedAt.IsZero() {
		l.CreatedAt = r.now()
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO server_share_links (user_id, label, token_sha256,
		expires_at, max_downloads, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		l.UserID, l.Label, l.TokenSHA256, nullMs(l.ExpiresAt), nullInt(l.MaxDownloads), toMs(l.CreatedAt)).Scan(&l.ID))
}

// SetTokenEnc stores the sealed token of link id.
func (r *ServerShareLinks) SetTokenEnc(ctx context.Context, id int64, enc []byte) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE server_share_links SET token_enc = ? WHERE id = ?`, enc, id))
}

// Get returns a link by id regardless of its state.
func (r *ServerShareLinks) Get(ctx context.Context, id int64) (*ServerShareLink, error) {
	return scanServerShareLink(r.q.QueryRowContext(ctx, serverShareLinkSelect+` WHERE l.id = ?`, id))
}

// GetByHash returns the link whose token digest is hash, regardless of its
// state (callers check Status).
func (r *ServerShareLinks) GetByHash(ctx context.Context, hash []byte) (*ServerShareLink, error) {
	return scanServerShareLink(r.q.QueryRowContext(ctx, serverShareLinkSelect+` WHERE l.token_sha256 = ?`, hash))
}

// List returns the non-revoked links, newest first: of userID, or of every
// user when userID is nil. limit is clamped to 1..1000 (default 500).
func (r *ServerShareLinks) List(ctx context.Context, userID *int64, limit int) ([]*ServerShareLink, error) {
	where, args := ` WHERE l.revoked_at IS NULL`, []any{}
	if userID != nil {
		where += ` AND l.user_id = ?`
		args = append(args, *userID)
	}
	rows, err := r.q.QueryContext(ctx, serverShareLinkSelect+where+` ORDER BY l.created_at DESC, l.id DESC LIMIT ?`,
		append(args, clampLimit(limit, 500, 1000))...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*ServerShareLink{}
	for rows.Next() {
		l, err := scanServerShareLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, mapErr(rows.Err())
}

// CountOpen counts the links of userID that are neither revoked nor expired.
func (r *ServerShareLinks) CountOpen(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM server_share_links WHERE user_id = ? AND revoked_at IS NULL
		AND (expires_at IS NULL OR expires_at > ?)`, userID, toMs(r.now())).Scan(&n)
	return n, mapErr(err)
}

// Revoke marks a link revoked (idempotent); unknown ids return ErrNotFound.
func (r *ServerShareLinks) Revoke(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE server_share_links SET revoked_at = coalesce(revoked_at, ?) WHERE id = ?`,
		toMs(r.now()), id))
}

// RecordView counts one opening of the public page.
func (r *ServerShareLinks) RecordView(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE server_share_links SET views = views + 1, last_used_at = ? WHERE id = ?`,
		toMs(r.now()), id))
}

// TakeDownload counts one installer download when the link is neither
// revoked, expired nor out of downloads, atomically; false means refused.
func (r *ServerShareLinks) TakeDownload(ctx context.Context, id int64) (bool, error) {
	now := toMs(r.now())
	res, err := r.q.ExecContext(ctx, `UPDATE server_share_links SET downloads = downloads + 1, last_used_at = ?
		WHERE id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)
		AND (max_downloads IS NULL OR downloads < max_downloads)`, now, id, now)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CanShare reports whether user id may create share links: an active admin,
// or an active user whose group has can_share_links. Unknown users: false.
func (r *ServerShareLinks) CanShare(ctx context.Context, userID int64) (bool, error) {
	var ok int64
	err := r.q.QueryRowContext(ctx, `SELECT `+ownerAllowedExpr+` FROM users u
		LEFT JOIN user_groups g ON g.id = u.user_group_id WHERE u.id = ?`, userID).Scan(&ok)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return ok == 1, mapErr(err)
}
