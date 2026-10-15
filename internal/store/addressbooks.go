package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/db"
)

// ABPermission is the effective permission of a user on an address book.
// Values match the client's "rule" (1 read, 2 read/write, 3 full control).
type ABPermission int

// Address-book permissions.
const (
	ABNone      ABPermission = 0
	ABRead      ABPermission = 1
	ABReadWrite ABPermission = 2
	ABFull      ABPermission = 3
)

// ParseABPermission maps the address_book_shares.permission column.
func ParseABPermission(s string) ABPermission {
	switch s {
	case "read":
		return ABRead
	case "read_write":
		return ABReadWrite
	case "full":
		return ABFull
	}
	return ABNone
}

// String returns the address_book_shares.permission column value.
func (p ABPermission) String() string {
	switch p {
	case ABRead:
		return "read"
	case ABReadWrite:
		return "read_write"
	case ABFull:
		return "full"
	}
	return ""
}

// DefaultTagColor is used for tags created implicitly (a peer or legacy blob
// referencing a tag name with no colour): Flutter ARGB 0xFF9E9E9E (grey).
const DefaultTagColor uint32 = 0xFF9E9E9E

// AddressBook is a row of address_books (owner username joined in).
type AddressBook struct {
	ID          int64
	GUID        string
	OwnerUserID int64
	Name        string
	Note        string
	IsPersonal  bool
	// LegacyCollectionID is the lejianwen collection id (0 = personal).
	LegacyCollectionID *int64
	LegacyGUID         string
	// DefaultPassword is the decrypted shared-book default password ("" = none).
	DefaultPassword string
	CreatedAt       time.Time
	UpdatedAt       time.Time

	OwnerUsername string
}

// ABEntry is an address-book entry with its decrypted secrets and tag names.
type ABEntry struct {
	ID               int64
	AddressBookID    int64
	RustdeskID       string
	Alias            string
	OSUsername       string
	Hostname         string
	Platform         string
	LoginName        string
	Note             string
	Password         string
	Hash             string
	ForceAlwaysRelay bool
	RDPPort          string
	RDPUsername      string
	SameServer       bool
	Tags             []string
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// DeviceGroupName is joined from devices (same RustDesk ID), "" if unknown.
	DeviceGroupName string
}

// ABEntryPatch is a partial entry update; nil fields are left unchanged.
// Password/Hash "" clears the secret. Tags replaces the tag set (missing tags
// are created with DefaultTagColor).
type ABEntryPatch struct {
	Alias            *string
	OSUsername       *string
	Hostname         *string
	Platform         *string
	LoginName        *string
	Note             *string
	Password         *string
	Hash             *string
	ForceAlwaysRelay *bool
	RDPPort          *string
	RDPUsername      *string
	SameServer       *bool
	Tags             *[]string
}

// ABTag is a row of address_book_tags.
type ABTag struct {
	ID            int64
	AddressBookID int64
	Name          string
	Color         uint32
}

// AddressBooks is the repository for address_books and their entries, tags
// and (read side) shares. Secrets are sealed with associated data
// "ab_entry.password:<id>", "ab_entry.hash:<id>" and
// "address_book.password:<id>".
type AddressBooks struct {
	q     db.Querier
	d     *db.DB
	now   func() time.Time
	aead  *crypto.AEAD
	newID func() string
}

func (r *AddressBooks) with(q db.Querier) *AddressBooks {
	c := *r
	c.q = q
	return &c
}

func (r *AddressBooks) atomic(ctx context.Context, fn func(r *AddressBooks) error) error {
	return atomic(ctx, r.d, r.q, func(q db.Querier) error { return fn(r.with(q)) })
}

func (r *AddressBooks) seal(plain, ad string) (any, error) {
	if plain == "" {
		return nil, nil
	}
	if r.aead == nil {
		return nil, ErrNoSecrets
	}
	return r.aead.SealString(plain, ad)
}

func (r *AddressBooks) open(ct []byte, ad string) (string, error) {
	if len(ct) == 0 {
		return "", nil
	}
	if r.aead == nil {
		return "", ErrNoSecrets
	}
	pt, err := r.aead.OpenString(ct, ad)
	if err != nil {
		return "", fmt.Errorf("store: %s: %w", ad, err)
	}
	return pt, nil
}

func entryPasswordAD(id int64) string { return "ab_entry.password:" + strconv.FormatInt(id, 10) }
func entryHashAD(id int64) string     { return "ab_entry.hash:" + strconv.FormatInt(id, 10) }
func bookPasswordAD(id int64) string  { return "address_book.password:" + strconv.FormatInt(id, 10) }

const bookColumns = `b.id, b.guid, b.owner_user_id, b.name, b.note, b.is_personal, b.legacy_collection_id,
	b.legacy_guid, b.default_password_enc, b.created_at, b.updated_at, coalesce(u.username, '')`

const bookFrom = ` FROM address_books b LEFT JOIN users u ON u.id = b.owner_user_id`

func (r *AddressBooks) scanBook(row interface{ Scan(...any) error }) (*AddressBook, error) {
	var (
		b                    AddressBook
		personal             int
		legacyCID            sql.NullInt64
		legacyGUID           sql.NullString
		pwEnc                []byte
		createdAt, updatedAt int64
	)
	err := row.Scan(&b.ID, &b.GUID, &b.OwnerUserID, &b.Name, &b.Note, &personal, &legacyCID, &legacyGUID, &pwEnc,
		&createdAt, &updatedAt, &b.OwnerUsername)
	if err != nil {
		return nil, mapErr(err)
	}
	b.IsPersonal = personal == 1
	b.LegacyCollectionID, b.LegacyGUID = ptrID(legacyCID), legacyGUID.String
	b.CreatedAt, b.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	if b.DefaultPassword, err = r.open(pwEnc, bookPasswordAD(b.ID)); err != nil {
		return nil, err
	}
	return &b, nil
}

// Create inserts b (a preset ID is honoured for legacy import). An empty GUID
// is generated. ID, GUID and timestamps are set.
func (r *AddressBooks) Create(ctx context.Context, b *AddressBook) error {
	return r.atomic(ctx, func(r *AddressBooks) error {
		now := r.now()
		if b.CreatedAt.IsZero() {
			b.CreatedAt = now
		}
		b.UpdatedAt = now
		if b.GUID == "" {
			b.GUID = r.newID()
		}
		var id sql.NullInt64
		if b.ID != 0 {
			id = sql.NullInt64{Int64: b.ID, Valid: true}
		}
		err := r.q.QueryRowContext(ctx, `INSERT INTO address_books (id, guid, owner_user_id, name, note, is_personal,
			legacy_collection_id, legacy_guid, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			id, b.GUID, b.OwnerUserID, b.Name, b.Note, boolInt(b.IsPersonal), nullID(b.LegacyCollectionID),
			nullStr(b.LegacyGUID), toMs(b.CreatedAt), toMs(b.UpdatedAt)).Scan(&b.ID)
		if err != nil {
			return mapErr(err)
		}
		return r.setDefaultPassword(ctx, b.ID, b.DefaultPassword)
	})
}

func (r *AddressBooks) setDefaultPassword(ctx context.Context, id int64, pw string) error {
	enc, err := r.seal(pw, bookPasswordAD(id))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `UPDATE address_books SET default_password_enc = ? WHERE id = ?`, enc, id)
	return mapErr(err)
}

// Get returns the book with id.
func (r *AddressBooks) Get(ctx context.Context, id int64) (*AddressBook, error) {
	return r.scanBook(r.q.QueryRowContext(ctx, `SELECT `+bookColumns+bookFrom+` WHERE b.id = ?`, id))
}

// GetByGUID resolves a client-supplied guid: the stable guid, a stored legacy
// guid, or (fallback) a legacy "<group_id>-<user_id>-<collection_id>" guid
// derived from the owner and legacy collection id. The group id part is
// ignored so moving a user between groups does not break cached guids.
func (r *AddressBooks) GetByGUID(ctx context.Context, guid string) (*AddressBook, error) {
	guid = strings.TrimSpace(guid)
	if guid == "" {
		return nil, ErrNotFound
	}
	b, err := r.scanBook(r.q.QueryRowContext(ctx, `SELECT `+bookColumns+bookFrom+`
		WHERE b.guid = ? OR b.legacy_guid = ? ORDER BY b.guid = ? DESC LIMIT 1`, guid, guid, guid))
	if !errors.Is(err, ErrNotFound) {
		return b, err
	}
	_, userID, collID, ok := ParseLegacyGUID(guid)
	if !ok {
		return nil, ErrNotFound
	}
	if collID == 0 {
		return r.scanBook(r.q.QueryRowContext(ctx, `SELECT `+bookColumns+bookFrom+`
			WHERE b.owner_user_id = ? AND b.is_personal = 1`, userID))
	}
	return r.scanBook(r.q.QueryRowContext(ctx, `SELECT `+bookColumns+bookFrom+`
		WHERE b.owner_user_id = ? AND b.legacy_collection_id = ?`, userID, collID))
}

// ParseLegacyGUID splits a lejianwen guid "<group_id>-<user_id>-<collection_id>".
func ParseLegacyGUID(guid string) (groupID, userID, collectionID int64, ok bool) {
	parts := strings.Split(guid, "-")
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	var nums [3]int64
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 || (p != "0" && strings.HasPrefix(p, "0")) {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	if nums[1] == 0 {
		return 0, 0, 0, false
	}
	return nums[0], nums[1], nums[2], true
}

// GetPersonal returns the personal book of userID.
func (r *AddressBooks) GetPersonal(ctx context.Context, userID int64) (*AddressBook, error) {
	return r.scanBook(r.q.QueryRowContext(ctx, `SELECT `+bookColumns+bookFrom+`
		WHERE b.owner_user_id = ? AND b.is_personal = 1`, userID))
}

// EnsurePersonal returns the personal book of userID, creating it if needed.
func (r *AddressBooks) EnsurePersonal(ctx context.Context, userID int64) (*AddressBook, error) {
	b, err := r.GetPersonal(ctx, userID)
	if !errors.Is(err, ErrNotFound) {
		return b, err
	}
	nb := &AddressBook{OwnerUserID: userID, IsPersonal: true}
	if err := r.Create(ctx, nb); err != nil {
		if errors.Is(err, ErrConflict) { // created concurrently
			return r.GetPersonal(ctx, userID)
		}
		return nil, err
	}
	return r.Get(ctx, nb.ID)
}

// FindSharedByName returns the non-personal books named name (case-insensitive).
func (r *AddressBooks) FindSharedByName(ctx context.Context, name string) ([]*AddressBook, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+bookColumns+bookFrom+`
		WHERE b.is_personal = 0 AND b.name = ? COLLATE NOCASE ORDER BY b.id`, strings.TrimSpace(name))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []*AddressBook
	for rows.Next() {
		b, err := r.scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, mapErr(rows.Err())
}

// AddShare grants a user (userID) or a user group (groupID) perm on a book,
// replacing an existing grant for the same grantee.
func (r *AddressBooks) AddShare(ctx context.Context, bookID int64, userID, groupID *int64, perm ABPermission) error {
	if (userID == nil) == (groupID == nil) {
		return fmt.Errorf("%w: exactly one grantee required", ErrInvalid)
	}
	now := toMs(r.now())
	conflict := `(address_book_id, grantee_user_id) WHERE grantee_user_id IS NOT NULL`
	if groupID != nil {
		conflict = `(address_book_id, grantee_group_id) WHERE grantee_group_id IS NOT NULL`
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO address_book_shares (address_book_id, grantee_user_id,
		grantee_group_id, permission, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT `+conflict+` DO UPDATE SET permission = excluded.permission, updated_at = excluded.updated_at`,
		bookID, nullID(userID), nullID(groupID), perm.String(), now, now)
	return mapErr(err)
}

// Permission returns the effective permission of userID on book: the owner
// has full control; otherwise the maximum of the user's own grant and the
// grant to the user's group.
func (r *AddressBooks) Permission(ctx context.Context, book *AddressBook, userID int64) (ABPermission, error) {
	if book.OwnerUserID == userID {
		return ABFull, nil
	}
	rows, err := r.q.QueryContext(ctx, `SELECT s.permission FROM address_book_shares s
		WHERE s.address_book_id = ? AND (s.grantee_user_id = ?
		   OR s.grantee_group_id = (SELECT user_group_id FROM users WHERE id = ?))`, book.ID, userID, userID)
	if err != nil {
		return ABNone, mapErr(err)
	}
	defer rows.Close()
	best := ABNone
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return ABNone, mapErr(err)
		}
		best = max(best, ParseABPermission(p))
	}
	return best, mapErr(rows.Err())
}

// SharedBook is a non-personal book visible to a user with its effective rule.
type SharedBook struct {
	*AddressBook
	Rule ABPermission
}

// ListShared returns the non-personal books userID owns or that are shared
// with the user or the user's group, ordered by name, and the total count.
func (r *AddressBooks) ListShared(ctx context.Context, userID int64, limit, offset int) ([]SharedBook, int, error) {
	// rule: 3 for owned books, else the best grant.
	const ruleExpr = `CASE WHEN b.owner_user_id = ?1 THEN 3 ELSE (SELECT max(CASE s.permission
		WHEN 'full' THEN 3 WHEN 'read_write' THEN 2 WHEN 'read' THEN 1 ELSE 0 END)
		FROM address_book_shares s WHERE s.address_book_id = b.id AND (s.grantee_user_id = ?1
		OR s.grantee_group_id = (SELECT user_group_id FROM users WHERE id = ?1))) END`
	where := ` WHERE b.is_personal = 0 AND (` + ruleExpr + `) > 0`
	var total int
	if err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_books b`+where, userID).Scan(&total); err != nil {
		return nil, 0, mapErr(err)
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+bookColumns+`, `+ruleExpr+bookFrom+where+`
		ORDER BY b.name, b.id LIMIT ?2 OFFSET ?3`, userID, clampLimit(limit, 100, 1000), max(offset, 0))
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer rows.Close()
	var out []SharedBook
	for rows.Next() {
		var rule int
		var sb SharedBook
		sb.AddressBook, err = r.scanBook(scanFunc(func(dest ...any) error {
			return rows.Scan(append(dest, &rule)...)
		}))
		if err != nil {
			return nil, 0, err
		}
		sb.Rule = ABPermission(rule)
		out = append(out, sb)
	}
	return out, total, mapErr(rows.Err())
}

type scanFunc func(dest ...any) error

func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

// ---- entries ----

const entryColumns = `e.id, e.address_book_id, e.rustdesk_id, e.alias, e.os_username, e.hostname, e.platform,
	e.login_name, e.note, e.password_enc, e.hash_enc, e.force_always_relay, e.rdp_port, e.rdp_username,
	e.same_server, e.created_at, e.updated_at, coalesce(g.name, '')`

const entryFrom = ` FROM address_book_entries e
	LEFT JOIN devices d ON d.rustdesk_id = e.rustdesk_id
	LEFT JOIN device_groups g ON g.id = d.device_group_id`

func (r *AddressBooks) scanEntry(row interface{ Scan(...any) error }) (*ABEntry, error) {
	var (
		e                    ABEntry
		note                 sql.NullString
		pwEnc, hashEnc       []byte
		relay, same          int
		createdAt, updatedAt int64
	)
	err := row.Scan(&e.ID, &e.AddressBookID, &e.RustdeskID, &e.Alias, &e.OSUsername, &e.Hostname, &e.Platform,
		&e.LoginName, &note, &pwEnc, &hashEnc, &relay, &e.RDPPort, &e.RDPUsername, &same, &createdAt, &updatedAt,
		&e.DeviceGroupName)
	if err != nil {
		return nil, mapErr(err)
	}
	e.Note = note.String
	e.ForceAlwaysRelay, e.SameServer = relay == 1, same == 1
	e.CreatedAt, e.UpdatedAt = fromMs(createdAt), fromMs(updatedAt)
	if e.Password, err = r.open(pwEnc, entryPasswordAD(e.ID)); err != nil {
		return nil, err
	}
	if e.Hash, err = r.open(hashEnc, entryHashAD(e.ID)); err != nil {
		return nil, err
	}
	e.Tags = []string{}
	return &e, nil
}

// CountEntries returns the number of entries in a book.
func (r *AddressBooks) CountEntries(ctx context.Context, bookID int64) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT count(*) FROM address_book_entries WHERE address_book_id = ?`,
		bookID).Scan(&n)
	return n, mapErr(err)
}

// ListEntries returns a page of a book's entries (ordered by id, tags sorted
// by name) and the total count. limit <= 0 returns every entry.
func (r *AddressBooks) ListEntries(ctx context.Context, bookID int64, limit, offset int) ([]*ABEntry, int, error) {
	total, err := r.CountEntries(ctx, bookID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = -1
	}
	rows, err := r.q.QueryContext(ctx, `SELECT `+entryColumns+entryFrom+`
		WHERE e.address_book_id = ? ORDER BY e.id LIMIT ? OFFSET ?`, bookID, limit, max(offset, 0))
	if err != nil {
		return nil, 0, mapErr(err)
	}
	var (
		out  []*ABEntry
		byID = map[int64]*ABEntry{}
	)
	for rows.Next() {
		e, err := r.scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, 0, err
		}
		out = append(out, e)
		byID[e.ID] = e
	}
	if err := rows.Close(); err != nil {
		return nil, 0, mapErr(err)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, mapErr(err)
	}
	if len(out) == 0 {
		return out, total, nil
	}
	tagRows, err := r.q.QueryContext(ctx, `SELECT et.entry_id, t.name FROM address_book_entry_tags et
		JOIN address_book_tags t ON t.id = et.tag_id
		WHERE t.address_book_id = ? AND et.entry_id BETWEEN ? AND ? ORDER BY t.name, t.id`,
		bookID, out[0].ID, out[len(out)-1].ID)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer tagRows.Close()
	for tagRows.Next() {
		var (
			id   int64
			name string
		)
		if err := tagRows.Scan(&id, &name); err != nil {
			return nil, 0, mapErr(err)
		}
		if e := byID[id]; e != nil {
			e.Tags = append(e.Tags, name)
		}
	}
	return out, total, mapErr(tagRows.Err())
}

// GetEntry returns the entry for rustdeskID in a book (with tags).
func (r *AddressBooks) GetEntry(ctx context.Context, bookID int64, rustdeskID string) (*ABEntry, error) {
	e, err := r.scanEntry(r.q.QueryRowContext(ctx, `SELECT `+entryColumns+entryFrom+`
		WHERE e.address_book_id = ? AND e.rustdesk_id = ?`, bookID, rustdeskID))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.QueryContext(ctx, `SELECT t.name FROM address_book_entry_tags et
		JOIN address_book_tags t ON t.id = et.tag_id WHERE et.entry_id = ? ORDER BY t.name, t.id`, e.ID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, mapErr(err)
		}
		e.Tags = append(e.Tags, name)
	}
	return e, mapErr(rows.Err())
}

// UpsertEntry creates the entry for rustdeskID (or updates it when it already
// exists) and applies p. It reports whether a new entry was created.
func (r *AddressBooks) UpsertEntry(ctx context.Context, bookID int64, rustdeskID string, p ABEntryPatch) (bool, error) {
	created := false
	err := r.atomic(ctx, func(r *AddressBooks) error {
		now := toMs(r.now())
		var id int64
		err := r.q.QueryRowContext(ctx, `SELECT id FROM address_book_entries WHERE address_book_id = ? AND rustdesk_id = ?`,
			bookID, rustdeskID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if err = r.q.QueryRowContext(ctx, `INSERT INTO address_book_entries (address_book_id, rustdesk_id,
				created_at, updated_at) VALUES (?, ?, ?, ?) RETURNING id`, bookID, rustdeskID, now, now).Scan(&id); err != nil {
				return mapErr(err)
			}
			created = true
		} else if err != nil {
			return mapErr(err)
		}
		return r.patchEntry(ctx, bookID, id, p)
	})
	return created, err
}

// PatchEntry applies p to the existing entry for rustdeskID (ErrNotFound if absent).
func (r *AddressBooks) PatchEntry(ctx context.Context, bookID int64, rustdeskID string, p ABEntryPatch) error {
	return r.atomic(ctx, func(r *AddressBooks) error {
		var id int64
		if err := r.q.QueryRowContext(ctx, `SELECT id FROM address_book_entries WHERE address_book_id = ? AND rustdesk_id = ?`,
			bookID, rustdeskID).Scan(&id); err != nil {
			return mapErr(err)
		}
		return r.patchEntry(ctx, bookID, id, p)
	})
}

func (r *AddressBooks) patchEntry(ctx context.Context, bookID, id int64, p ABEntryPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{toMs(r.now())}
	str := func(col string, v *string) {
		if v != nil {
			sets, args = append(sets, col+" = ?"), append(args, *v)
		}
	}
	str("alias", p.Alias)
	str("os_username", p.OSUsername)
	str("hostname", p.Hostname)
	str("platform", p.Platform)
	str("login_name", p.LoginName)
	str("note", p.Note)
	str("rdp_port", p.RDPPort)
	str("rdp_username", p.RDPUsername)
	if p.ForceAlwaysRelay != nil {
		sets, args = append(sets, "force_always_relay = ?"), append(args, boolInt(*p.ForceAlwaysRelay))
	}
	if p.SameServer != nil {
		sets, args = append(sets, "same_server = ?"), append(args, boolInt(*p.SameServer))
	}
	if p.Password != nil {
		enc, err := r.seal(*p.Password, entryPasswordAD(id))
		if err != nil {
			return err
		}
		sets, args = append(sets, "password_enc = ?"), append(args, enc)
	}
	if p.Hash != nil {
		enc, err := r.seal(*p.Hash, entryHashAD(id))
		if err != nil {
			return err
		}
		sets, args = append(sets, "hash_enc = ?"), append(args, enc)
	}
	if err := expectOne(r.q.ExecContext(ctx, `UPDATE address_book_entries SET `+strings.Join(sets, ", ")+
		` WHERE id = ?`, append(args, id)...)); err != nil {
		return err
	}
	if p.Tags == nil {
		return nil
	}
	if _, err := r.q.ExecContext(ctx, `DELETE FROM address_book_entry_tags WHERE entry_id = ?`, id); err != nil {
		return mapErr(err)
	}
	for _, name := range normalizeTags(*p.Tags) {
		tagID, err := r.ensureTag(ctx, bookID, name, DefaultTagColor)
		if err != nil {
			return err
		}
		if _, err := r.q.ExecContext(ctx, `INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)
			ON CONFLICT DO NOTHING`, id, tagID); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func normalizeTags(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ensureTag returns the id of tag name in a book, creating it with color.
func (r *AddressBooks) ensureTag(ctx context.Context, bookID int64, name string, color uint32) (int64, error) {
	var id int64
	err := r.q.QueryRowContext(ctx, `SELECT id FROM address_book_tags WHERE address_book_id = ? AND name = ?`,
		bookID, name).Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return id, mapErr(err)
	}
	now := toMs(r.now())
	err = r.q.QueryRowContext(ctx, `INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at,
		updated_at) VALUES (?, ?, ?, ?, ?) RETURNING id`, bookID, name, int64(color), now, now).Scan(&id)
	return id, mapErr(err)
}

// DeleteEntries removes the entries with the given RustDesk IDs and returns
// how many were deleted.
func (r *AddressBooks) DeleteEntries(ctx context.Context, bookID int64, rustdeskIDs []string) (int64, error) {
	var n int64
	err := r.atomic(ctx, func(r *AddressBooks) error {
		for _, id := range rustdeskIDs {
			res, err := r.q.ExecContext(ctx, `DELETE FROM address_book_entries WHERE address_book_id = ? AND rustdesk_id = ?`,
				bookID, id)
			if err != nil {
				return mapErr(err)
			}
			c, _ := res.RowsAffected()
			n += c
		}
		return nil
	})
	return n, err
}

// LegacyEntry is one peer of a legacy address-book blob.
type LegacyEntry struct {
	RustdeskID string
	Patch      ABEntryPatch
}

// ReplaceAll makes a book match a legacy full-replace push (POST /api/ab):
// listed entries are created or patched (absent keys keep their values),
// unlisted entries are deleted, and the tag set becomes tags plus every tag an
// entry references, with colours from colors (existing colour or
// DefaultTagColor when missing).
func (r *AddressBooks) ReplaceAll(ctx context.Context, bookID int64, entries []LegacyEntry, tags []string, colors map[string]uint32) error {
	return r.atomic(ctx, func(r *AddressBooks) error {
		keep := map[string]bool{}
		wantTags := normalizeTags(tags)
		for _, e := range entries {
			if e.Patch.Tags != nil {
				wantTags = normalizeTags(append(wantTags, *e.Patch.Tags...))
			}
		}
		now := toMs(r.now())
		tagSet := map[string]bool{}
		for _, name := range wantTags {
			tagSet[name] = true
			color, hasColor := colors[name]
			if !hasColor {
				color = DefaultTagColor
			}
			id, err := r.ensureTag(ctx, bookID, name, color)
			if err != nil {
				return err
			}
			if hasColor {
				if _, err := r.q.ExecContext(ctx, `UPDATE address_book_tags SET color_argb = ?, updated_at = ?
					WHERE id = ? AND color_argb <> ?`, int64(color), now, id, int64(color)); err != nil {
					return mapErr(err)
				}
			}
		}
		existing, err := r.ListTags(ctx, bookID)
		if err != nil {
			return err
		}
		var drop []string
		for _, t := range existing {
			if !tagSet[t.Name] {
				drop = append(drop, t.Name)
			}
		}
		if _, err := r.DeleteTags(ctx, bookID, drop); err != nil {
			return err
		}
		for _, e := range entries {
			if e.RustdeskID == "" || keep[e.RustdeskID] {
				continue
			}
			keep[e.RustdeskID] = true
			if _, err := r.UpsertEntry(ctx, bookID, e.RustdeskID, e.Patch); err != nil {
				return err
			}
		}
		rows, err := r.q.QueryContext(ctx, `SELECT rustdesk_id FROM address_book_entries WHERE address_book_id = ?`, bookID)
		if err != nil {
			return mapErr(err)
		}
		var gone []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return mapErr(err)
			}
			if !keep[id] {
				gone = append(gone, id)
			}
		}
		if err := rows.Close(); err != nil {
			return mapErr(err)
		}
		_, err = r.DeleteEntries(ctx, bookID, gone)
		return err
	})
}

// ---- tags ----

// ListTags returns a book's tags ordered by id (creation order).
func (r *AddressBooks) ListTags(ctx context.Context, bookID int64) ([]*ABTag, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT id, address_book_id, name, color_argb FROM address_book_tags
		WHERE address_book_id = ? ORDER BY id`, bookID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []*ABTag{}
	for rows.Next() {
		var (
			t     ABTag
			color int64
		)
		if err := rows.Scan(&t.ID, &t.AddressBookID, &t.Name, &color); err != nil {
			return nil, mapErr(err)
		}
		t.Color = uint32(color)
		out = append(out, &t)
	}
	return out, mapErr(rows.Err())
}

// UpsertTag creates tag name with color, or updates the colour of an existing tag.
func (r *AddressBooks) UpsertTag(ctx context.Context, bookID int64, name string, color uint32) error {
	now := toMs(r.now())
	_, err := r.q.ExecContext(ctx, `INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at,
		updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (address_book_id, name)
		DO UPDATE SET color_argb = excluded.color_argb, updated_at = excluded.updated_at`,
		bookID, name, int64(color), now, now)
	return mapErr(err)
}

// SetTagColor changes the colour of an existing tag (ErrNotFound if absent).
func (r *AddressBooks) SetTagColor(ctx context.Context, bookID int64, name string, color uint32) error {
	return expectOne(r.q.ExecContext(ctx, `UPDATE address_book_tags SET color_argb = ?, updated_at = ?
		WHERE address_book_id = ? AND name = ?`, int64(color), toMs(r.now()), bookID, name))
}

// RenameTag renames a tag; entries reference tags by id, so their tag lists
// follow. ErrNotFound when old is absent, ErrConflict when newName exists.
func (r *AddressBooks) RenameTag(ctx context.Context, bookID int64, old, newName string) error {
	if old == newName {
		var id int64
		return mapErr(r.q.QueryRowContext(ctx, `SELECT id FROM address_book_tags WHERE address_book_id = ? AND name = ?`,
			bookID, old).Scan(&id))
	}
	return expectOne(r.q.ExecContext(ctx, `UPDATE address_book_tags SET name = ?, updated_at = ?
		WHERE address_book_id = ? AND name = ?`, newName, toMs(r.now()), bookID, old))
}

// DeleteTags removes tags by name (entries lose them via cascade) and returns
// how many were deleted.
func (r *AddressBooks) DeleteTags(ctx context.Context, bookID int64, names []string) (int64, error) {
	var n int64
	for _, name := range names {
		res, err := r.q.ExecContext(ctx, `DELETE FROM address_book_tags WHERE address_book_id = ? AND name = ?`, bookID, name)
		if err != nil {
			return n, mapErr(err)
		}
		c, _ := res.RowsAffected()
		n += c
	}
	return n, nil
}
