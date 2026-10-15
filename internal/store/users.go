package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// Role is a user's panel role.
type Role string

// Roles.
const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// UserStatus is whether a user may log in.
type UserStatus string

// User statuses.
const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

// User is a row of the users table.
type User struct {
	ID          int64
	Username    string
	Email       string // "" = NULL
	DisplayName string
	AvatarURL   string
	Note        string
	// PasswordHash/PasswordAlgo are both empty when the user has no local password.
	PasswordHash      string
	PasswordAlgo      string
	Role              Role
	Status            UserStatus
	UserGroupID       *int64
	TOTPRequired      bool
	PasswordChangedAt *time.Time
	LastLoginAt       *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// IsActive reports whether the user may log in.
func (u *User) IsActive() bool { return u.Status == UserActive }

// HasPassword reports whether a local password is set.
func (u *User) HasPassword() bool { return u.PasswordHash != "" }

// Users is the users repository.
type Users struct {
	q   db.Querier
	now func() time.Time
}

const userColumns = `id, username, email, display_name, avatar_url, note, password_hash, password_algo,
	role, status, user_group_id, totp_required, password_changed_at, last_login_at, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u                     User
		email, hash, algo     sql.NullString
		groupID, pwAt, lastAt sql.NullInt64
		totp                  int
		createdAt, updatedAt  int64
	)
	err := row.Scan(&u.ID, &u.Username, &email, &u.DisplayName, &u.AvatarURL, &u.Note, &hash, &algo,
		&u.Role, &u.Status, &groupID, &totp, &pwAt, &lastAt, &createdAt, &updatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	u.Email, u.PasswordHash, u.PasswordAlgo = email.String, hash.String, algo.String
	u.UserGroupID = ptrID(groupID)
	u.TOTPRequired = totp == 1
	u.PasswordChangedAt, u.LastLoginAt = ptrMs(pwAt), ptrMs(lastAt)
	u.CreatedAt, u.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &u, nil
}

// Create inserts u. When u.ID is non-zero it is used as-is (legacy import);
// otherwise an id is assigned. ID, CreatedAt and UpdatedAt are set on u.
func (r *Users) Create(ctx context.Context, u *User) error {
	now := r.now()
	if u.Role == "" {
		u.Role = RoleUser
	}
	if u.Status == "" {
		u.Status = UserActive
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	var id sql.NullInt64
	if u.ID != 0 {
		id = sql.NullInt64{Int64: u.ID, Valid: true}
	}
	err := r.q.QueryRowContext(ctx, `INSERT INTO users (id, username, email, display_name, avatar_url, note,
		password_hash, password_algo, role, status, user_group_id, totp_required, password_changed_at,
		last_login_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		id, strings.TrimSpace(u.Username), nullStr(u.Email), u.DisplayName, u.AvatarURL, u.Note,
		nullStr(u.PasswordHash), nullStr(u.PasswordAlgo), u.Role, u.Status, nullID(u.UserGroupID),
		boolInt(u.TOTPRequired), nullMs(u.PasswordChangedAt), nullMs(u.LastLoginAt),
		toMs(u.CreatedAt), toMs(u.UpdatedAt)).Scan(&u.ID)
	return mapErr(err)
}

// GetByID returns the user with id.
func (r *Users) GetByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(r.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// GetByUsername returns the user with username (case-insensitive).
func (r *Users) GetByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(r.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`,
		strings.TrimSpace(username)))
}

// ListUsersParams filters and pages List.
type ListUsersParams struct {
	// Query matches username, email or display name (substring, case-insensitive).
	Query       string
	Role        Role
	Status      UserStatus
	UserGroupID *int64
	Limit       int
	Offset      int
}

// List returns a page of users ordered by id and the total match count.
func (r *Users) List(ctx context.Context, p ListUsersParams) ([]*User, int, error) {
	var (
		where []string
		args  []any
	)
	if q := strings.TrimSpace(p.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		where = append(where, `(username LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if p.Role != "" {
		where = append(where, "role = ?")
		args = append(args, p.Role)
	}
	if p.Status != "" {
		where = append(where, "status = ?")
		args = append(args, p.Status)
	}
	if p.UserGroupID != nil {
		where = append(where, "user_group_id = ?")
		args = append(args, *p.UserGroupID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM users`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	limit := clampLimit(p.Limit, 50, 1000)
	rows, err := r.q.QueryContext(ctx, `SELECT `+userColumns+` FROM users`+cond+` ORDER BY id LIMIT ? OFFSET ?`,
		append(args, limit, max(p.Offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, mapErr(rows.Err())
}

// Update writes the mutable profile/role fields of u (not the password).
func (r *Users) Update(ctx context.Context, u *User) error {
	u.UpdatedAt = r.now()
	return expectOne(r.q.ExecContext(ctx, `UPDATE users SET username = ?, email = ?, display_name = ?,
		avatar_url = ?, note = ?, role = ?, status = ?, user_group_id = ?, totp_required = ?, updated_at = ?
		WHERE id = ?`,
		strings.TrimSpace(u.Username), nullStr(u.Email), u.DisplayName, u.AvatarURL, u.Note, u.Role, u.Status,
		nullID(u.UserGroupID), boolInt(u.TOTPRequired), toMs(u.UpdatedAt), u.ID))
}

// SetPassword stores a new password hash (empty hash clears the password).
func (r *Users) SetPassword(ctx context.Context, id int64, hash, algo string) error {
	now := r.now()
	changed := sql.NullInt64{Int64: toMs(now), Valid: hash != ""}
	return expectOne(r.q.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_algo = ?,
		password_changed_at = ?, updated_at = ? WHERE id = ?`,
		nullStr(hash), nullStr(algo), changed, toMs(now), id))
}

// RehashPassword replaces the stored hash after a successful login with an
// outdated algorithm; password_changed_at is left untouched.
func (r *Users) RehashPassword(ctx context.Context, id int64, hash, algo string) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_algo = ? WHERE id = ?`,
		hash, algo, id))
}

// TouchLastLogin records a successful login.
func (r *Users) TouchLastLogin(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, toMs(r.now()), id))
}

// Delete removes a user (tokens, identities, books cascade).
func (r *Users) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id))
}

// Count returns the number of users.
func (r *Users) Count(ctx context.Context) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, mapErr(err)
}

// CountActiveAdmins returns the number of enabled admins (last-admin protection).
func (r *Users) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, mapErr(err)
}
