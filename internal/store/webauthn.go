package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
)

// MaxPasskeysPerUser bounds webauthn_credentials rows per user.
const MaxPasskeysPerUser = 20

// WebAuthnCredential is a row of webauthn_credentials (a registered passkey).
type WebAuthnCredential struct {
	ID              int64
	UserID          int64
	CredentialID    []byte
	PublicKey       []byte
	AttestationType string
	AAGUID          []byte
	SignCount       uint32
	CloneWarning    bool
	Transports      []string
	Attachment      string
	UserVerified    bool
	BackupEligible  bool
	BackupState     bool
	Name            string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastUsedAt      *time.Time
}

// WebAuthnCredentials is the webauthn_credentials / webauthn_user_handles
// repository.
type WebAuthnCredentials struct {
	q   db.Querier
	now func() time.Time
}

const webauthnCredCols = `id, user_id, credential_id, public_key, attestation_type, aaguid, sign_count,
	clone_warning, transports, attachment, user_verified, backup_eligible, backup_state, name,
	created_at, updated_at, last_used_at`

func scanWebAuthnCredential(row interface{ Scan(...any) error }) (*WebAuthnCredential, error) {
	var (
		c                    WebAuthnCredential
		signCount            int64
		clone, uv, be, bs    int
		transports           string
		createdAt, updatedAt int64
		lastUsed             sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.AttestationType, &c.AAGUID, &signCount,
		&clone, &transports, &c.Attachment, &uv, &be, &bs, &c.Name, &createdAt, &updatedAt, &lastUsed); err != nil {
		return nil, mapErr(err)
	}
	c.SignCount = uint32(signCount)
	c.CloneWarning, c.UserVerified, c.BackupEligible, c.BackupState = clone == 1, uv == 1, be == 1, bs == 1
	if transports != "" {
		c.Transports = strings.Split(transports, ",")
	}
	c.CreatedAt, c.UpdatedAt, c.LastUsedAt = fromMs(createdAt), fromMs(updatedAt), ptrMs(lastUsed)
	return &c, nil
}

// ListForUser returns a user's passkeys, oldest first.
func (r *WebAuthnCredentials) ListForUser(ctx context.Context, userID int64) ([]*WebAuthnCredential, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+webauthnCredCols+` FROM webauthn_credentials
		WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*WebAuthnCredential{}
	for rows.Next() {
		c, err := scanWebAuthnCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// CountForUser returns the number of passkeys of a user.
func (r *WebAuthnCredentials) CountForUser(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = ?`, userID).Scan(&n)
	return n, mapErr(err)
}

// GetByCredentialID returns the passkey with the WebAuthn credential id.
func (r *WebAuthnCredentials) GetByCredentialID(ctx context.Context, credID []byte) (*WebAuthnCredential, error) {
	return scanWebAuthnCredential(r.q.QueryRowContext(ctx, `SELECT `+webauthnCredCols+`
		FROM webauthn_credentials WHERE credential_id = ?`, credID))
}

// GetForUser returns passkey id of user userID (ErrNotFound otherwise).
func (r *WebAuthnCredentials) GetForUser(ctx context.Context, userID, id int64) (*WebAuthnCredential, error) {
	return scanWebAuthnCredential(r.q.QueryRowContext(ctx, `SELECT `+webauthnCredCols+`
		FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID))
}

// Create inserts c; ID, CreatedAt and UpdatedAt are set. A duplicate
// credential id is ErrConflict.
func (r *WebAuthnCredentials) Create(ctx context.Context, c *WebAuthnCredential) error {
	now := r.now()
	res, err := r.q.ExecContext(ctx, `INSERT INTO webauthn_credentials (user_id, credential_id, public_key,
		attestation_type, aaguid, sign_count, clone_warning, transports, attachment, user_verified, backup_eligible,
		backup_state, name, created_at, updated_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		c.UserID, c.CredentialID, c.PublicKey, c.AttestationType, c.AAGUID, int64(c.SignCount),
		strings.Join(c.Transports, ","), c.Attachment, boolInt(c.UserVerified), boolInt(c.BackupEligible),
		boolInt(c.BackupState), c.Name, toMs(now), toMs(now))
	if err != nil {
		return mapErr(err)
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt, c.UpdatedAt = fromMs(toMs(now)), fromMs(toMs(now))
	return nil
}

// Rename sets the display name of passkey id of user userID.
func (r *WebAuthnCredentials) Rename(ctx context.Context, userID, id int64, name string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE webauthn_credentials SET name = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`, name, toMs(r.now()), id, userID))
}

// DeleteForUser removes passkey id of user userID.
func (r *WebAuthnCredentials) DeleteForUser(ctx context.Context, userID, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID))
}

// RecordUse stores the state after a successful assertion: the new signature
// counter, the backup state and the last-use time. The update only applies
// while the stored counter is still prev (compare-and-set against concurrent
// replays of the same counter); ErrNotFound otherwise.
func (r *WebAuthnCredentials) RecordUse(ctx context.Context, id int64, prev, signCount uint32, backupState, userVerified bool) error {
	now := toMs(r.now())
	return expectOne(r.q.ExecContext(ctx, `UPDATE webauthn_credentials SET sign_count = ?, backup_state = ?,
		user_verified = MAX(user_verified, ?), last_used_at = ?, updated_at = ? WHERE id = ? AND sign_count = ?`,
		int64(signCount), boolInt(backupState), boolInt(userVerified), now, now, id, int64(prev)))
}

// FlagClone marks a passkey whose signature counter went backwards.
func (r *WebAuthnCredentials) FlagClone(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE webauthn_credentials SET clone_warning = 1, updated_at = ?
		WHERE id = ?`, toMs(r.now()), id))
}

// UserHandle returns the WebAuthn user handle of a user (ErrNotFound when
// none was assigned yet).
func (r *WebAuthnCredentials) UserHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	err := r.q.QueryRowContext(ctx, `SELECT handle FROM webauthn_user_handles WHERE user_id = ?`, userID).Scan(&h)
	return h, mapErr(err)
}

// EnsureUserHandle returns the user's handle, storing handle when none
// exists yet.
func (r *WebAuthnCredentials) EnsureUserHandle(ctx context.Context, userID int64, handle []byte) ([]byte, error) {
	if _, err := r.q.ExecContext(ctx, `INSERT INTO webauthn_user_handles (user_id, handle, created_at)
		VALUES (?, ?, ?) ON CONFLICT (user_id) DO NOTHING`, userID, handle, toMs(r.now())); err != nil {
		return nil, mapErr(err)
	}
	return r.UserHandle(ctx, userID)
}

// UserByHandle resolves a WebAuthn user handle to a user id.
func (r *WebAuthnCredentials) UserByHandle(ctx context.Context, handle []byte) (int64, error) {
	var id int64
	err := r.q.QueryRowContext(ctx, `SELECT user_id FROM webauthn_user_handles WHERE handle = ?`, handle).Scan(&id)
	return id, mapErr(err)
}

// WebAuthnChallengeKind is webauthn_challenges.kind.
type WebAuthnChallengeKind string

// Ceremony kinds.
const (
	WebAuthnRegister WebAuthnChallengeKind = "register"
	WebAuthnLogin    WebAuthnChallengeKind = "login"
	WebAuthnMFA      WebAuthnChallengeKind = "mfa"
)

// WebAuthnChallenge is a row of webauthn_challenges.
type WebAuthnChallenge struct {
	IDSHA256    []byte
	Kind        WebAuthnChallengeKind
	UserID      *int64
	TokenID     *int64
	SessionData []byte
	ExpiresAt   time.Time
}

// WebAuthnChallenges is the webauthn_challenges repository.
type WebAuthnChallenges struct {
	q   db.Querier
	now func() time.Time
}

// Create stores a pending ceremony.
func (r *WebAuthnChallenges) Create(ctx context.Context, c *WebAuthnChallenge) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO webauthn_challenges (id_sha256, kind, user_id, token_id,
		session_data, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, c.IDSHA256, string(c.Kind),
		nullID(c.UserID), nullID(c.TokenID), c.SessionData, toMs(c.ExpiresAt), toMs(r.now()))
	return mapErr(err)
}

// Consume deletes and returns the unexpired ceremony with digest idHash and
// the given kind (single use). ErrNotFound when missing or expired.
func (r *WebAuthnChallenges) Consume(ctx context.Context, idHash []byte, kind WebAuthnChallengeKind) (*WebAuthnChallenge, error) {
	var (
		c             WebAuthnChallenge
		userID, tokID sql.NullInt64
		expires       int64
		kindStr       string
	)
	err := r.q.QueryRowContext(ctx, `DELETE FROM webauthn_challenges WHERE id_sha256 = ? AND kind = ?
		RETURNING id_sha256, kind, user_id, token_id, session_data, expires_at`, idHash, string(kind)).
		Scan(&c.IDSHA256, &kindStr, &userID, &tokID, &c.SessionData, &expires)
	if err != nil {
		return nil, mapErr(err)
	}
	c.Kind, c.UserID, c.TokenID, c.ExpiresAt = WebAuthnChallengeKind(kindStr), ptrID(userID), ptrID(tokID), fromMs(expires)
	if !c.ExpiresAt.After(r.now()) {
		return nil, ErrNotFound
	}
	return &c, nil
}

// CountActiveAnonymous purges expired ceremonies and returns the number of
// pending ceremonies not bound to a user (usernameless logins), which callers
// cap to bound unauthenticated writes.
func (r *WebAuthnChallenges) CountActiveAnonymous(ctx context.Context) (int, error) {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM webauthn_challenges WHERE expires_at <= ?`, toMs(r.now())); err != nil {
		return 0, mapErr(err)
	}
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM webauthn_challenges WHERE user_id IS NULL`).Scan(&n)
	return n, mapErr(err)
}
