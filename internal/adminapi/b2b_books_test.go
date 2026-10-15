package adminapi

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/bropines/rustdesk-panel/internal/store"
)

func TestBooksAuthz(t *testing.T) {
	e := newB2Env(t, b2opts{})
	anon := e.client()
	code, m, _ := anon.do(http.MethodGet, "/address-books", nil)
	wantErr(t, "anonymous list", code, 401, m, "unauthorized")
	code, m, _ = anon.do(http.MethodPost, "/address-books", map[string]any{"name": "x"})
	wantErr(t, "anonymous create", code, 401, m, "unauthorized")

	c, _ := e.as("alice", store.RoleUser)
	c.noCSRF = true
	code, m, _ = c.do(http.MethodPost, "/address-books", map[string]any{"name": "x"})
	wantErr(t, "create without CSRF", code, 403, m, "csrf_failed")
	c.noCSRF = false
	code, m, _ = c.do(http.MethodGet, "/address-books?scope=all", nil)
	wantErr(t, "user scope=all", code, 403, m, "forbidden")
	code, m, _ = c.do(http.MethodGet, "/address-books?page_size=501", nil)
	wantErr(t, "page_size", code, 400, m, "validation_failed")
	code, m, _ = c.do(http.MethodGet, "/address-books?sort=owner", nil)
	wantErr(t, "sort", code, 400, m, "validation_failed")
}

func TestBookCRUD(t *testing.T) {
	e := newB2Env(t, b2opts{})
	alice, au := e.as("alice", store.RoleUser)
	admin, _ := e.as("root", store.RoleAdmin)
	bob, bu := e.as("bob", store.RoleUser)
	if _, err := e.st.AddressBooks.EnsurePersonal(e.ctx, au.ID); err != nil {
		t.Fatal(err)
	}

	code, m, _ := alice.do(http.MethodPost, "/address-books", map[string]any{"name": " Team ", "note": "n",
		"default_password": "book-secret-pw"})
	want(t, "create", code, 201, m)
	if m["name"] != "Team" || m["permission"] != "full" || m["is_personal"] != false ||
		m["has_default_password"] != true || b2bNum(m["owner_user_id"]) != au.ID {
		t.Fatalf("created: %v", m)
	}
	if o, _ := m["owner"].(map[string]any); o["username"] != "alice" {
		t.Fatalf("owner: %v", m["owner"])
	}
	bookID := idOf(m)
	_, body := alice.b2bRaw(http.MethodGet, "/address-books/"+bookID, nil)
	noSecret(t, "get book", body, "book-secret-pw")

	code, m, _ = bob.do(http.MethodPost, "/address-books", map[string]any{"name": "team"})
	wantErr(t, "duplicate name", code, 409, m, "conflict")
	code, m, _ = bob.do(http.MethodPost, "/address-books", map[string]any{"name": "x", "owner_user_id": au.ID})
	wantErr(t, "user creates for other", code, 403, m, "forbidden")
	code, m, _ = admin.do(http.MethodPost, "/address-books", map[string]any{"name": "Bobs", "owner_user_id": bu.ID})
	want(t, "admin creates for bob", code, 201, m)
	if b2bNum(m["owner_user_id"]) != bu.ID {
		t.Fatalf("owner: %v", m)
	}
	code, m, _ = admin.do(http.MethodPost, "/address-books", map[string]any{"name": "Ghost", "owner_user_id": 9999})
	wantErr(t, "unknown owner", code, 400, m, "validation_failed")
	code, m, _ = alice.do(http.MethodPost, "/address-books", map[string]any{"name": ""})
	wantErr(t, "empty name", code, 400, m, "validation_failed")
	code, m, _ = alice.do(http.MethodPost, "/address-books", map[string]any{"name": "x", "bogus": 1})
	wantErr(t, "unknown field", code, 400, m, "validation_failed")

	// alice sees her personal + Team; not Bobs.
	code, m, _ = alice.do(http.MethodGet, "/address-books", nil)
	want(t, "list", code, 200, m)
	if b2bNum(m["total"]) != 2 || b2bNum(m["page"]) != 1 || b2bNum(m["page_size"]) != 50 {
		t.Fatalf("list: %v", m)
	}
	code, m, _ = alice.do(http.MethodGet, "/address-books?kind=shared", nil)
	if l := b2bItems(m); code != 200 || len(l) != 1 || l[0]["name"] != "Team" {
		t.Fatalf("kind=shared: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/address-books?scope=all&sort=-name", nil)
	if l := b2bItems(m); code != 200 || b2bNum(m["total"]) != 3 || l[0]["name"] != "Team" || l[0]["permission"] != "full" {
		t.Fatalf("admin all: %v", m)
	}
	code, m, _ = admin.do(http.MethodGet, "/address-books?scope=all&owner_user_id="+strconv.FormatInt(bu.ID, 10), nil)
	if code != 200 || b2bNum(m["total"]) != 1 {
		t.Fatalf("owner filter: %v", m)
	}
	code, m, _ = alice.do(http.MethodGet, "/address-books?owner_user_id="+strconv.FormatInt(bu.ID, 10), nil)
	wantErr(t, "user owner filter", code, 403, m, "forbidden")
	code, m, _ = alice.do(http.MethodGet, "/address-books?q=TEA", nil)
	if code != 200 || b2bNum(m["total"]) != 1 {
		t.Fatalf("q: %v", m)
	}

	// Update: rename, clear password.
	code, m, _ = alice.do(http.MethodPatch, "/address-books/"+bookID, map[string]any{"name": "Team 2", "default_password": nil})
	want(t, "update", code, 200, m)
	if m["name"] != "Team 2" || m["has_default_password"] != false {
		t.Fatalf("updated: %v", m)
	}
	code, m, _ = alice.do(http.MethodPatch, "/address-books/"+bookID, map[string]any{"name": "bobs"})
	wantErr(t, "rename conflict", code, 409, m, "conflict")
	code, m, _ = bob.do(http.MethodGet, "/address-books/"+bookID, nil)
	wantErr(t, "stranger get", code, 404, m, "not_found")
	code, m, _ = bob.do(http.MethodDelete, "/address-books/"+bookID, nil)
	wantErr(t, "stranger delete", code, 404, m, "not_found")

	personal, _ := e.st.AddressBooks.GetPersonal(e.ctx, au.ID)
	pid := strconv.FormatInt(personal.ID, 10)
	code, m, _ = alice.do(http.MethodDelete, "/address-books/"+pid, nil)
	wantErr(t, "delete personal", code, 409, m, "personal_book")
	code, m, _ = alice.do(http.MethodPatch, "/address-books/"+pid, map[string]any{"default_password": "x"})
	wantErr(t, "personal default password", code, 400, m, "validation_failed")

	code, _, _ = alice.do(http.MethodDelete, "/address-books/"+bookID, nil)
	want(t, "delete", code, 204, nil)
	code, _, _ = alice.do(http.MethodGet, "/address-books/"+bookID, nil)
	want(t, "get deleted", code, 404, nil)
	code, m, _ = alice.do(http.MethodGet, "/address-books/abc", nil)
	wantErr(t, "bad id", code, 404, m, "not_found")

	// Every mutation is audited without secrets.
	rows, _, err := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{ActionPrefix: "address_book."})
	if err != nil || len(rows) < 4 {
		t.Fatalf("audit rows %d %v", len(rows), err)
	}
	for _, r := range rows {
		for k, v := range r.Details {
			if s, ok := v.(string); ok && s == "book-secret-pw" {
				t.Fatalf("audit %s leaked %s", r.Action, k)
			}
		}
	}
}

// shareFixture: alice owns "Team"; bob has a read share; carol is in group G
// which has read_write; dave has nothing; root is admin.
type shareFixture struct {
	e                              *b2env
	alice, bob, carol, dave, admin *client
	au, bu, cu                     *store.User
	book                           string
	groupID                        int64
}

func newShareFixture(t *testing.T) *shareFixture {
	e := newB2Env(t, b2opts{})
	f := &shareFixture{e: e}
	g := &store.UserGroup{Name: "G"}
	if err := e.st.UserGroups.Create(e.ctx, g); err != nil {
		t.Fatal(err)
	}
	f.groupID = g.ID
	f.alice, f.au = e.as("alice", store.RoleUser)
	f.bob, f.bu = e.as("bob", store.RoleUser)
	f.carol, f.cu = e.as("carol", store.RoleUser)
	f.cu.UserGroupID = &g.ID
	if err := e.st.Users.Update(e.ctx, f.cu); err != nil {
		t.Fatal(err)
	}
	f.dave, _ = e.as("dave", store.RoleUser)
	f.admin, _ = e.as("root", store.RoleAdmin)
	code, m, _ := f.alice.do(http.MethodPost, "/address-books", map[string]any{"name": "Team"})
	want(t, "create", code, 201, m)
	f.book = "/address-books/" + idOf(m)
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "user", "grantee_id": f.bu.ID, "permission": "read"})
	want(t, "share bob", code, 201, m)
	if m["grantee_type"] != "user" || m["permission"] != "read" {
		t.Fatalf("share: %v", m)
	}
	if gr, _ := m["grantee"].(map[string]any); gr["name"] != "bob" {
		t.Fatalf("grantee: %v", m)
	}
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "group", "grantee_id": g.ID, "permission": "read_write"})
	want(t, "share group", code, 201, m)
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/entries", map[string]any{"rustdesk_id": "100000001", "alias": "one", "tags": []string{"t1"}})
	want(t, "seed entry", code, 201, m)
	return f
}

func TestSharePermissionMatrix(t *testing.T) {
	f := newShareFixture(t)
	e := f.e
	entry := func(c *client, id string) (int, map[string]any) {
		code, m, _ := c.do(http.MethodPost, f.book+"/entries", map[string]any{"rustdesk_id": id})
		return code, m
	}
	firstEntry := func() string {
		_, m, _ := f.alice.do(http.MethodGet, f.book+"/entries", nil)
		return idOf(b2bItems(m)[0])
	}

	// Effective permission reported per caller.
	for _, tc := range []struct {
		c    *client
		perm string
	}{{f.alice, "full"}, {f.bob, "read"}, {f.carol, "read_write"}, {f.admin, "full"}} {
		code, m, _ := tc.c.do(http.MethodGet, f.book, nil)
		if code != 200 || m["permission"] != tc.perm {
			t.Fatalf("permission: %d %v want %s", code, m, tc.perm)
		}
	}
	// dave: no access at all -> 404 everywhere.
	for _, p := range []string{f.book, f.book + "/entries", f.book + "/tags", f.book + "/shares"} {
		code, m, _ := f.dave.do(http.MethodGet, p, nil)
		wantErr(t, "dave "+p, code, 404, m, "not_found")
	}
	// The book is listed for sharees.
	if _, m, _ := f.carol.do(http.MethodGet, "/address-books", nil); b2bNum(m["total"]) != 1 {
		t.Fatalf("carol list: %v", m)
	}
	if _, m, _ := f.dave.do(http.MethodGet, "/address-books", nil); b2bNum(m["total"]) != 0 {
		t.Fatalf("dave list: %v", m)
	}

	// read: list only.
	code, m, _ := f.bob.do(http.MethodGet, f.book+"/entries", nil)
	want(t, "bob list entries", code, 200, m)
	code, m = entry(f.bob, "200000002")
	wantErr(t, "bob create entry", code, 403, m, "forbidden")
	code, m, _ = f.bob.do(http.MethodPost, f.book+"/tags", map[string]any{"name": "x", "color_argb": 1})
	wantErr(t, "bob create tag", code, 403, m, "forbidden")
	code, m, _ = f.bob.do(http.MethodGet, f.book+"/shares", nil)
	wantErr(t, "bob shares", code, 403, m, "forbidden")
	code, m, _ = f.bob.do(http.MethodPatch, f.book, map[string]any{"name": "nope"})
	wantErr(t, "bob rename", code, 403, m, "forbidden")

	// read_write via group: add/edit, no delete, no shares.
	code, m = entry(f.carol, "300000003")
	want(t, "carol create entry", code, 201, m)
	eid := idOf(m)
	code, m, _ = f.carol.do(http.MethodPatch, f.book+"/entries/"+eid, map[string]any{"alias": "three"})
	want(t, "carol update entry", code, 200, m)
	code, m, _ = f.carol.do(http.MethodPost, f.book+"/tags", map[string]any{"name": "c", "color_argb": 5})
	want(t, "carol create tag", code, 201, m)
	tagID := idOf(m)
	code, m, _ = f.carol.do(http.MethodPost, f.book+"/entries/bulk", map[string]any{"action": "add_tags", "entry_ids": []string{eid}, "tags": []string{"c"}})
	wantErr(t, "bulk with string ids", code, 400, m, "validation_failed")
	n, _ := strconv.ParseInt(eid, 10, 64)
	code, m, _ = f.carol.do(http.MethodPost, f.book+"/entries/bulk", map[string]any{"action": "add_tags", "entry_ids": []int64{n}, "tags": []string{"c"}})
	want(t, "carol bulk tags", code, 200, m)
	code, m, _ = f.carol.do(http.MethodPost, f.book+"/entries/bulk", map[string]any{"action": "delete", "entry_ids": []int64{n}})
	wantErr(t, "carol bulk delete", code, 403, m, "forbidden")
	code, m, _ = f.carol.do(http.MethodDelete, f.book+"/entries/"+eid, nil)
	wantErr(t, "carol delete entry", code, 403, m, "forbidden")
	code, m, _ = f.carol.do(http.MethodDelete, f.book+"/tags/"+tagID, nil)
	wantErr(t, "carol delete tag", code, 403, m, "forbidden")
	code, m, _ = f.carol.do(http.MethodGet, f.book+"/shares", nil)
	wantErr(t, "carol shares", code, 403, m, "forbidden")

	// Owner manages shares: list, upgrade bob to full.
	code, m, _ = f.alice.do(http.MethodGet, f.book+"/shares", nil)
	if l := b2bItems(m); code != 200 || len(l) != 2 || l[0]["grantee_type"] != "user" || l[1]["grantee_type"] != "group" {
		t.Fatalf("shares: %v", m)
	}
	bobShare := idOf(b2bItems(m)[0])
	code, m, _ = f.alice.do(http.MethodPatch, f.book+"/shares/"+bobShare, map[string]any{"permission": "full"})
	want(t, "upgrade bob", code, 200, m)
	if m["permission"] != "full" {
		t.Fatalf("upgrade: %v", m)
	}
	code, m, _ = f.alice.do(http.MethodPatch, f.book+"/shares/"+bobShare, map[string]any{"permission": "owner"})
	wantErr(t, "bad permission", code, 400, m, "validation_failed")
	// full sharee: delete entries/tags, manage shares, but not delete the book.
	code, _, _ = f.bob.do(http.MethodDelete, f.book+"/entries/"+eid, nil)
	want(t, "bob delete entry", code, 204, nil)
	code, _, _ = f.bob.do(http.MethodDelete, f.book+"/tags/"+tagID, nil)
	want(t, "bob delete tag", code, 204, nil)
	code, m, _ = f.bob.do(http.MethodGet, f.book+"/shares", nil)
	want(t, "bob shares now", code, 200, m)
	code, m, _ = f.bob.do(http.MethodDelete, f.book, nil)
	wantErr(t, "bob delete book", code, 403, m, "forbidden")
	code, m, _ = f.bob.do(http.MethodDelete, f.book+"/entries/"+eid, nil)
	wantErr(t, "delete missing entry", code, 404, m, "not_found")
	first := firstEntry()
	code, m, _ = f.bob.do(http.MethodPatch, f.book+"/entries/"+first, map[string]any{"rustdesk_id": "100000001x"})
	want(t, "change rustdesk id", code, 200, m)

	// Share validation.
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "user", "grantee_id": f.bu.ID, "permission": "read"})
	wantErr(t, "duplicate share", code, 409, m, "conflict")
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "user", "grantee_id": f.au.ID, "permission": "read"})
	wantErr(t, "share to owner", code, 400, m, "validation_failed")
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "user", "grantee_id": 9999, "permission": "read"})
	wantErr(t, "unknown grantee", code, 400, m, "validation_failed")
	code, m, _ = f.alice.do(http.MethodPost, f.book+"/shares", map[string]any{"grantee_type": "role", "grantee_id": 1, "permission": "read"})
	wantErr(t, "bad grantee type", code, 400, m, "validation_failed")
	personal, _ := e.st.AddressBooks.EnsurePersonal(e.ctx, f.au.ID)
	code, m, _ = f.alice.do(http.MethodPost, "/address-books/"+strconv.FormatInt(personal.ID, 10)+"/shares",
		map[string]any{"grantee_type": "user", "grantee_id": f.bu.ID, "permission": "read"})
	wantErr(t, "share personal", code, 409, m, "personal_book")

	// Delete the group share: carol loses access.
	_, m, _ = f.alice.do(http.MethodGet, f.book+"/shares", nil)
	groupShare := idOf(b2bItems(m)[1])
	code, _, _ = f.alice.do(http.MethodDelete, f.book+"/shares/"+groupShare, nil)
	want(t, "delete share", code, 204, nil)
	code, _, _ = f.carol.do(http.MethodGet, f.book, nil)
	want(t, "carol after unshare", code, 404, nil)
	code, _, _ = f.alice.do(http.MethodDelete, f.book+"/shares/"+groupShare, nil)
	want(t, "delete share again", code, 404, nil)

	// Admin has full control of every book.
	code, _, _ = f.admin.do(http.MethodDelete, f.book, nil)
	want(t, "admin delete", code, 204, nil)
}

func TestEntrySecretsWriteOnly(t *testing.T) {
	e := newB2Env(t, b2opts{})
	c, au := e.as("alice", store.RoleUser)
	book, _ := e.st.AddressBooks.EnsurePersonal(e.ctx, au.ID)
	base := "/address-books/" + strconv.FormatInt(book.ID, 10)
	code, body := c.b2bRaw(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": "123456789",
		"password": "entry-pass-1", "alias": "pc", "note": "hello", "rdp_port": "3389"})
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	noSecret(t, "create", body, "entry-pass-1")
	stored, err := e.st.AddressBooks.GetEntry(e.ctx, book.ID, "123456789")
	if err != nil || stored.Password != "entry-pass-1" {
		t.Fatalf("stored password: %v %v", stored, err)
	}
	// Client API data (hash) set out of band is not returned either.
	h := "client-hash-xyz"
	if err := e.st.AddressBooks.PatchEntry(e.ctx, book.ID, "123456789", store.ABEntryPatch{Hash: &h}); err != nil {
		t.Fatal(err)
	}
	code, body = c.b2bRaw(http.MethodGet, base+"/entries", nil)
	if code != 200 {
		t.Fatalf("list: %d %s", code, body)
	}
	noSecret(t, "list", body, "entry-pass-1", "client-hash-xyz")
	_, m, _ := c.do(http.MethodGet, base+"/entries", nil)
	ent := b2bItems(m)[0]
	if ent["has_password"] != true || ent["note"] != "hello" || ent["online"] != nil || ent["device_id"] != nil {
		t.Fatalf("entry: %v", ent)
	}
	eid := idOf(ent)
	// Omitting password keeps it; null clears it (hash too remains -> has_password).
	code, m, _ = c.do(http.MethodPatch, base+"/entries/"+eid, map[string]any{"alias": "pc2"})
	want(t, "patch keep", code, 200, m)
	if stored, _ = e.st.AddressBooks.GetEntry(e.ctx, book.ID, "123456789"); stored.Password != "entry-pass-1" {
		t.Fatalf("password not kept: %q", stored.Password)
	}
	code, m, _ = c.do(http.MethodPatch, base+"/entries/"+eid, map[string]any{"password": nil, "note": nil})
	want(t, "patch clear", code, 200, m)
	if stored, _ = e.st.AddressBooks.GetEntry(e.ctx, book.ID, "123456789"); stored.Password != "" || stored.Note != "" {
		t.Fatalf("password not cleared: %+v", stored)
	}
	code, m, _ = c.do(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": "bad id"})
	wantErr(t, "bad rustdesk id", code, 400, m, "validation_failed")
	code, m, _ = c.do(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": "123456789"})
	wantErr(t, "duplicate entry", code, 409, m, "conflict")
	code, m, _ = c.do(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": "x1", "rdp_port": "70000"})
	wantErr(t, "bad rdp port", code, 400, m, "validation_failed")
	// Audit details never carry the password.
	rows, _, _ := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{ActionPrefix: "address_book.entry"})
	for _, r := range rows {
		for _, v := range r.Details {
			if v == "entry-pass-1" {
				t.Fatalf("audit leaked password: %v", r.Details)
			}
		}
	}
}

func TestTagsARGBAndRename(t *testing.T) {
	e := newB2Env(t, b2opts{})
	c, au := e.as("alice", store.RoleUser)
	book, _ := e.st.AddressBooks.EnsurePersonal(e.ctx, au.ID)
	base := "/address-books/" + strconv.FormatInt(book.ID, 10)
	for _, color := range []int64{0, 4288585374, 4294967295} {
		code, m, _ := c.do(http.MethodPost, base+"/tags", map[string]any{"name": "c" + strconv.FormatInt(color, 10), "color_argb": color})
		want(t, "create tag", code, 201, m)
		if b2bNum(m["color_argb"]) != color || b2bNum(m["entry_count"]) != 0 {
			t.Fatalf("argb round trip %d: %v", color, m)
		}
		stored, _ := e.st.AddressBooks.ListTags(e.ctx, book.ID)
		if uint32(color) != stored[len(stored)-1].Color {
			t.Fatalf("stored color %d", stored[len(stored)-1].Color)
		}
	}
	for _, bad := range []any{-1, 4294967296, 1.5, "red"} {
		code, m, _ := c.do(http.MethodPost, base+"/tags", map[string]any{"name": "bad", "color_argb": bad})
		wantErr(t, "bad color", code, 400, m, "validation_failed")
	}
	code, m, _ := c.do(http.MethodPost, base+"/tags", map[string]any{"name": "c0", "color_argb": 1})
	wantErr(t, "dup tag", code, 409, m, "conflict")
	code, m, _ = c.do(http.MethodPost, base+"/tags", map[string]any{"name": "c0"})
	wantErr(t, "missing color", code, 400, m, "validation_failed")

	// Unknown tag names on an entry are created with the default colour.
	code, m, _ = c.do(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": "555555", "tags": []string{"work", "c0"}})
	want(t, "entry with tags", code, 201, m)
	code, m, _ = c.do(http.MethodGet, base+"/tags", nil)
	want(t, "list tags", code, 200, m)
	var workID string
	for _, tg := range b2bItems(m) {
		if tg["name"] == "work" {
			workID = idOf(tg)
			if b2bNum(tg["color_argb"]) != int64(store.DefaultTagColor) || b2bNum(tg["entry_count"]) != 1 {
				t.Fatalf("auto tag: %v", tg)
			}
		}
	}
	// Rename keeps the link; recolour.
	code, m, _ = c.do(http.MethodPatch, base+"/tags/"+workID, map[string]any{"name": "job", "color_argb": 4278190335})
	want(t, "rename", code, 200, m)
	if m["name"] != "job" || b2bNum(m["color_argb"]) != 4278190335 || b2bNum(m["entry_count"]) != 1 {
		t.Fatalf("renamed: %v", m)
	}
	_, m, _ = c.do(http.MethodGet, base+"/entries", nil)
	if tags := b2bItems(m)[0]["tags"]; !hasStr(tags, "job") || hasStr(tags, "work") {
		t.Fatalf("entry tags after rename: %v", tags)
	}
	code, m, _ = c.do(http.MethodPatch, base+"/tags/"+workID, map[string]any{"name": "c0"})
	wantErr(t, "rename conflict", code, 409, m, "conflict")
	code, _, _ = c.do(http.MethodDelete, base+"/tags/"+workID, nil)
	want(t, "delete tag", code, 204, nil)
	_, m, _ = c.do(http.MethodGet, base+"/entries", nil)
	if tags := b2bItems(m)[0]["tags"]; hasStr(tags, "job") || !hasStr(tags, "c0") {
		t.Fatalf("entry tags after delete: %v", tags)
	}
	code, _, _ = c.do(http.MethodPatch, base+"/tags/999", map[string]any{"name": "zz"})
	want(t, "missing tag", code, 404, nil)
}

func TestEntriesFilterBulkAndFromDevices(t *testing.T) {
	e := newB2Env(t, b2opts{})
	c, au := e.as("alice", store.RoleUser)
	admin, _ := e.as("root", store.RoleAdmin)
	_, bu := e.as("bob", store.RoleUser)
	book, _ := e.st.AddressBooks.EnsurePersonal(e.ctx, au.ID)
	base := "/address-books/" + strconv.FormatInt(book.ID, 10)
	ids := map[string]int64{}
	for _, en := range []struct {
		id, alias string
		tags      []string
	}{{"111111", "zeta", []string{"a", "b"}}, {"222222", "alpha", []string{"a"}}, {"333333", "Mid", nil}} {
		code, m, _ := c.do(http.MethodPost, base+"/entries", map[string]any{"rustdesk_id": en.id, "alias": en.alias, "tags": en.tags})
		want(t, "seed", code, 201, m)
		ids[en.id] = b2bNum(m["id"])
	}
	check := func(q string, wantIDs ...string) {
		t.Helper()
		code, m, _ := c.do(http.MethodGet, base+"/entries"+q, nil)
		l := b2bItems(m)
		if code != 200 || len(l) != len(wantIDs) {
			t.Fatalf("%s: %d %v", q, code, m)
		}
		for i, id := range wantIDs {
			if l[i]["rustdesk_id"] != id {
				t.Fatalf("%s: item %d = %v, want %s", q, i, l[i]["rustdesk_id"], id)
			}
		}
	}
	check("", "222222", "333333", "111111") // alias, case-insensitive
	check("?sort=-rustdesk_id", "333333", "222222", "111111")
	check("?tag=a", "222222", "111111")
	check("?tag=a&tag=b", "111111")
	check("?q=MID", "333333")
	check("?page=2&page_size=2", "111111")
	_, m, _ := c.do(http.MethodGet, base+"/entries?page=2&page_size=2", nil)
	if b2bNum(m["total"]) != 3 || b2bNum(m["page"]) != 2 {
		t.Fatalf("b2bPaging: %v", m)
	}

	bulk := func(body map[string]any) map[string]any {
		t.Helper()
		code, m, _ := c.do(http.MethodPost, base+"/entries/bulk", body)
		want(t, "bulk", code, 200, m)
		return m
	}
	m = bulk(map[string]any{"action": "add_tags", "entry_ids": []int64{ids["333333"], ids["222222"], 999999}, "tags": []string{"new"}})
	if b2bNum(m["affected"]) != 2 || b2bNum(m["skipped"]) != 1 {
		t.Fatalf("add_tags: %v", m)
	}
	check("?tag=new", "222222", "333333")
	bulk(map[string]any{"action": "remove_tags", "entry_ids": []int64{ids["222222"]}, "tags": []string{"new", "a"}})
	check("?tag=a", "111111")
	bulk(map[string]any{"action": "set_tags", "entry_ids": []int64{ids["111111"]}, "tags": []string{}})
	check("?tag=b")
	m = bulk(map[string]any{"action": "delete", "entry_ids": []int64{ids["111111"], ids["111111"]}})
	if b2bNum(m["affected"]) != 1 || b2bNum(m["skipped"]) != 0 {
		t.Fatalf("delete: %v", m)
	}
	check("", "222222", "333333")
	code, m, _ := c.do(http.MethodPost, base+"/entries/bulk", map[string]any{"action": "add_tags", "entry_ids": []int64{1}})
	wantErr(t, "add_tags without tags", code, 400, m, "validation_failed")
	code, m, _ = c.do(http.MethodPost, base+"/entries/bulk", map[string]any{"action": "explode", "entry_ids": []int64{1}})
	wantErr(t, "bad action", code, 400, m, "validation_failed")
	code, m, _ = c.do(http.MethodPost, base+"/entries/bulk", map[string]any{"action": "delete", "entry_ids": []int64{}})
	wantErr(t, "empty ids", code, 400, m, "validation_failed")

	// from-devices: own device ok, foreign device refused for users, present ids skipped.
	own := &store.Device{RustdeskID: "444444", Hostname: "host4", OS: "Windows 11", OSUsername: "al", OwnerUserID: &au.ID}
	foreign := &store.Device{RustdeskID: "666666", Hostname: "host6", OS: "Linux", OwnerUserID: &bu.ID}
	dup := &store.Device{RustdeskID: "222222", Hostname: "dup", OwnerUserID: &au.ID}
	for _, d := range []*store.Device{own, foreign, dup} {
		if err := e.st.Devices.Create(e.ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	e.pres.set("444444", true)
	code, m, _ = c.do(http.MethodPost, base+"/entries/from-devices", map[string]any{
		"device_ids": []int64{own.ID, foreign.ID, dup.ID, 424242}, "tags": []string{"imported"}})
	want(t, "from-devices", code, 200, m)
	if b2bNum(m["affected"]) != 1 || b2bNum(m["skipped"]) != 1 || len(m["errors"].([]any)) != 2 {
		t.Fatalf("from-devices: %v", m)
	}
	_, m, _ = c.do(http.MethodGet, base+"/entries?tag=imported", nil)
	l := b2bItems(m)
	if len(l) != 1 || l[0]["alias"] != "host4" || l[0]["platform"] != "Windows" || l[0]["os_username"] != "al" ||
		l[0]["online"] != true || b2bNum(l[0]["device_id"]) != own.ID {
		t.Fatalf("imported entry: %v", l)
	}
	// Admins may add any device.
	code, m, _ = admin.do(http.MethodPost, base+"/entries/from-devices", map[string]any{"device_ids": []int64{foreign.ID}})
	if code != 200 || b2bNum(m["affected"]) != 1 {
		t.Fatalf("admin from-devices: %d %v", code, m)
	}
	_, m, _ = c.do(http.MethodGet, base+"/entries?q=666666", nil)
	if l := b2bItems(m); len(l) != 1 || l[0]["online"] != false || l[0]["platform"] != "Linux" {
		t.Fatalf("foreign entry: %v", l)
	}
	_, m, _ = c.do(http.MethodGet, base, nil)
	if b2bNum(m["entry_count"]) != 4 {
		t.Fatalf("entry_count: %v", m)
	}
}
