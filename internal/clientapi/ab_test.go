package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bropines/rustdesk-panel/internal/store"
)

func TestAddressBookPersonal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("alice", "pw-alice-123", nil)
	tok := e.login("alice", "pw-alice-123", "", "")

	// Bodyless POSTs (Content-Length: 0) are accepted on every read endpoint.
	e.golden("ab_personal", e.do("POST", "/api/ab/personal", "", tok), 200)
	e.golden("ab_personal", e.do("POST", "/api/ab/personal", "", tok), 200) // stable
	e.golden("ab_settings", e.do("POST", "/api/ab/settings", "", tok), 200)
	e.golden("ab_shared_profiles_empty", e.do("POST", "/api/ab/shared/profiles?current=1&pageSize=100", "", tok), 200)
	e.golden("ab_peers_empty", e.do("POST", "/api/ab/peers?current=1&pageSize=100&ab=guid-1", "", tok), 200)
	e.golden("ab_tags_empty", e.do("POST", "/api/ab/tags/guid-1", "", tok), 200)

	// Writes: exactly 200 + empty body.
	e.empty("POST", "/api/ab/tag/add/guid-1", `{"name":"work","color":4288585374}`, tok)
	e.empty("POST", "/api/ab/tag/add/guid-1", `{"name":"home","color":-16776961}`, tok) // int32 ARGB
	e.empty("POST", "/api/ab/peer/add/guid-1", `{"id":"123456789","alias":"Office <PC> & co","tags":["work","new"],
		"hash":"c2FsdGVkK2hhc2g=","username":"bob","hostname":"OFFICE","platform":"Windows","note":"first"}`, tok)
	e.empty("POST", "/api/ab/peer/add/guid-1", `{"id":"987654321","alias":"","tags":[],"forceAlwaysRelay":"true","rdpPort":3389,"same_server":true}`, tok)
	e.golden("ab_peers", e.do("POST", "/api/ab/peers?current=1&pageSize=100&ab=guid-1", "", tok), 200)
	e.golden("ab_tags", e.do("POST", "/api/ab/tags/guid-1", "", tok), 200)

	// Partial update: absent keys (note!) are kept; sync-from-recent fields apply.
	e.empty("PUT", "/api/ab/peer/update/guid-1", `{"id":"123456789","alias":"Office","hostname":"OFFICE-2","username":"carol"}`, tok)
	e.empty("PUT", "/api/ab/peer/update/guid-1", `{"id":"123456789","note":"second"}`, tok)
	en, _ := e.st.AddressBooks.GetEntry(ctx, mustBook(t, e, "guid-1").ID, "123456789")
	if en.Note != "second" || en.Hostname != "OFFICE-2" || en.OSUsername != "carol" || en.Hash != "c2FsdGVkK2hhc2g=" ||
		len(en.Tags) != 2 {
		t.Fatalf("partial update: %+v", en)
	}
	e.golden("ab_peer_not_found", e.do("PUT", "/api/ab/peer/update/guid-1", `{"id":"000","alias":"x"}`, tok), 404)
	e.golden("ab_invalid_peer", e.do("POST", "/api/ab/peer/add/guid-1", `{"alias":"no id"}`, tok), 400)

	// Tag rename rewrites peers' tags; update changes colour; delete removes from peers.
	e.empty("PUT", "/api/ab/tag/rename/guid-1", `{"old":"work","new":"job"}`, tok)
	e.golden("ab_tag_exists", e.do("PUT", "/api/ab/tag/rename/guid-1", `{"old":"job","new":"home"}`, tok), 409)
	e.golden("ab_tag_not_found", e.do("PUT", "/api/ab/tag/rename/guid-1", `{"old":"zzz","new":"y"}`, tok), 404)
	e.empty("PUT", "/api/ab/tag/update/guid-1", `{"name":"job","color":4278190335}`, tok)
	e.golden("ab_tag_not_found", e.do("PUT", "/api/ab/tag/update/guid-1", `{"name":"zzz","color":1}`, tok), 404)
	e.empty("DELETE", "/api/ab/tag/guid-1", `["new"]`, tok)
	e.golden("ab_peers_after_tags", e.do("POST", "/api/ab/peers?ab=guid-1", "", tok), 200)
	e.golden("ab_tags_after", e.do("POST", "/api/ab/tags/guid-1", "", tok), 200)

	// DELETE carries a JSON array body (required).
	e.golden("invalid_request", e.do("DELETE", "/api/ab/peer/guid-1", "", tok), 400)
	e.empty("DELETE", "/api/ab/peer/guid-1", `["987654321","unknown"]`, tok)
	r := e.mustDo("POST", "/api/ab/peers?ab=guid-1", "", tok, 200)
	if !strings.HasPrefix(r.body, `{"total":1,`) {
		t.Fatalf("after delete: %s", r.body)
	}

	// Secrets are encrypted at rest.
	var raw []byte
	_ = e.st.Querier().QueryRowContext(ctx, `SELECT hash_enc FROM address_book_entries WHERE rustdesk_id = '123456789'`).Scan(&raw)
	if len(raw) == 0 || bytes.Contains(raw, []byte("c2FsdGVkK2hhc2g=")) {
		t.Fatal("hash not encrypted")
	}

	// Pagination: current/pageSize, total from the full set.
	for i := 0; i < 3; i++ {
		e.empty("POST", "/api/ab/peer/add/guid-1", fmt.Sprintf(`{"id":"p%d"}`, i), tok)
	}
	r = e.mustDo("POST", "/api/ab/peers?current=2&pageSize=2&ab=guid-1", "", tok, 200)
	var pg struct {
		Total int      `json:"total"`
		Data  []abPeer `json:"data"`
	}
	_ = json.Unmarshal([]byte(r.body), &pg)
	if pg.Total != 4 || len(pg.Data) != 2 || pg.Data[0].ID != "p1" {
		t.Fatalf("paging: %s", r.body)
	}

	// Legacy guid "<group>-<user>-0" still resolves to the personal book.
	e.mustDo("POST", fmt.Sprintf("/api/ab/tags/7-%d-0", u.ID), "", tok, 200)
	// Unknown guid / no access: 404. Bad token: 401.
	e.golden("ab_not_found", e.do("POST", "/api/ab/peers?ab=nope", "", tok), 404)
	e.golden("unauthorized", e.do("POST", "/api/ab/personal", "", "bad"), 401)
}

func mustBook(t *testing.T, e *env, guid string) *store.AddressBook {
	t.Helper()
	b, err := e.st.AddressBooks.GetByGUID(context.Background(), guid)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAddressBookSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.user("u1", "pw-u1-12345", nil)
	tok := e.login("u1", "pw-u1-12345", "", "")
	_ = e.st.Settings.Set(ctx, SettingABMaxPeers, 1, nil)
	e.golden("ab_settings_limited", e.do("POST", "/api/ab/settings", "", tok), 200)
	e.mustDo("POST", "/api/ab/personal", "", tok, 200)
	e.empty("POST", "/api/ab/peer/add/guid-1", `{"id":"1"}`, tok)
	e.empty("POST", "/api/ab/peer/add/guid-1", `{"id":"1","alias":"again"}`, tok) // update is fine
	e.golden("ab_exceed", e.do("POST", "/api/ab/peer/add/guid-1", `{"id":"2"}`, tok), 400)

	// Legacy-only mode: /api/ab/personal answers 404 (never 200 null).
	_ = e.st.Settings.Set(ctx, SettingABLegacyOnly, true, nil)
	r := e.mustDo("POST", "/api/ab/personal", "", tok, 404)
	if !strings.Contains(r.body, `"error"`) {
		t.Fatalf("legacy mode body: %s", r.body)
	}
}

func TestAddressBookShared(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	grp := &store.UserGroup{Name: "team"}
	_ = e.st.UserGroups.Create(ctx, grp)
	owner := e.user("owner", "pw-owner-1", nil)
	reader := e.user("reader", "pw-reader-1", func(u *store.User) { u.UserGroupID = &grp.ID })
	writer := e.user("writer", "pw-writer-1", nil)
	e.user("stranger", "pw-stranger-1", nil)
	book := &store.AddressBook{OwnerUserID: owner.ID, Name: "Servers", Note: "prod", DefaultPassword: "Sh4red!"}
	if err := e.st.AddressBooks.Create(ctx, book); err != nil {
		t.Fatal(err)
	}
	other := &store.AddressBook{OwnerUserID: owner.ID, Name: "Archive"}
	_ = e.st.AddressBooks.Create(ctx, other)
	_ = e.st.AddressBooks.AddShare(ctx, book.ID, nil, &grp.ID, store.ABRead)
	_ = e.st.AddressBooks.AddShare(ctx, book.ID, &writer.ID, nil, store.ABReadWrite)
	_ = e.st.AddressBooks.AddShare(ctx, other.ID, &writer.ID, nil, store.ABFull)
	_ = reader

	ownerTok := e.login("owner", "pw-owner-1", "", "")
	readerTok := e.login("reader", "pw-reader-1", "", "")
	writerTok := e.login("writer", "pw-writer-1", "", "")
	strangerTok := e.login("stranger", "pw-stranger-1", "", "")

	e.golden("ab_shared_profiles_owner", e.do("POST", "/api/ab/shared/profiles?current=1&pageSize=100", "", ownerTok), 200)
	e.golden("ab_shared_profiles_reader", e.do("POST", "/api/ab/shared/profiles?current=1&pageSize=100", "", readerTok), 200)
	e.golden("ab_shared_profiles_writer", e.do("POST", "/api/ab/shared/profiles", "", writerTok), 200)

	g := book.GUID
	// Shared books carry plain-text passwords.
	e.empty("POST", "/api/ab/peer/add/"+g, `{"id":"111222333","alias":"db","password":"p@ss\"word","tags":["prod"]}`, writerTok)
	e.golden("ab_shared_peers", e.do("POST", "/api/ab/peers?ab="+g, "", readerTok), 200)

	// rule 1: read only.
	e.golden("forbidden", e.do("POST", "/api/ab/peer/add/"+g, `{"id":"1"}`, readerTok), 403)
	e.golden("forbidden", e.do("POST", "/api/ab/tag/add/"+g, `{"name":"x","color":1}`, readerTok), 403)
	// rule 2: add/update/tags but no deletes.
	e.empty("PUT", "/api/ab/peer/update/"+g, `{"id":"111222333","note":"primary"}`, writerTok)
	e.empty("POST", "/api/ab/tag/add/"+g, `{"name":"x","color":1}`, writerTok)
	e.golden("forbidden", e.do("DELETE", "/api/ab/peer/"+g, `["111222333"]`, writerTok), 403)
	e.golden("forbidden", e.do("DELETE", "/api/ab/tag/"+g, `["x"]`, writerTok), 403)
	// rule 3 / owner: deletes.
	e.empty("DELETE", "/api/ab/tag/"+g, `["x"]`, ownerTok)
	e.empty("DELETE", "/api/ab/tag/"+other.GUID, `["none"]`, writerTok)
	// No access at all: 404 (existence not revealed).
	e.golden("ab_not_found", e.do("POST", "/api/ab/peers?ab="+g, "", strangerTok), 404)
	e.golden("ab_not_found", e.do("POST", "/api/ab/tags/"+g, "", strangerTok), 404)
}

func TestAddressBookLegacy(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.user("lee", "pw-lee-12345", nil)
	tok := e.login("lee", "pw-lee-12345", "", "")

	e.golden("ab_legacy_empty", e.do("GET", "/api/ab", "", tok), 200)

	blob := map[string]any{
		"tags": []string{"a", "b"},
		"peers": []map[string]any{
			{"id": "100200300", "username": "u", "hostname": "h", "platform": "Linux", "alias": "<x>", "tags": []string{"a", "c"}, "hash": "aGFzaA=="},
			{"id": "400500600", "tags": []string{}},
		},
		"tag_colors": `{"a":4288585374,"b":4278255360}`,
	}
	data, _ := json.Marshal(blob)
	body, _ := json.Marshal(map[string]string{"data": string(data)})
	e.empty("POST", "/api/ab", string(body), tok)
	e.golden("ab_legacy", e.do("GET", "/api/ab", "", tok), 200)
	e.golden("ab_legacy", e.do("POST", "/api/ab/get", "{}", tok), 200) // Sciter

	// The same book is the personal book of the new API.
	e.mustDo("POST", "/api/ab/personal", "", tok, 200)
	r := e.mustDo("POST", "/api/ab/peers?ab=guid-1", "", tok, 200)
	if !strings.Contains(r.body, `"hash":"aGFzaA=="`) {
		t.Fatalf("new API view: %s", r.body)
	}
	// A note set through the new API survives a legacy push without "note".
	e.empty("PUT", "/api/ab/peer/update/guid-1", `{"id":"100200300","note":"keep me"}`, tok)
	e.empty("POST", "/api/ab", string(body), tok)
	b, _ := e.st.AddressBooks.GetPersonal(ctx, 1)
	en, _ := e.st.AddressBooks.GetEntry(ctx, b.ID, "100200300")
	if en.Note != "keep me" {
		t.Fatalf("note lost: %+v", en)
	}
	// Full replace: an empty blob clears the book.
	empty, _ := json.Marshal(map[string]string{"data": `{"tags":[],"peers":[],"tag_colors":"{}"}`})
	e.empty("POST", "/api/ab", string(empty), tok)
	e.golden("ab_legacy_empty", e.do("GET", "/api/ab", "", tok), 200)
	e.golden("invalid_request", e.do("POST", "/api/ab", `{"nodata":1}`, tok), 400)
}
