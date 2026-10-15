package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Panel (admin API) side of the address-book repository: filtered listing,
// share CRUD and id-addressed entries and tags. Permission semantics are the
// client API's (Permission / abRuleSQL); the panel adds the admin override.

// nargs builds numbered SQLite parameters (?1, ?2, ...) so one value can be
// referenced several times (abRuleSQL uses ?1 for the viewer).
type nargs struct{ vals []any }

func (a *nargs) add(v any) string {
	a.vals = append(a.vals, v)
	return "?" + strconv.Itoa(len(a.vals))
}

func likeArg(q string) string { return "%" + escapeLike(strings.TrimSpace(q)) + "%" }

// BookListFilter selects address books for the panel.
type BookListFilter struct {
	// ViewerID computes Rule (and, unless All, restricts to books with a rule > 0).
	ViewerID int64
	// All lists every book regardless of the viewer's rule (admin scope=all).
	All bool
	// ID restricts to one book.
	ID          int64
	OwnerUserID *int64
	// Kind is "personal", "shared" or "" (both).
	Kind string
	// Q matches name, note or owner username (case-insensitive substring).
	Q string
	// Sort: name, entry_count or updated_at, "-" prefix for descending.
	Sort   string
	Limit  int
	Offset int
}

// BookListItem is a book with the viewer's rule and counters.
type BookListItem struct {
	*AddressBook
	OwnerDisplayName string
	Rule             ABPermission
	EntryCount       int
	ShareCount       int
}

var bookSorts = map[string]string{
	"name": "b.name COLLATE NOCASE", "entry_count": "entry_count", "updated_at": "b.updated_at",
}

// ListBooks returns a page of books matching f and the total count.
func (r *AddressBooks) ListBooks(ctx context.Context, f BookListFilter) ([]BookListItem, int, error) {
	a := &nargs{}
	a.add(f.ViewerID) // ?1 for abRuleSQL
	var where []string
	if !f.All {
		where = append(where, "("+abRuleSQL+") > 0")
	}
	if f.ID > 0 {
		where = append(where, "b.id = "+a.add(f.ID))
	}
	if f.OwnerUserID != nil {
		where = append(where, "b.owner_user_id = "+a.add(*f.OwnerUserID))
	}
	switch f.Kind {
	case "personal":
		where = append(where, "b.is_personal = 1")
	case "shared":
		where = append(where, "b.is_personal = 0")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		p := a.add(likeArg(q))
		where = append(where, fmt.Sprintf(`(b.name LIKE %[1]s ESCAPE '\' OR b.note LIKE %[1]s ESCAPE '\'
			OR coalesce(u.username, '') LIKE %[1]s ESCAPE '\')`, p))
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*)`+bookFrom+cond, a.vals...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	col, desc := sortSpec(f.Sort, bookSorts, "name")
	order := col
	if desc {
		order += " DESC"
	}
	lim, off := a.add(clampLimit(f.Limit, 50, 1000)), a.add(max(f.Offset, 0))
	rows, err := r.q.QueryContext(ctx, `SELECT `+bookColumns+`, coalesce(u.display_name, ''), `+abRuleSQL+`,
		(SELECT count(*) FROM address_book_entries x WHERE x.address_book_id = b.id) AS entry_count,
		(SELECT count(*) FROM address_book_shares y WHERE y.address_book_id = b.id)`+bookFrom+cond+`
		ORDER BY `+order+`, b.id LIMIT `+lim+` OFFSET `+off, a.vals...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	out := []BookListItem{}
	for rows.Next() {
		var (
			it   BookListItem
			rule int
		)
		it.AddressBook, err = r.scanBook(scanFunc(func(dest ...any) error {
			return rows.Scan(append(dest, &it.OwnerDisplayName, &rule, &it.EntryCount, &it.ShareCount)...)
		}))
		if err != nil {
			return nil, 0, err
		}
		it.Rule = ABPermission(rule)
		out = append(out, it)
	}
	return out, total, mapErr(rows.Err())
}

// sortSpec resolves "field" / "-field" against allowed (SQL expressions).
func sortSpec(sort string, allowed map[string]string, def string) (string, bool) {
	desc := strings.HasPrefix(sort, "-")
	if col, ok := allowed[strings.TrimPrefix(sort, "-")]; ok {
		return col, desc
	}
	return allowed[strings.TrimPrefix(def, "-")], strings.HasPrefix(def, "-")
}

// ensureSharedNameFree returns ErrConflict when another shared book already
// uses name (case-insensitive). The client provisioning API resolves shared
// books by name, so names must stay unambiguous.
func (r *AddressBooks) ensureSharedNameFree(ctx context.Context, name string, exceptID int64) error {
	var n int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_books WHERE is_personal = 0
		AND name = ? COLLATE NOCASE AND id <> ?`, strings.TrimSpace(name), exceptID).Scan(&n); err != nil {
		return mapErr(err)
	}
	if n > 0 {
		return fmt.Errorf("%w: address book name %q is taken", ErrConflict, name)
	}
	return nil
}

// CreateShared creates a non-personal book (unique name, see ensureSharedNameFree).
func (r *AddressBooks) CreateShared(ctx context.Context, b *AddressBook) error {
	b.IsPersonal = false
	return r.atomic(ctx, func(r *AddressBooks) error {
		if err := r.ensureSharedNameFree(ctx, b.Name, 0); err != nil {
			return err
		}
		return r.Create(ctx, b)
	})
}

// BookPatch is a partial book update; nil fields are kept. DefaultPassword ""
// clears the password.
type BookPatch struct {
	Name            *string
	Note            *string
	DefaultPassword *string
}

// UpdateBook applies p to book id.
func (r *AddressBooks) UpdateBook(ctx context.Context, id int64, p BookPatch) error {
	return r.atomic(ctx, func(r *AddressBooks) error {
		b, err := r.Get(ctx, id)
		if err != nil {
			return err
		}
		if p.Name != nil && !b.IsPersonal {
			if err := r.ensureSharedNameFree(ctx, *p.Name, id); err != nil {
				return err
			}
		}
		sets, args := []string{"updated_at = ?"}, []any{toMs(r.now())}
		if p.Name != nil {
			sets, args = append(sets, "name = ?"), append(args, *p.Name)
		}
		if p.Note != nil {
			sets, args = append(sets, "note = ?"), append(args, *p.Note)
		}
		if err := expectOne(r.q.ExecContext(ctx, `UPDATE address_books SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
			append(args, id)...)); err != nil {
			return err
		}
		if p.DefaultPassword != nil {
			return r.setDefaultPassword(ctx, id, *p.DefaultPassword)
		}
		return nil
	})
}

// DeleteBook deletes book id (entries, tags and shares cascade).
func (r *AddressBooks) DeleteBook(ctx context.Context, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM address_books WHERE id = ?`, id))
}

// touchBook bumps updated_at of a book after an entry/tag change.
func (r *AddressBooks) touchBook(ctx context.Context, id int64) error {
	_, err := r.q.ExecContext(ctx, `UPDATE address_books SET updated_at = ? WHERE id = ?`, toMs(r.now()), id)
	return mapErr(err)
}

// ---- shares ----

// ABShare is a row of address_book_shares with the grantee's name joined in
// (username for users, group name for groups).
type ABShare struct {
	ID             int64
	AddressBookID  int64
	GranteeUserID  *int64
	GranteeGroupID *int64
	GranteeName    string
	Permission     ABPermission
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const shareSelect = `SELECT s.id, s.address_book_id, s.grantee_user_id, s.grantee_group_id,
	coalesce(u.username, g.name, ''), s.permission, s.created_at, s.updated_at
	FROM address_book_shares s
	LEFT JOIN users u ON u.id = s.grantee_user_id
	LEFT JOIN user_groups g ON g.id = s.grantee_group_id`

func scanShare(row interface{ Scan(...any) error }) (*ABShare, error) {
	var (
		s                    ABShare
		uid, gid             sql.NullInt64
		perm                 string
		createdAt, updatedAt int64
	)
	if err := row.Scan(&s.ID, &s.AddressBookID, &uid, &gid, &s.GranteeName, &perm, &createdAt, &updatedAt); err != nil {
		return nil, mapErr(err)
	}
	s.GranteeUserID, s.GranteeGroupID = ptrID(uid), ptrID(gid)
	s.Permission = ParseABPermission(perm)
	s.CreatedAt, s.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	return &s, nil
}

// ListShares returns the shares of a book (users first, then groups, by name).
func (r *AddressBooks) ListShares(ctx context.Context, bookID int64) ([]*ABShare, error) {
	rows, err := r.q.QueryContext(ctx, shareSelect+` WHERE s.address_book_id = ?
		ORDER BY s.grantee_user_id IS NULL, coalesce(u.username, g.name) COLLATE NOCASE, s.id`, bookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*ABShare{}
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

// GetShare returns share id of a book.
func (r *AddressBooks) GetShare(ctx context.Context, bookID, id int64) (*ABShare, error) {
	return scanShare(r.q.QueryRowContext(ctx, shareSelect+` WHERE s.address_book_id = ? AND s.id = ?`, bookID, id))
}

// CreateShare grants exactly one grantee perm on a book. ErrConflict when the
// grantee already has a share, ErrReferenced when the grantee does not exist.
func (r *AddressBooks) CreateShare(ctx context.Context, bookID int64, userID, groupID *int64, perm ABPermission) (*ABShare, error) {
	if (userID == nil) == (groupID == nil) {
		return nil, fmt.Errorf("%w: exactly one grantee required", ErrInvalid)
	}
	now := toMs(r.now())
	var id int64
	if err := r.q.QueryRowContext(ctx, `INSERT INTO address_book_shares (address_book_id, grantee_user_id,
		grantee_group_id, permission, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		bookID, nullID(userID), nullID(groupID), perm.String(), now, now).Scan(&id); err != nil {
		return nil, mapErr(err)
	}
	return r.GetShare(ctx, bookID, id)
}

// UpdateShare changes the permission of share id.
func (r *AddressBooks) UpdateShare(ctx context.Context, bookID, id int64, perm ABPermission) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE address_book_shares SET permission = ?, updated_at = ?
		WHERE address_book_id = ? AND id = ?`, perm.String(), toMs(r.now()), bookID, id))
}

// DeleteShare removes share id.
func (r *AddressBooks) DeleteShare(ctx context.Context, bookID, id int64) error {
	return expectOne(r.q.ExecContext(ctx, `DELETE FROM address_book_shares WHERE address_book_id = ? AND id = ?`,
		bookID, id))
}

// ---- entries by id ----

// EntryListFilter selects entries of a book.
type EntryListFilter struct {
	// Q matches rustdesk id, alias, hostname, os username, platform or note.
	Q string
	// Tags: only entries having every tag.
	Tags []string
	// Sort: alias, rustdesk_id, hostname or updated_at ("-" = descending).
	Sort   string
	Limit  int
	Offset int
}

var entrySorts = map[string]string{
	"alias": "e.alias COLLATE NOCASE", "rustdesk_id": "e.rustdesk_id", "hostname": "e.hostname COLLATE NOCASE",
	"updated_at": "e.updated_at",
}

// ListEntriesFiltered returns a page of a book's entries (with tags) and the total.
func (r *AddressBooks) ListEntriesFiltered(ctx context.Context, bookID int64, f EntryListFilter) ([]*ABEntry, int, error) {
	where, args := []string{"e.address_book_id = ?"}, []any{bookID}
	if q := strings.TrimSpace(f.Q); q != "" {
		var ors []string
		for _, c := range []string{"e.rustdesk_id", "e.alias", "e.hostname", "e.os_username", "e.platform", "coalesce(e.note, '')"} {
			ors, args = append(ors, c+` LIKE ? ESCAPE '\'`), append(args, likeArg(q))
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	for _, t := range normalizeTags(f.Tags) {
		where, args = append(where, `EXISTS (SELECT 1 FROM address_book_entry_tags et
			JOIN address_book_tags t ON t.id = et.tag_id WHERE et.entry_id = e.id AND t.name = ?)`), append(args, t)
	}
	cond := " WHERE " + strings.Join(where, " AND ")
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_book_entries e`+cond, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	col, desc := sortSpec(f.Sort, entrySorts, "alias")
	order := col
	if desc {
		order += " DESC"
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+entryColumns+entryFrom+cond+` ORDER BY `+order+`, e.id LIMIT ? OFFSET ?`,
		append(args, clampLimit(f.Limit, 50, 1000), max(f.Offset, 0))...)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	out := []*ABEntry{}
	for rows.Next() {
		e, err := r.scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, mapErr(err)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapErr(err)
	}
	return out, total, r.loadTags(ctx, out)
}

// loadTags fills the tag names (sorted) of entries.
func (r *AddressBooks) loadTags(ctx context.Context, entries []*ABEntry) error {
	byID := make(map[int64]*ABEntry, len(entries))
	ids := make([]any, 0, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
		ids = append(ids, e.ID)
	}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		chunk := ids[:n]
		ids = ids[n:]
		rows, err := r.q.QueryContext(ctx, `SELECT et.entry_id, t.name FROM address_book_entry_tags et
			JOIN address_book_tags t ON t.id = et.tag_id WHERE et.entry_id IN (`+placeholders(len(chunk))+`)
			ORDER BY t.name, t.id`, chunk...)
		if err != nil {
			return mapErr(err)
		}
		for rows.Next() {
			var (
				id   int64
				name string
			)
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return mapErr(err)
			}
			if e := byID[id]; e != nil {
				e.Tags = append(e.Tags, name)
			}
		}
		if err := rows.Close(); err != nil {
			return mapErr(err)
		}
		if err := rows.Err(); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// GetEntryByID returns entry id of a book (with tags).
func (r *AddressBooks) GetEntryByID(ctx context.Context, bookID, id int64) (*ABEntry, error) {
	e, err := r.scanEntry(r.q.QueryRowContext(ctx, `SELECT `+entryColumns+entryFrom+`
		WHERE e.address_book_id = ? AND e.id = ?`, bookID, id))
	if err != nil {
		return nil, err
	}
	return e, r.loadTags(ctx, []*ABEntry{e})
}

// CreateEntry adds rustdeskID to a book (ErrConflict when present) and applies p.
func (r *AddressBooks) CreateEntry(ctx context.Context, bookID int64, rustdeskID string, p ABEntryPatch) (int64, error) {
	var id int64
	err := r.atomic(ctx, func(r *AddressBooks) error {
		now := toMs(r.now())
		if err := r.q.QueryRowContext(ctx, `INSERT INTO address_book_entries (address_book_id, rustdesk_id,
			created_at, updated_at) VALUES (?, ?, ?, ?) RETURNING id`, bookID, rustdeskID, now, now).Scan(&id); err != nil {
			return mapErr(err)
		}
		if err := r.patchEntry(ctx, bookID, id, p); err != nil {
			return err
		}
		return r.touchBook(ctx, bookID)
	})
	return id, err
}

// PatchEntryByID applies p (and an optional new RustDesk id) to entry id.
func (r *AddressBooks) PatchEntryByID(ctx context.Context, bookID, id int64, rustdeskID *string, p ABEntryPatch) error {
	return r.atomic(ctx, func(r *AddressBooks) error {
		if rustdeskID != nil {
			if err := expectOne(r.q.ExecContext(ctx, `UPDATE address_book_entries SET rustdesk_id = ?
				WHERE address_book_id = ? AND id = ?`, *rustdeskID, bookID, id)); err != nil {
				return err
			}
		} else {
			var x int64
			if err := r.q.QueryRowContext(ctx, `SELECT id FROM address_book_entries WHERE address_book_id = ? AND id = ?`,
				bookID, id).Scan(&x); err != nil {
				return mapErr(err)
			}
		}
		if err := r.patchEntry(ctx, bookID, id, p); err != nil {
			return err
		}
		return r.touchBook(ctx, bookID)
	})
}

// existingEntryIDs returns the ids of ids that are entries of the book.
func (r *AddressBooks) existingEntryIDs(ctx context.Context, bookID int64, ids []int64) ([]int64, error) {
	seen := map[int64]bool{}
	var out []int64
	for len(ids) > 0 {
		n := min(len(ids), 500)
		args := []any{bookID}
		for _, id := range ids[:n] {
			args = append(args, id)
		}
		ids = ids[n:]
		rows, err := r.q.QueryContext(ctx, `SELECT id FROM address_book_entries WHERE address_book_id = ?
			AND id IN (`+placeholders(len(args)-1)+`)`, args...)
		if err != nil {
			return nil, mapErr(err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, mapErr(err)
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, mapErr(err)
		}
	}
	return out, nil
}

// Bulk entry actions.
const (
	BulkDelete     = "delete"
	BulkAddTags    = "add_tags"
	BulkRemoveTags = "remove_tags"
	BulkSetTags    = "set_tags"
)

// BulkEntries applies action to the given entries of a book and returns how
// many entries were affected (ids not in the book are ignored). Added tag
// names are created with DefaultTagColor.
func (r *AddressBooks) BulkEntries(ctx context.Context, bookID int64, ids []int64, action string, tags []string) (int, error) {
	var affected int
	err := r.atomic(ctx, func(r *AddressBooks) error {
		found, err := r.existingEntryIDs(ctx, bookID, ids)
		if err != nil {
			return err
		}
		affected = len(found)
		if affected == 0 {
			return nil
		}
		now := toMs(r.now())
		tags = normalizeTags(tags)
		tagIDs := make([]int64, 0, len(tags))
		if action == BulkAddTags || action == BulkSetTags {
			for _, name := range tags {
				id, err := r.ensureTag(ctx, bookID, name, DefaultTagColor)
				if err != nil {
					return err
				}
				tagIDs = append(tagIDs, id)
			}
		}
		for _, id := range found {
			var stmts []string
			switch action {
			case BulkDelete:
				stmts = []string{`DELETE FROM address_book_entries WHERE id = ?`}
			case BulkSetTags:
				stmts = []string{`DELETE FROM address_book_entry_tags WHERE entry_id = ?`}
			case BulkRemoveTags:
				for _, name := range tags {
					if _, err := r.q.ExecContext(ctx, `DELETE FROM address_book_entry_tags WHERE entry_id = ?
						AND tag_id = (SELECT id FROM address_book_tags WHERE address_book_id = ? AND name = ?)`,
						id, bookID, name); err != nil {
						return mapErr(err)
					}
				}
			case BulkAddTags:
			default:
				return fmt.Errorf("%w: unknown bulk action %q", ErrInvalid, action)
			}
			for _, s := range stmts {
				if _, err := r.q.ExecContext(ctx, s, id); err != nil {
					return mapErr(err)
				}
			}
			if action == BulkDelete {
				continue
			}
			for _, tid := range tagIDs {
				if _, err := r.q.ExecContext(ctx, `INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)
					ON CONFLICT DO NOTHING`, id, tid); err != nil {
					return mapErr(err)
				}
			}
			if _, err := r.q.ExecContext(ctx, `UPDATE address_book_entries SET updated_at = ? WHERE id = ?`, now, id); err != nil {
				return mapErr(err)
			}
		}
		return r.touchBook(ctx, bookID)
	})
	return affected, err
}

// ---- tags by id ----

// ABTagInfo is a tag with the number of entries carrying it.
type ABTagInfo struct {
	ABTag
	EntryCount int
}

const tagInfoSelect = `SELECT t.id, t.address_book_id, t.name, t.color_argb,
	(SELECT count(*) FROM address_book_entry_tags et WHERE et.tag_id = t.id) FROM address_book_tags t`

func scanTagInfo(row interface{ Scan(...any) error }) (*ABTagInfo, error) {
	var (
		t     ABTagInfo
		color int64
	)
	if err := row.Scan(&t.ID, &t.AddressBookID, &t.Name, &color, &t.EntryCount); err != nil {
		return nil, mapErr(err)
	}
	t.Color = uint32(color)
	return &t, nil
}

// ListTagInfos returns a book's tags ordered by name, with entry counts.
func (r *AddressBooks) ListTagInfos(ctx context.Context, bookID int64) ([]*ABTagInfo, error) {
	rows, err := r.q.QueryContext(ctx, tagInfoSelect+` WHERE t.address_book_id = ? ORDER BY t.name, t.id`, bookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*ABTagInfo{}
	for rows.Next() {
		t, err := scanTagInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, mapErr(rows.Err())
}

// GetTagInfo returns tag id of a book.
func (r *AddressBooks) GetTagInfo(ctx context.Context, bookID, id int64) (*ABTagInfo, error) {
	return scanTagInfo(r.q.QueryRowContext(ctx, tagInfoSelect+` WHERE t.address_book_id = ? AND t.id = ?`, bookID, id))
}

// CreateTag adds a tag (ErrConflict when the name exists) and returns its id.
func (r *AddressBooks) CreateTag(ctx context.Context, bookID int64, name string, color uint32) (int64, error) {
	now := toMs(r.now())
	var id int64
	err := r.q.QueryRowContext(ctx, `INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at,
		updated_at) VALUES (?, ?, ?, ?, ?) RETURNING id`, bookID, name, int64(color), now, now).Scan(&id)
	if err == nil {
		err = r.touchBook(ctx, bookID)
	}
	return id, mapErr(err)
}

// UpdateTagByID renames and/or recolours tag id (entries keep the link).
func (r *AddressBooks) UpdateTagByID(ctx context.Context, bookID, id int64, name *string, color *uint32) error {
	sets, args := []string{"updated_at = ?"}, []any{toMs(r.now())}
	if name != nil {
		sets, args = append(sets, "name = ?"), append(args, *name)
	}
	if color != nil {
		sets, args = append(sets, "color_argb = ?"), append(args, int64(*color))
	}
	err := expectOne(r.q.ExecContext(ctx, `UPDATE address_book_tags SET `+strings.Join(sets, ", ")+`
		WHERE address_book_id = ? AND id = ?`, append(args, bookID, id)...))
	if err == nil {
		err = r.touchBook(ctx, bookID)
	}
	return err
}

// DeleteTagByID deletes tag id (removed from every entry).
func (r *AddressBooks) DeleteTagByID(ctx context.Context, bookID, id int64) error {
	err := expectOne(r.q.ExecContext(ctx, `DELETE FROM address_book_tags WHERE address_book_id = ? AND id = ?`, bookID, id))
	if err == nil {
		err = r.touchBook(ctx, bookID)
	}
	return err
}
