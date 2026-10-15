package adminapi

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Address books (/address-books/*). Effective permissions are the client
// API's (store.AddressBooks.Permission: owner = full, else the best grant to
// the user or the user's group) plus the panel rule that admins have full
// control of every book. As in the client API, deleting entries or tags
// requires full control; adding/editing requires read/write.

func (c *b2bCore) registerBooks(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireUser)
		r.Get("/address-books", c.handleListBooks)
		r.Post("/address-books", c.handleCreateBook)
		r.Get("/address-books/{book_id}", c.handleGetBook)
		r.Patch("/address-books/{book_id}", c.handleUpdateBook)
		r.Delete("/address-books/{book_id}", c.handleDeleteBook)
		r.Get("/address-books/{book_id}/shares", c.handleListShares)
		r.Post("/address-books/{book_id}/shares", c.handleCreateShare)
		r.Patch("/address-books/{book_id}/shares/{share_id}", c.handleUpdateShare)
		r.Delete("/address-books/{book_id}/shares/{share_id}", c.handleDeleteShare)
		r.Get("/address-books/{book_id}/entries", c.handleListEntries)
		r.Post("/address-books/{book_id}/entries", c.handleCreateEntry)
		r.Post("/address-books/{book_id}/entries/bulk", c.handleBulkEntries)
		r.Post("/address-books/{book_id}/entries/from-devices", c.handleEntriesFromDevices)
		r.Patch("/address-books/{book_id}/entries/{entry_id}", c.handleUpdateEntry)
		r.Delete("/address-books/{book_id}/entries/{entry_id}", c.handleDeleteEntry)
		r.Get("/address-books/{book_id}/tags", c.handleListTags)
		r.Post("/address-books/{book_id}/tags", c.handleCreateTag)
		r.Patch("/address-books/{book_id}/tags/{tag_id}", c.handleUpdateTag)
		r.Delete("/address-books/{book_id}/tags/{tag_id}", c.handleDeleteTag)
	})
}

// ---- DTOs ----

// bookOwnerDTO is components.schemas.UserRef.
type bookOwnerDTO struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// bookDTO is components.schemas.AddressBook.
type bookDTO struct {
	ID                 int64        `json:"id"`
	GUID               string       `json:"guid"`
	OwnerUserID        int64        `json:"owner_user_id"`
	Owner              bookOwnerDTO `json:"owner"`
	Name               string       `json:"name"`
	Note               string       `json:"note"`
	IsPersonal         bool         `json:"is_personal"`
	Permission         string       `json:"permission"`
	EntryCount         int          `json:"entry_count"`
	ShareCount         int          `json:"share_count"`
	HasDefaultPassword bool         `json:"has_default_password"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
}

func newBookDTO(b store.BookListItem, perm store.ABPermission) bookDTO {
	return bookDTO{ID: b.ID, GUID: b.GUID, OwnerUserID: b.OwnerUserID,
		Owner: bookOwnerDTO{ID: b.OwnerUserID, Username: b.OwnerUsername, DisplayName: b.OwnerDisplayName},
		Name:  b.Name, Note: b.Note, IsPersonal: b.IsPersonal, Permission: perm.String(), EntryCount: b.EntryCount,
		ShareCount: b.ShareCount, HasDefaultPassword: !b.IsPersonal && b.DefaultPassword != "",
		CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
}

// shareGranteeDTO is components.schemas.GroupRef (name = username or group name).
type shareGranteeDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// shareDTO is components.schemas.AddressBookShare.
type shareDTO struct {
	ID            int64           `json:"id"`
	AddressBookID int64           `json:"address_book_id"`
	GranteeType   string          `json:"grantee_type"`
	Grantee       shareGranteeDTO `json:"grantee"`
	Permission    string          `json:"permission"`
	CreatedAt     time.Time       `json:"created_at"`
}

func newShareDTO(s *store.ABShare) shareDTO {
	d := shareDTO{ID: s.ID, AddressBookID: s.AddressBookID, Permission: s.Permission.String(), CreatedAt: s.CreatedAt}
	if s.GranteeUserID != nil {
		d.GranteeType, d.Grantee = "user", shareGranteeDTO{ID: *s.GranteeUserID, Name: s.GranteeName}
	} else if s.GranteeGroupID != nil {
		d.GranteeType, d.Grantee = "group", shareGranteeDTO{ID: *s.GranteeGroupID, Name: s.GranteeName}
	}
	return d
}

// entryDTO is components.schemas.Entry. Password and hash are write-only.
type entryDTO struct {
	ID               int64     `json:"id"`
	AddressBookID    int64     `json:"address_book_id"`
	RustdeskID       string    `json:"rustdesk_id"`
	Alias            string    `json:"alias"`
	OSUsername       string    `json:"os_username"`
	Hostname         string    `json:"hostname"`
	Platform         string    `json:"platform"`
	LoginName        string    `json:"login_name"`
	Note             *string   `json:"note"`
	Tags             []string  `json:"tags"`
	HasPassword      bool      `json:"has_password"`
	ForceAlwaysRelay bool      `json:"force_always_relay"`
	RDPPort          string    `json:"rdp_port"`
	RDPUsername      string    `json:"rdp_username"`
	SameServer       bool      `json:"same_server"`
	Online           *bool     `json:"online"`
	DeviceID         *int64    `json:"device_id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (c *b2bCore) entryDTO(e *store.ABEntry) entryDTO {
	d := entryDTO{ID: e.ID, AddressBookID: e.AddressBookID, RustdeskID: e.RustdeskID, Alias: e.Alias,
		OSUsername: e.OSUsername, Hostname: e.Hostname, Platform: e.Platform, LoginName: e.LoginName,
		Note: strPtr(e.Note), Tags: e.Tags, HasPassword: e.Password != "" || e.Hash != "",
		ForceAlwaysRelay: e.ForceAlwaysRelay, RDPPort: e.RDPPort, RDPUsername: e.RDPUsername,
		SameServer: e.SameServer, DeviceID: e.DeviceID, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if e.DeviceID != nil {
		on := c.d.Presence != nil && c.d.Presence.IsOnline(e.RustdeskID)
		d.Online = &on
	}
	return d
}

// tagDTO is components.schemas.Tag (color_argb: Flutter ARGB uint32).
type tagDTO struct {
	ID            int64  `json:"id"`
	AddressBookID int64  `json:"address_book_id"`
	Name          string `json:"name"`
	ColorARGB     int64  `json:"color_argb"`
	EntryCount    int    `json:"entry_count"`
}

func newTagDTO(t *store.ABTagInfo) tagDTO {
	return tagDTO{ID: t.ID, AddressBookID: t.AddressBookID, Name: t.Name, ColorARGB: int64(t.Color), EntryCount: t.EntryCount}
}

// ---- access ----

// bookAccess is a loaded book and the caller's effective permission.
type bookAccess struct {
	book store.BookListItem
	perm store.ABPermission
	user *store.User
}

// effectivePerm applies the panel admin override to the client-API rule.
func effectivePerm(u *store.User, rule store.ABPermission) store.ABPermission {
	if u.IsAdmin() {
		return store.ABFull
	}
	return rule
}

// loadBook resolves {book_id} and checks the caller has at least need:
// 404 when the caller has no access at all, 403 when it is insufficient.
func (c *b2bCore) loadBook(w http.ResponseWriter, r *http.Request, need store.ABPermission) *bookAccess {
	u := CurrentUser(r.Context())
	id, ok := idParam(chi.URLParam(r, "book_id"))
	if !ok {
		httpx.NotFound(w, r)
		return nil
	}
	items, _, err := c.d.Store.AddressBooks.ListBooks(r.Context(), store.BookListFilter{ViewerID: u.ID, All: true, ID: id, Limit: 1})
	if err != nil {
		c.fail(w, r, "load address book", err)
		return nil
	}
	if len(items) == 0 {
		httpx.NotFound(w, r)
		return nil
	}
	perm := effectivePerm(u, items[0].Rule)
	switch {
	case perm == store.ABNone:
		httpx.NotFound(w, r)
		return nil
	case perm < need:
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "insufficient permission on this address book")
		return nil
	}
	return &bookAccess{book: items[0], perm: perm, user: u}
}

func (c *b2bCore) reloadBook(w http.ResponseWriter, r *http.Request, id int64, status int) {
	u := CurrentUser(r.Context())
	items, _, err := c.d.Store.AddressBooks.ListBooks(r.Context(), store.BookListFilter{ViewerID: u.ID, All: true, ID: id, Limit: 1})
	if err == nil && len(items) == 0 {
		err = store.ErrNotFound
	}
	if err != nil {
		c.fail(w, r, "reload address book", err)
		return
	}
	httpx.WriteJSON(w, status, newBookDTO(items[0], effectivePerm(u, items[0].Rule)))
}

func personalBook(w http.ResponseWriter, msg string) {
	httpx.WriteError(w, http.StatusConflict, "personal_book", msg)
}

// ---- books ----

func (c *b2bCore) handleListBooks(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	q := newListQuery(r)
	p := q.paging()
	f := store.BookListFilter{ViewerID: u.ID, Q: q.search(), Kind: q.enum("kind", "personal", "shared"),
		OwnerUserID: q.int64Ptr("owner_user_id"),
		Sort:        q.sort("name", "name", "entry_count", "updated_at"), Limit: p.Size, Offset: p.offset()}
	scope := q.enum("scope", "accessible", "all")
	if !q.ok(w) {
		return
	}
	if !u.IsAdmin() && (scope == "all" || (f.OwnerUserID != nil && *f.OwnerUserID != u.ID)) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "administrator role required")
		return
	}
	f.All = scope == "all"
	items, total, err := c.d.Store.AddressBooks.ListBooks(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list address books", err)
		return
	}
	out := make([]bookDTO, 0, len(items))
	for _, b := range items {
		out = append(out, newBookDTO(b, effectivePerm(u, b.Rule)))
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

func (c *b2bCore) handleGetBook(w http.ResponseWriter, r *http.Request) {
	if a := c.loadBook(w, r, store.ABRead); a != nil {
		httpx.WriteJSON(w, http.StatusOK, newBookDTO(a.book, a.perm))
	}
}

type bookCreateReq struct {
	Name            string  `json:"name"`
	Note            string  `json:"note"`
	OwnerUserID     *int64  `json:"owner_user_id"`
	DefaultPassword *string `json:"default_password"`
}

func validBookName(s string) bool {
	return s != "" && utf8.RuneCountInString(s) <= 128
}

func (c *b2bCore) handleCreateBook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := CurrentUser(ctx)
	var req bookCreateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	switch {
	case !validBookName(req.Name):
		badRequest(w, "name must be 1-128 characters")
		return
	case utf8.RuneCountInString(req.Note) > 4096:
		badRequest(w, "note is too long")
		return
	case req.DefaultPassword != nil && len(*req.DefaultPassword) > 1024:
		badRequest(w, "default_password is too long")
		return
	}
	owner := u.ID
	if req.OwnerUserID != nil && *req.OwnerUserID != u.ID {
		if !u.IsAdmin() {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "only administrators can create books for other users")
			return
		}
		if _, err := c.d.Store.Users.GetByID(ctx, *req.OwnerUserID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				badRequest(w, "owner_user_id does not exist")
				return
			}
			c.fail(w, r, "create address book", err)
			return
		}
		owner = *req.OwnerUserID
	}
	b := &store.AddressBook{OwnerUserID: owner, Name: req.Name, Note: req.Note}
	if req.DefaultPassword != nil {
		b.DefaultPassword = *req.DefaultPassword
	}
	if err := c.d.Store.AddressBooks.CreateShared(ctx, b); err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "a shared address book with this name already exists")
			return
		}
		c.fail(w, r, "create address book", err)
		return
	}
	c.audit(r, "address_book.create", "address_book", b.ID, map[string]any{"name": b.Name, "owner_user_id": owner,
		"default_password_set": b.DefaultPassword != ""})
	c.reloadBook(w, r, b.ID, http.StatusCreated)
}

type bookUpdateReq struct {
	Name            *string   `json:"name"`
	Note            *string   `json:"note"`
	DefaultPassword optString `json:"default_password"`
}

func (c *b2bCore) handleUpdateBook(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	var req bookUpdateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	var p store.BookPatch
	details := map[string]any{}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if !validBookName(n) {
			badRequest(w, "name must be 1-128 characters")
			return
		}
		p.Name, details["name"] = &n, n
	}
	if req.Note != nil {
		if utf8.RuneCountInString(*req.Note) > 4096 {
			badRequest(w, "note is too long")
			return
		}
		p.Note, details["note_changed"] = req.Note, true
	}
	if req.DefaultPassword.Set {
		if a.book.IsPersonal {
			badRequest(w, "personal address books have no default password")
			return
		}
		pw := ""
		if req.DefaultPassword.Val != nil {
			pw = *req.DefaultPassword.Val
		}
		if len(pw) > 1024 {
			badRequest(w, "default_password is too long")
			return
		}
		p.DefaultPassword, details["default_password_set"] = &pw, pw != ""
	}
	if err := c.d.Store.AddressBooks.UpdateBook(r.Context(), a.book.ID, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "a shared address book with this name already exists")
			return
		}
		c.fail(w, r, "update address book", err)
		return
	}
	c.audit(r, "address_book.update", "address_book", a.book.ID, details)
	c.reloadBook(w, r, a.book.ID, http.StatusOK)
}

func (c *b2bCore) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABRead)
	if a == nil {
		return
	}
	if !a.user.IsAdmin() && a.book.OwnerUserID != a.user.ID {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only the owner or an administrator can delete a book")
		return
	}
	if a.book.IsPersonal {
		personalBook(w, "personal address books cannot be deleted")
		return
	}
	if err := c.d.Store.AddressBooks.DeleteBook(r.Context(), a.book.ID); err != nil {
		c.fail(w, r, "delete address book", err)
		return
	}
	c.audit(r, "address_book.delete", "address_book", a.book.ID, map[string]any{"name": a.book.Name,
		"owner_user_id": a.book.OwnerUserID, "entry_count": a.book.EntryCount})
	w.WriteHeader(http.StatusNoContent)
}

// ---- shares ----

func (c *b2bCore) handleListShares(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	shares, err := c.d.Store.AddressBooks.ListShares(r.Context(), a.book.ID)
	if err != nil {
		c.fail(w, r, "list shares", err)
		return
	}
	out := make([]shareDTO, 0, len(shares))
	for _, s := range shares {
		out = append(out, newShareDTO(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type shareCreateReq struct {
	GranteeType string `json:"grantee_type"`
	GranteeID   int64  `json:"grantee_id"`
	Permission  string `json:"permission"`
}

func (c *b2bCore) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	var req shareCreateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	perm := store.ParseABPermission(req.Permission)
	if perm == store.ABNone {
		badRequest(w, "permission must be read, read_write or full")
		return
	}
	var userID, groupID *int64
	switch req.GranteeType {
	case "user":
		gu, err := c.d.Store.Users.GetByID(ctx, req.GranteeID)
		if errors.Is(err, store.ErrNotFound) {
			badRequest(w, "grantee user does not exist")
			return
		} else if err != nil {
			c.fail(w, r, "create share", err)
			return
		}
		if gu.ID == a.book.OwnerUserID {
			badRequest(w, "the owner already has full control")
			return
		}
		userID = &gu.ID
	case "group":
		g, err := c.d.Store.UserGroups.Get(ctx, req.GranteeID)
		if errors.Is(err, store.ErrNotFound) {
			badRequest(w, "grantee group does not exist")
			return
		} else if err != nil {
			c.fail(w, r, "create share", err)
			return
		}
		groupID = &g.ID
	default:
		badRequest(w, "grantee_type must be user or group")
		return
	}
	if a.book.IsPersonal {
		personalBook(w, "personal address books cannot be shared")
		return
	}
	s, err := c.d.Store.AddressBooks.CreateShare(ctx, a.book.ID, userID, groupID, perm)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "this grantee already has a share; update it instead")
			return
		}
		c.fail(w, r, "create share", err)
		return
	}
	c.audit(r, "address_book.share.create", "address_book", a.book.ID, map[string]any{"share_id": s.ID,
		"grantee_type": req.GranteeType, "grantee_id": req.GranteeID, "permission": perm.String()})
	httpx.WriteJSON(w, http.StatusCreated, newShareDTO(s))
}

func (c *b2bCore) shareID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := idParam(chi.URLParam(r, "share_id"))
	if !ok {
		httpx.NotFound(w, r)
	}
	return id, ok
}

type shareUpdateReq struct {
	Permission string `json:"permission"`
}

func (c *b2bCore) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	id, ok := c.shareID(w, r)
	if !ok {
		return
	}
	var req shareUpdateReq
	if !decodeJSON(w, r, &req) {
		return
	}
	perm := store.ParseABPermission(req.Permission)
	if perm == store.ABNone {
		badRequest(w, "permission must be read, read_write or full")
		return
	}
	if err := c.d.Store.AddressBooks.UpdateShare(ctx, a.book.ID, id, perm); err != nil {
		c.fail(w, r, "update share", err)
		return
	}
	s, err := c.d.Store.AddressBooks.GetShare(ctx, a.book.ID, id)
	if err != nil {
		c.fail(w, r, "update share", err)
		return
	}
	c.audit(r, "address_book.share.update", "address_book", a.book.ID, map[string]any{"share_id": id,
		"permission": perm.String()})
	httpx.WriteJSON(w, http.StatusOK, newShareDTO(s))
}

func (c *b2bCore) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	id, ok := c.shareID(w, r)
	if !ok {
		return
	}
	if err := c.d.Store.AddressBooks.DeleteShare(r.Context(), a.book.ID, id); err != nil {
		c.fail(w, r, "delete share", err)
		return
	}
	c.audit(r, "address_book.share.delete", "address_book", a.book.ID, map[string]any{"share_id": id})
	w.WriteHeader(http.StatusNoContent)
}

// ---- entries ----

var rustdeskIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func (c *b2bCore) handleListEntries(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABRead)
	if a == nil {
		return
	}
	q := newListQuery(r)
	p := q.paging()
	f := store.EntryListFilter{Q: q.search(), Tags: q.v["tag"],
		Sort: q.sort("alias", "alias", "rustdesk_id", "hostname", "updated_at"), Limit: p.Size, Offset: p.offset()}
	if !q.ok(w) {
		return
	}
	entries, total, err := c.d.Store.AddressBooks.ListEntriesFiltered(r.Context(), a.book.ID, f)
	if err != nil {
		c.fail(w, r, "list entries", err)
		return
	}
	out := make([]entryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, c.entryDTO(e))
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// entryReq is EntryCreate / EntryUpdate.
type entryReq struct {
	RustdeskID       *string   `json:"rustdesk_id"`
	Password         optString `json:"password"`
	Alias            *string   `json:"alias"`
	OSUsername       *string   `json:"os_username"`
	Hostname         *string   `json:"hostname"`
	Platform         *string   `json:"platform"`
	LoginName        *string   `json:"login_name"`
	Note             optString `json:"note"`
	Tags             *[]string `json:"tags"`
	ForceAlwaysRelay *bool     `json:"force_always_relay"`
	RDPPort          *string   `json:"rdp_port"`
	RDPUsername      *string   `json:"rdp_username"`
	SameServer       *bool     `json:"same_server"`
}

// patch validates the request and converts it; it returns a validation message.
func (in *entryReq) patch() (store.ABEntryPatch, string) {
	var p store.ABEntryPatch
	for _, f := range []struct {
		name string
		v    *string
		max  int
	}{
		{"alias", in.Alias, 128}, {"os_username", in.OSUsername, 128}, {"hostname", in.Hostname, 255},
		{"platform", in.Platform, 64}, {"login_name", in.LoginName, 128}, {"rdp_port", in.RDPPort, 5},
		{"rdp_username", in.RDPUsername, 128},
	} {
		if f.v != nil && utf8.RuneCountInString(*f.v) > f.max {
			return p, f.name + " is too long"
		}
	}
	if in.RDPPort != nil && *in.RDPPort != "" {
		if n, err := strconv.Atoi(*in.RDPPort); err != nil || n < 1 || n > 65535 {
			return p, "rdp_port must be a port number"
		}
	}
	p.Alias, p.OSUsername, p.Hostname, p.Platform, p.LoginName = in.Alias, in.OSUsername, in.Hostname, in.Platform, in.LoginName
	p.RDPPort, p.RDPUsername, p.ForceAlwaysRelay, p.SameServer = in.RDPPort, in.RDPUsername, in.ForceAlwaysRelay, in.SameServer
	if in.Note.Set {
		note := ""
		if in.Note.Val != nil {
			note = *in.Note.Val
		}
		if utf8.RuneCountInString(note) > 4096 {
			return p, "note is too long"
		}
		p.Note = &note
	}
	if in.Password.Set {
		pw := ""
		if in.Password.Val != nil {
			pw = *in.Password.Val
		}
		if len(pw) > 1024 {
			return p, "password is too long"
		}
		p.Password = &pw
	}
	if in.Tags != nil {
		if msg := validTags(*in.Tags); msg != "" {
			return p, msg
		}
		p.Tags = in.Tags
	}
	return p, ""
}

func validTags(tags []string) string {
	if len(tags) > 1000 {
		return "too many tags"
	}
	for _, t := range tags {
		if t == "" || utf8.RuneCountInString(t) > 64 {
			return "tag names must be 1-64 characters"
		}
	}
	return ""
}

func (c *b2bCore) writeEntry(w http.ResponseWriter, r *http.Request, bookID, id int64, status int) {
	e, err := c.d.Store.AddressBooks.GetEntryByID(r.Context(), bookID, id)
	if err != nil {
		c.fail(w, r, "load entry", err)
		return
	}
	httpx.WriteJSON(w, status, c.entryDTO(e))
}

func (c *b2bCore) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABReadWrite)
	if a == nil {
		return
	}
	var req entryReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RustdeskID == nil || !rustdeskIDRe.MatchString(*req.RustdeskID) {
		badRequest(w, "rustdesk_id must be 1-100 characters: letters, digits, '_' or '-'")
		return
	}
	if req.Password.Set && req.Password.Val == nil {
		req.Password = optString{}
	}
	p, msg := req.patch()
	if msg != "" {
		badRequest(w, msg)
		return
	}
	id, err := c.d.Store.AddressBooks.CreateEntry(r.Context(), a.book.ID, *req.RustdeskID, p)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "this id is already in the address book")
			return
		}
		c.fail(w, r, "create entry", err)
		return
	}
	c.audit(r, "address_book.entry.create", "address_book", a.book.ID, map[string]any{"entry_id": id,
		"rustdesk_id": *req.RustdeskID, "password_set": p.Password != nil && *p.Password != ""})
	c.writeEntry(w, r, a.book.ID, id, http.StatusCreated)
}

func (c *b2bCore) entryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := idParam(chi.URLParam(r, "entry_id"))
	if !ok {
		httpx.NotFound(w, r)
	}
	return id, ok
}

func (c *b2bCore) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABReadWrite)
	if a == nil {
		return
	}
	id, ok := c.entryID(w, r)
	if !ok {
		return
	}
	var req entryReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.RustdeskID != nil && !rustdeskIDRe.MatchString(*req.RustdeskID) {
		badRequest(w, "rustdesk_id must be 1-100 characters: letters, digits, '_' or '-'")
		return
	}
	p, msg := req.patch()
	if msg != "" {
		badRequest(w, msg)
		return
	}
	if err := c.d.Store.AddressBooks.PatchEntryByID(r.Context(), a.book.ID, id, req.RustdeskID, p); err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "this id is already in the address book")
			return
		}
		c.fail(w, r, "update entry", err)
		return
	}
	details := map[string]any{"entry_id": id, "password_changed": p.Password != nil}
	if req.RustdeskID != nil {
		details["rustdesk_id"] = *req.RustdeskID
	}
	c.audit(r, "address_book.entry.update", "address_book", a.book.ID, details)
	c.writeEntry(w, r, a.book.ID, id, http.StatusOK)
}

func (c *b2bCore) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	id, ok := c.entryID(w, r)
	if !ok {
		return
	}
	n, err := c.d.Store.AddressBooks.BulkEntries(r.Context(), a.book.ID, []int64{id}, store.BulkDelete, nil)
	if err != nil {
		c.fail(w, r, "delete entry", err)
		return
	}
	if n == 0 {
		httpx.NotFound(w, r)
		return
	}
	c.audit(r, "address_book.entry.delete", "address_book", a.book.ID, map[string]any{"entry_id": id})
	w.WriteHeader(http.StatusNoContent)
}

// bulkResultDTO is components.schemas.BulkResult.
type bulkResultDTO struct {
	Affected int            `json:"affected"`
	Skipped  int            `json:"skipped"`
	Errors   []bulkErrorDTO `json:"errors,omitempty"`
}

type bulkErrorDTO struct {
	ID      any    `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type entryBulkReq struct {
	Action   string   `json:"action"`
	EntryIDs []int64  `json:"entry_ids"`
	Tags     []string `json:"tags"`
}

func (c *b2bCore) handleBulkEntries(w http.ResponseWriter, r *http.Request) {
	var req entryBulkReq
	if !decodeBody(w, r, &req, 1<<20) {
		return
	}
	need := store.ABReadWrite
	switch req.Action {
	case store.BulkDelete:
		need = store.ABFull // as in the client API: deleting entries requires full control
	case store.BulkAddTags, store.BulkRemoveTags:
		if len(req.Tags) == 0 {
			badRequest(w, "tags are required for "+req.Action)
			return
		}
	case store.BulkSetTags:
	default:
		badRequest(w, "action must be delete, add_tags, remove_tags or set_tags")
		return
	}
	if len(req.EntryIDs) == 0 || len(req.EntryIDs) > 5000 {
		badRequest(w, "entry_ids must contain 1-5000 ids")
		return
	}
	if msg := validTags(req.Tags); msg != "" {
		badRequest(w, msg)
		return
	}
	a := c.loadBook(w, r, need)
	if a == nil {
		return
	}
	n, err := c.d.Store.AddressBooks.BulkEntries(r.Context(), a.book.ID, req.EntryIDs, req.Action, req.Tags)
	if err != nil {
		c.fail(w, r, "bulk entries", err)
		return
	}
	c.audit(r, "address_book.entry.bulk", "address_book", a.book.ID, map[string]any{"action": req.Action,
		"requested": len(req.EntryIDs), "affected": n, "tags": req.Tags})
	httpx.WriteJSON(w, http.StatusOK, bulkResultDTO{Affected: n, Skipped: len(uniqueIDs(req.EntryIDs)) - n})
}

func uniqueIDs(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

type fromDevicesReq struct {
	DeviceIDs []int64  `json:"device_ids"`
	Tags      []string `json:"tags"`
}

// devicePlatform maps a device OS string to the client's platform names.
func devicePlatform(os string) string {
	l := strings.ToLower(os)
	switch {
	case strings.Contains(l, "windows"):
		return "Windows"
	case strings.Contains(l, "android"):
		return "Android"
	case strings.Contains(l, "mac") || strings.Contains(l, "darwin"):
		return "Mac OS"
	case strings.Contains(l, "ios"):
		return "iOS"
	case strings.Contains(l, "linux"):
		return "Linux"
	}
	if utf8.RuneCountInString(os) > 64 {
		return string([]rune(os)[:64])
	}
	return os
}

func (c *b2bCore) handleEntriesFromDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := c.loadBook(w, r, store.ABReadWrite)
	if a == nil {
		return
	}
	var req fromDevicesReq
	if !decodeBody(w, r, &req, 1<<20) {
		return
	}
	if len(req.DeviceIDs) == 0 || len(req.DeviceIDs) > 1000 {
		badRequest(w, "device_ids must contain 1-1000 ids")
		return
	}
	if msg := validTags(req.Tags); msg != "" {
		badRequest(w, msg)
		return
	}
	res := bulkResultDTO{}
	seen := map[int64]bool{}
	var added []string
	for _, id := range req.DeviceIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		d, err := c.d.Store.Devices.Get(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			c.fail(w, r, "entries from devices", err)
			return
		}
		if d == nil || (!a.user.IsAdmin() && (d.OwnerUserID == nil || *d.OwnerUserID != a.user.ID)) {
			res.Errors = append(res.Errors, bulkErrorDTO{ID: id, Code: "not_found", Message: "device not found"})
			continue
		}
		alias := d.Alias
		if alias == "" {
			alias = d.Hostname
		}
		platform := devicePlatform(d.OS)
		p := store.ABEntryPatch{Alias: &alias, Hostname: &d.Hostname, OSUsername: &d.OSUsername, Platform: &platform}
		if len(req.Tags) > 0 {
			tags := req.Tags
			p.Tags = &tags
		}
		if _, err := c.d.Store.AddressBooks.CreateEntry(ctx, a.book.ID, d.RustdeskID, p); err != nil {
			if errors.Is(err, store.ErrConflict) {
				res.Skipped++
				continue
			}
			c.fail(w, r, "entries from devices", err)
			return
		}
		res.Affected++
		added = append(added, d.RustdeskID)
	}
	if res.Affected > 0 {
		c.audit(r, "address_book.entry.from_devices", "address_book", a.book.ID, map[string]any{
			"rustdesk_ids": added, "skipped": res.Skipped})
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// ---- tags ----

func (c *b2bCore) handleListTags(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABRead)
	if a == nil {
		return
	}
	tags, err := c.d.Store.AddressBooks.ListTagInfos(r.Context(), a.book.ID)
	if err != nil {
		c.fail(w, r, "list tags", err)
		return
	}
	out := make([]tagDTO, 0, len(tags))
	for _, t := range tags {
		out = append(out, newTagDTO(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type tagReq struct {
	Name      *string `json:"name"`
	ColorARGB *int64  `json:"color_argb"`
}

// validate checks the provided fields; it returns a validation message.
func (in *tagReq) validate() string {
	if in.Name != nil && (*in.Name == "" || utf8.RuneCountInString(*in.Name) > 64) {
		return "name must be 1-64 characters"
	}
	if in.ColorARGB != nil && (*in.ColorARGB < 0 || *in.ColorARGB > 0xFFFFFFFF) {
		return "color_argb must be an ARGB uint32 (0..4294967295)"
	}
	return ""
}

func (c *b2bCore) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABReadWrite)
	if a == nil {
		return
	}
	var req tagReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil || req.ColorARGB == nil {
		badRequest(w, "name and color_argb are required")
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(w, msg)
		return
	}
	ctx := r.Context()
	id, err := c.d.Store.AddressBooks.CreateTag(ctx, a.book.ID, *req.Name, uint32(*req.ColorARGB))
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "a tag with this name already exists")
			return
		}
		c.fail(w, r, "create tag", err)
		return
	}
	c.audit(r, "address_book.tag.create", "address_book", a.book.ID, map[string]any{"tag_id": id, "name": *req.Name,
		"color_argb": *req.ColorARGB})
	c.writeTag(w, r, a.book.ID, id, http.StatusCreated)
}

func (c *b2bCore) writeTag(w http.ResponseWriter, r *http.Request, bookID, id int64, status int) {
	t, err := c.d.Store.AddressBooks.GetTagInfo(r.Context(), bookID, id)
	if err != nil {
		c.fail(w, r, "load tag", err)
		return
	}
	httpx.WriteJSON(w, status, newTagDTO(t))
}

func (c *b2bCore) tagID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, ok := idParam(chi.URLParam(r, "tag_id"))
	if !ok {
		httpx.NotFound(w, r)
	}
	return id, ok
}

func (c *b2bCore) handleUpdateTag(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABReadWrite)
	if a == nil {
		return
	}
	id, ok := c.tagID(w, r)
	if !ok {
		return
	}
	var req tagReq
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		badRequest(w, msg)
		return
	}
	var color *uint32
	details := map[string]any{"tag_id": id}
	if req.ColorARGB != nil {
		v := uint32(*req.ColorARGB)
		color, details["color_argb"] = &v, *req.ColorARGB
	}
	if req.Name != nil {
		details["name"] = *req.Name
	}
	if err := c.d.Store.AddressBooks.UpdateTagByID(r.Context(), a.book.ID, id, req.Name, color); err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpx.WriteError(w, http.StatusConflict, "conflict", "a tag with this name already exists")
			return
		}
		c.fail(w, r, "update tag", err)
		return
	}
	c.audit(r, "address_book.tag.update", "address_book", a.book.ID, details)
	c.writeTag(w, r, a.book.ID, id, http.StatusOK)
}

func (c *b2bCore) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	a := c.loadBook(w, r, store.ABFull)
	if a == nil {
		return
	}
	id, ok := c.tagID(w, r)
	if !ok {
		return
	}
	if err := c.d.Store.AddressBooks.DeleteTagByID(r.Context(), a.book.ID, id); err != nil {
		c.fail(w, r, "delete tag", err)
		return
	}
	c.audit(r, "address_book.tag.delete", "address_book", a.book.ID, map[string]any{"tag_id": id})
	w.WriteHeader(http.StatusNoContent)
}
