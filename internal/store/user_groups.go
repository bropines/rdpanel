package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/db"
)

// GroupVisibility controls what members of a user group see in the client.
type GroupVisibility string

// Group visibilities.
const (
	// VisibilitySelf: members see only their own devices (legacy type 1).
	VisibilitySelf GroupVisibility = "self"
	// VisibilityShared: members see each other's devices and users (legacy type 2).
	VisibilityShared GroupVisibility = "shared"
)

// UserGroup is a row of user_groups.
type UserGroup struct {
	ID         int64
	Name       string
	Visibility GroupVisibility
	Note       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// UserGroups is the user_groups repository.
type UserGroups struct {
	q   db.Querier
	now func() time.Time
}

const userGroupColumns = `id, name, visibility, note, created_at, updated_at`

func scanUserGroup(row interface{ Scan(...any) error }) (*UserGroup, error) {
	var (
		g                    UserGroup
		createdAt, updatedAt int64
	)
	if err := row.Scan(&g.ID, &g.Name, &g.Visibility, &g.Note, &createdAt, &updatedAt); err != nil {
		return nil, mapErr(err)
	}
	g.CreatedAt, g.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &g, nil
}

// Create inserts g (honouring a preset ID for legacy import).
func (r *UserGroups) Create(ctx context.Context, g *UserGroup) error {
	now := r.now()
	if g.Visibility == "" {
		g.Visibility = VisibilitySelf
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = now
	}
	g.UpdatedAt = now
	var id sql.NullInt64
	if g.ID != 0 {
		id = sql.NullInt64{Int64: g.ID, Valid: true}
	}
	return mapErr(r.q.QueryRowContext(ctx, `INSERT INTO user_groups (id, name, visibility, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		id, strings.TrimSpace(g.Name), g.Visibility, g.Note, toMs(g.CreatedAt), toMs(g.UpdatedAt)).Scan(&g.ID))
}

// Get returns the group with id.
func (r *UserGroups) Get(ctx context.Context, id int64) (*UserGroup, error) {
	return scanUserGroup(r.q.QueryRowContext(ctx, `SELECT `+userGroupColumns+` FROM user_groups WHERE id = ?`, id))
}

// GetByName returns the group named name (case-insensitive).
func (r *UserGroups) GetByName(ctx context.Context, name string) (*UserGroup, error) {
	return scanUserGroup(r.q.QueryRowContext(ctx,
		`SELECT `+userGroupColumns+` FROM user_groups WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name)))
}

// List returns all groups ordered by name.
func (r *UserGroups) List(ctx context.Context) ([]*UserGroup, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+userGroupColumns+` FROM user_groups ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*UserGroup
	for rows.Next() {
		g, err := scanUserGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, mapErr(rows.Err())
}

// Update writes name, visibility and note.
func (r *UserGroups) Update(ctx context.Context, g *UserGroup) error {
	g.UpdatedAt = r.now()
	return expectOne(r.q.ExecContext(ctx, `UPDATE user_groups SET name = ?, visibility = ?, note = ?, updated_at = ?
		WHERE id = ?`, strings.TrimSpace(g.Name), g.Visibility, g.Note, toMs(g.UpdatedAt), g.ID))
}

// Delete removes a group; members' user_group_id becomes NULL.
func (r *UserGroups) Delete(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM user_groups WHERE id = ?`, id))
}

// MemberCount returns the number of users in group id.
func (r *UserGroups) MemberCount(ctx context.Context, id int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE user_group_id = ?`, id).Scan(&n)
	return n, mapErr(err)
}
