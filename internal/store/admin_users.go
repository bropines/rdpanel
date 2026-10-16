package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// UserRef is the minimal user identity used by pickers and embeds.
type UserRef struct {
	ID          int64
	Username    string
	DisplayName string
}

// GroupRef is a minimal group identity (id + name).
type GroupRef struct {
	ID   int64
	Name string
}

func idsJSON(ids []int64) string {
	if ids == nil {
		ids = []int64{}
	}
	raw, _ := json.Marshal(ids)
	return string(raw)
}

// Refs returns the users with the given ids keyed by id (unknown ids are absent).
func (r *Users) Refs(ctx context.Context, ids []int64) (map[int64]UserRef, error) {
	out := map[int64]UserRef{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.q.QueryContext(ctx, `SELECT id, username, display_name FROM users
		WHERE id IN (SELECT value FROM json_each(?))`, idsJSON(ids))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName); err != nil {
			return nil, mapErr(err)
		}
		out[u.ID] = u
	}
	return out, mapErr(rows.Err())
}

// Lookup returns up to limit active users whose username, display name or
// email contains q, ordered by username.
func (r *Users) Lookup(ctx context.Context, q string, limit int) ([]UserRef, error) {
	like := "%" + escapeLike(strings.TrimSpace(q)) + "%"
	rows, err := r.q.QueryContext(ctx, `SELECT id, username, display_name FROM users
		WHERE status = 'active' AND (username LIKE ?1 ESCAPE '\' OR display_name LIKE ?1 ESCAPE '\'
		  OR coalesce(email, '') LIKE ?1 ESCAPE '\')
		ORDER BY username COLLATE NOCASE, id LIMIT ?2`, like, clampLimit(limit, 20, 100))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, u)
	}
	return out, mapErr(rows.Err())
}

// UserRow is a user with the joined data of the admin users list.
type UserRow struct {
	*User
	GroupName   string
	DeviceCount int
	TOTPEnabled bool
}

const userRowSelect = `SELECT u.id, u.username, u.email, u.display_name, u.avatar_url, u.note, u.password_hash,
	u.password_algo, u.role, u.status, u.user_group_id, u.totp_required, u.password_changed_at, u.last_login_at,
	u.created_at, u.updated_at, u.email_verified_at, u.email_2fa_enabled, u.locale, coalesce(g.name, ''),
	(SELECT count(*) FROM devices d WHERE d.owner_user_id = u.id),
	EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.confirmed_at IS NOT NULL)
	FROM users u LEFT JOIN user_groups g ON g.id = u.user_group_id`

func scanUserRow(row interface{ Scan(...any) error }) (*UserRow, error) {
	var (
		ur    UserRow
		group string
		n     int
		totp  bool
	)
	u, err := scanUser(scanFunc(func(dest ...any) error {
		return row.Scan(append(dest, &group, &n, &totp)...)
	}))
	if err != nil {
		return nil, err
	}
	ur.User, ur.GroupName, ur.DeviceCount, ur.TOTPEnabled = u, group, n, totp
	return &ur, nil
}

// UserSearch filters, sorts and pages Users.Search.
type UserSearch struct {
	// Query matches username, email or display name (substring).
	Query       string
	Role        Role
	Status      UserStatus
	UserGroupID *int64
	// Sort: username (default), created_at, last_login_at, role.
	Sort   string
	Desc   bool
	Limit  int
	Offset int
}

// Search returns a page of users with list data and the total match count.
func (r *Users) Search(ctx context.Context, p UserSearch) ([]*UserRow, int, error) {
	var (
		where []string
		args  []any
	)
	if q := strings.TrimSpace(p.Query); q != "" {
		like := "%" + escapeLike(q) + "%"
		where = append(where, `(u.username LIKE ? ESCAPE '\' OR coalesce(u.email, '') LIKE ? ESCAPE '\'
			OR u.display_name LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if p.Role != "" {
		where, args = append(where, "u.role = ?"), append(args, p.Role)
	}
	if p.Status != "" {
		where, args = append(where, "u.status = ?"), append(args, p.Status)
	}
	if p.UserGroupID != nil {
		where, args = append(where, "u.user_group_id = ?"), append(args, *p.UserGroupID)
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var expr string
	switch p.Sort {
	case "", "username":
		expr = "u.username COLLATE NOCASE"
	case "created_at":
		expr = "u.created_at"
	case "last_login_at":
		expr = "coalesce(u.last_login_at, 0)"
	case "role":
		expr = "u.role"
	default:
		return nil, 0, fmt.Errorf("%w: sort %q", ErrInvalid, p.Sort)
	}
	dir := " ASC"
	if p.Desc {
		dir = " DESC"
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM users u`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, userRowSelect+cond+" ORDER BY "+expr+dir+", u.id"+dir+" LIMIT ? OFFSET ?",
		append(args, clampLimit(p.Limit, 50, 1000), max(p.Offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []*UserRow{}
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, mapErr(rows.Err())
}

// GetRow returns one user with list data.
func (r *Users) GetRow(ctx context.Context, id int64) (*UserRow, error) {
	return scanUserRow(r.q.QueryRowContext(ctx, userRowSelect+` WHERE u.id = ?`, id))
}

// ---- user groups ----

// UserGroupRow is a user group with its member count.
type UserGroupRow struct {
	*UserGroup
	MemberCount int
}

const userGroupRowSelect = `SELECT g.id, g.name, g.visibility, g.note, g.created_at, g.updated_at,
	(SELECT count(*) FROM users u WHERE u.user_group_id = g.id) AS member_count FROM user_groups g`

func scanUserGroupRow(row interface{ Scan(...any) error }) (*UserGroupRow, error) {
	var n int
	g, err := scanUserGroup(scanFunc(func(dest ...any) error { return row.Scan(append(dest, &n)...) }))
	if err != nil {
		return nil, err
	}
	return &UserGroupRow{UserGroup: g, MemberCount: n}, nil
}

// Search returns a page of user groups with member counts.
func (r *UserGroups) Search(ctx context.Context, p GroupSearch) ([]*UserGroupRow, int, error) {
	cond, args, order, err := p.clause("member_count")
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM user_groups g`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, userGroupRowSelect+cond+order+` LIMIT ? OFFSET ?`,
		append(args, clampLimit(p.Limit, 50, 1000), max(p.Offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []*UserGroupRow{}
	for rows.Next() {
		g, err := scanUserGroupRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, mapErr(rows.Err())
}

// GetRow returns one user group with its member count.
func (r *UserGroups) GetRow(ctx context.Context, id int64) (*UserGroupRow, error) {
	return scanUserGroupRow(r.q.QueryRowContext(ctx, userGroupRowSelect+` WHERE g.id = ?`, id))
}

// Lookup returns up to limit groups whose name contains q, ordered by name.
func (r *UserGroups) Lookup(ctx context.Context, q string, limit int) ([]GroupRef, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, name FROM user_groups WHERE name LIKE ? ESCAPE '\'
		ORDER BY name COLLATE NOCASE, id LIMIT ?`, "%"+escapeLike(strings.TrimSpace(q))+"%", clampLimit(limit, 20, 100))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []GroupRef{}
	for rows.Next() {
		var g GroupRef
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, g)
	}
	return out, mapErr(rows.Err())
}

// ---- address books (read helpers for the devices and users pages) ----

// bookVisibleExpr: book b is visible to user ?N (owner, user share or group share).
const bookVisibleExpr = `(b.owner_user_id = ?1 OR EXISTS (SELECT 1 FROM address_book_shares s
	WHERE s.address_book_id = b.id AND (s.grantee_user_id = ?1
	   OR s.grantee_group_id = (SELECT user_group_id FROM users WHERE id = ?1))))`

// BookRef identifies a book containing a device.
type BookRef struct {
	ID            int64
	Name          string
	IsPersonal    bool
	OwnerUsername string
}

// BooksContaining returns the books with an entry for rustdeskID, ordered by
// name. With visibleTo set only books visible to that user are returned.
func (r *AddressBooks) BooksContaining(ctx context.Context, rustdeskID string, visibleTo *int64) ([]BookRef, error) {
	q := `SELECT b.id, b.name, b.is_personal, coalesce(u.username, '') FROM address_books b
		LEFT JOIN users u ON u.id = b.owner_user_id
		WHERE EXISTS (SELECT 1 FROM address_book_entries e WHERE e.address_book_id = b.id AND e.rustdesk_id = ?2)`
	args := []any{int64(0), rustdeskID}
	if visibleTo != nil {
		q += ` AND ` + bookVisibleExpr
		args[0] = *visibleTo
	}
	rows, err := r.q.QueryContext(ctx, q+` ORDER BY b.name COLLATE NOCASE, b.id`, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []BookRef{}
	for rows.Next() {
		var b BookRef
		if err := rows.Scan(&b.ID, &b.Name, &b.IsPersonal, &b.OwnerUsername); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, b)
	}
	return out, mapErr(rows.Err())
}

// CountVisible counts books (all when visibleTo is nil, else those visible to the user).
func (r *AddressBooks) CountVisible(ctx context.Context, visibleTo *int64) (int, error) {
	var n int
	var err error
	if visibleTo == nil {
		err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_books`).Scan(&n)
	} else {
		err = r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_books b WHERE `+bookVisibleExpr, *visibleTo).Scan(&n)
	}
	return n, mapErr(err)
}

// CountOwned counts the books owned by userID.
func (r *AddressBooks) CountOwned(ctx context.Context, userID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_books WHERE owner_user_id = ?`, userID).Scan(&n)
	return n, mapErr(err)
}

// ---- dashboard statistics ----

// hourBucket is the SQL expression of the hour start (unix ms) of col.
func hourBucket(col string) string { return "((" + col + " / 3600000) * 3600000)" }

// LoginStats counts login events since `since` (optionally of one user):
// successes, failures and successes per hour (keyed by hour start, unix ms).
func (r *LoginEvents) LoginStats(ctx context.Context, since time.Time, userID *int64) (ok, failed int, hourly map[int64]int, err error) {
	cond, args := ` WHERE created_at >= ?`, []any{toMs(since)}
	if userID != nil {
		cond, args = cond+` AND user_id = ?`, append(args, *userID)
	}
	if err = r.q.QueryRowContext(ctx, `SELECT coalesce(sum(result = 'success'), 0), coalesce(sum(result = 'failure'), 0)
		FROM login_events`+cond, args...).Scan(&ok, &failed); err != nil {
		return 0, 0, nil, mapErr(err)
	}
	hourly, err = hourlyCounts(ctx, r.q, `SELECT `+hourBucket("created_at")+` AS h, count(*) FROM login_events`+cond+
		` AND result = 'success' GROUP BY h`, args)
	return ok, failed, hourly, err
}

// ConnectionStats counts connections started since `since` (optionally only
// to devices owned by ownerID): total, still open, and per hour.
func (r *ConnectionAudits) ConnectionStats(ctx context.Context, since time.Time, ownerID *int64) (total, active int, hourly map[int64]int, err error) {
	cond, args := ` WHERE started_at >= ?`, []any{toMs(since)}
	if ownerID != nil {
		cond += ` AND device_rustdesk_id IN (SELECT rustdesk_id FROM devices WHERE owner_user_id = ?)`
		args = append(args, *ownerID)
	}
	if err = r.q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(ended_at IS NULL), 0) FROM connection_audits`+cond,
		args...).Scan(&total, &active); err != nil {
		return 0, 0, nil, mapErr(err)
	}
	hourly, err = hourlyCounts(ctx, r.q, `SELECT `+hourBucket("started_at")+` AS h, count(*) FROM connection_audits`+
		cond+` GROUP BY h`, args)
	return total, active, hourly, err
}

func hourlyCounts(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, query string, args []any) (map[int64]int, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var h int64
		var n int
		if err := rows.Scan(&h, &n); err != nil {
			return nil, mapErr(err)
		}
		out[h] = n
	}
	return out, mapErr(rows.Err())
}

// HourStart returns the start of the UTC hour containing t.
func HourStart(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }
