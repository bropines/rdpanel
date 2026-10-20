package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
)

func newSecretStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	d := dbtest.Open(t)
	aead, err := crypto.NewAEAD(bytes.Repeat([]byte{7}, crypto.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	gen := func() string { n++; return fmt.Sprintf("guid-%d", n) }
	clk := &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	return New(d, WithSecrets(aead), WithIDGenerator(gen)).WithClock(clk.now), clk
}

func ptr[T any](v T) *T { return &v }

func TestNewUUID(t *testing.T) {
	a, b := NewUUID(), NewUUID()
	if len(a) != 36 || a == b || a[14] != '4' || a[8] != '-' {
		t.Fatalf("uuid %q %q", a, b)
	}
}

func TestDevices(t *testing.T) {
	s, clk := newSecretStore(t)
	ctx := context.Background()
	u := &User{Username: "owner"}
	_ = s.Users.Create(ctx, u)
	g := &DeviceGroup{Name: "Office"}
	if err := s.DeviceGroups.Create(ctx, g); err != nil {
		t.Fatal(err)
	}

	d := &Device{RustdeskID: "123456789", UUID: "dXVpZA==", Hostname: "HOST", OwnerUserID: &u.ID, DeviceGroupID: &g.ID}
	if err := s.Devices.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Devices.Create(ctx, &Device{RustdeskID: "123456789"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup: %v", err)
	}
	got, err := s.Devices.GetByRustdeskID(ctx, "123456789")
	if err != nil || got.OwnerUsername != "owner" || got.DeviceGroupName != "Office" || got.UUID != "dXVpZA==" {
		t.Fatalf("%+v %v", got, err)
	}
	if !got.SysinfoPending() {
		t.Fatal("never uploaded => pending")
	}

	name := "alice"
	if err := s.Devices.UpdateSysinfo(ctx, d.ID, Sysinfo{UUID: "dXVpZA==", Hostname: "H2", OS: "windows / Windows 11",
		CPU: "cpu", Memory: "8GB", ClientVersion: "1.4.9", OSUsername: &name}); err != nil {
		t.Fatal(err)
	}
	_ = s.Devices.UpdateSysinfo(ctx, d.ID, Sysinfo{UUID: "dXVpZA==", Hostname: "H2", OS: "windows / Windows 11"})
	got, _ = s.Devices.Get(ctx, d.ID)
	if got.OSUsername != "alice" || got.Hostname != "H2" || got.SysinfoPending() {
		t.Fatalf("sysinfo: %+v", got)
	}
	clk.advance(time.Second)
	_ = s.Devices.RequestSysinfo(ctx, d.ID)
	got, _ = s.Devices.Get(ctx, d.ID)
	if !got.SysinfoPending() {
		t.Fatal("requested => pending")
	}

	// Heartbeat throttling.
	if w, _ := s.Devices.TouchHeartbeat(ctx, d.ID, "1.2.3.4", 30*time.Second); !w {
		t.Fatal("first heartbeat must write")
	}
	clk.advance(3 * time.Second)
	if w, _ := s.Devices.TouchHeartbeat(ctx, d.ID, "1.2.3.4", 30*time.Second); w {
		t.Fatal("throttled heartbeat wrote")
	}
	if w, _ := s.Devices.TouchHeartbeat(ctx, d.ID, "5.6.7.8", 30*time.Second); !w {
		t.Fatal("ip change must write")
	}
	clk.advance(31 * time.Second)
	if w, _ := s.Devices.TouchHeartbeat(ctx, d.ID, "5.6.7.8", 30*time.Second); !w {
		t.Fatal("interval elapsed must write")
	}

	// Owner binding.
	other := &User{Username: "other"}
	_ = s.Users.Create(ctx, other)
	if ok, _ := s.Devices.BindOwner(ctx, "123456789", "wrong", other.ID); ok {
		t.Fatal("bound with wrong uuid")
	}
	if ok, _ := s.Devices.BindOwner(ctx, "123456789", "dXVpZA==", other.ID); !ok {
		t.Fatal("not bound")
	}
	_ = s.Devices.UnbindOwner(ctx, "123456789", "dXVpZA==", u.ID) // not the owner: no-op
	got, _ = s.Devices.Get(ctx, d.ID)
	if *got.OwnerUserID != other.ID {
		t.Fatal("unbind by non-owner")
	}
	_ = s.Devices.UnbindOwner(ctx, "123456789", "dXVpZA==", other.ID)
	got, _ = s.Devices.Get(ctx, d.ID)
	if got.OwnerUserID != nil {
		t.Fatal("unbind failed")
	}

	// GuessOwner from client tokens.
	if _, err := s.Devices.GuessOwner(ctx, "123456789", "dXVpZA=="); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = s.AuthTokens.Create(ctx, &AuthToken{UserID: u.ID, Kind: TokenClient, TokenSHA256: crypto.HashToken("a"),
		ExpiresAt: clk.now().Add(time.Hour), DeviceRustdeskID: "123456789", DeviceUUID: "dXVpZA=="})
	if id, err := s.Devices.GuessOwner(ctx, "123456789", "dXVpZA=="); err != nil || id != u.ID {
		t.Fatalf("guess %d %v", id, err)
	}

	// Identity / pk.
	pk := bytes.Repeat([]byte{1}, 32)
	if err := s.Devices.SetIdentity(ctx, d.ID, "bmV3", pk); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Devices.Get(ctx, d.ID)
	if got.UUID != "bmV3" || !bytes.Equal(got.PublicKey, pk) {
		t.Fatalf("identity %+v", got)
	}
	if err := s.Devices.SetIdentity(ctx, d.ID, "bmV3", []byte{1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short pk: %v", err)
	}

	// Scoped listing.
	grp := &UserGroup{Name: "team", Visibility: VisibilityShared}
	_ = s.UserGroups.Create(ctx, grp)
	u.UserGroupID = &grp.ID
	_ = s.Users.Update(ctx, u)
	d2 := &Device{RustdeskID: "222", OwnerUserID: &u.ID}
	_ = s.Devices.Create(ctx, d2)
	_ = s.Devices.Create(ctx, &Device{RustdeskID: "333", OwnerUserID: &other.ID, DeviceGroupID: &g.ID})
	all, total, err := s.Devices.List(ctx, ListDevicesParams{})
	if err != nil || total != 3 || len(all) != 3 {
		t.Fatalf("all %d %v", total, err)
	}
	mine, total, _ := s.Devices.List(ctx, ListDevicesParams{Scope: DeviceScope{OwnerUserID: &u.ID}})
	if total != 1 || mine[0].RustdeskID != "222" {
		t.Fatalf("mine %d", total)
	}
	team, total, _ := s.Devices.List(ctx, ListDevicesParams{Scope: DeviceScope{OwnerGroupID: &grp.ID}})
	if total != 1 || team[0].RustdeskID != "222" {
		t.Fatalf("team %d", total)
	}
	page, total, _ := s.Devices.List(ctx, ListDevicesParams{Limit: 1, Offset: 1})
	if total != 3 || len(page) != 1 || page[0].RustdeskID != "222" {
		t.Fatal("paging")
	}
	if _, total, _ := s.Devices.List(ctx, ListDevicesParams{Query: "33"}); total != 1 {
		t.Fatal("query")
	}

	groups, total, err := s.DeviceGroups.List(ctx, DeviceScope{}, 10, 0)
	if err != nil || total != 1 || groups[0].Name != "Office" {
		t.Fatalf("groups %v", err)
	}
	if _, total, _ := s.DeviceGroups.List(ctx, DeviceScope{OwnerUserID: &u.ID}, 10, 0); total != 0 {
		t.Fatalf("scoped groups %d", total)
	}
	if _, total, _ := s.DeviceGroups.List(ctx, DeviceScope{OwnerUserID: &other.ID}, 10, 0); total != 1 {
		t.Fatalf("scoped groups other %d", total)
	}
	if g2, err := s.DeviceGroups.GetByName(ctx, "office"); err != nil || g2.ID != g.ID {
		t.Fatal("by name")
	}
	if err := s.Devices.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAddressBooks(t *testing.T) {
	s, _ := newSecretStore(t)
	ctx := context.Background()
	grp := &UserGroup{Name: "g"}
	_ = s.UserGroups.Create(ctx, grp)
	alice := &User{Username: "alice", UserGroupID: &grp.ID}
	bob := &User{Username: "bob", UserGroupID: &grp.ID}
	carol := &User{Username: "carol"}
	for _, u := range []*User{alice, bob, carol} {
		if err := s.Users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	pb, err := s.AddressBooks.EnsurePersonal(ctx, alice.ID)
	if err != nil || pb.GUID != "guid-1" || !pb.IsPersonal || pb.OwnerUsername != "alice" {
		t.Fatalf("%+v %v", pb, err)
	}
	again, _ := s.AddressBooks.EnsurePersonal(ctx, alice.ID)
	if again.ID != pb.ID {
		t.Fatal("second personal book")
	}

	// Legacy guid resolution: stored and derived.
	if b, err := s.AddressBooks.GetByGUID(ctx, fmt.Sprintf("9-%d-0", alice.ID)); err != nil || b.ID != pb.ID {
		t.Fatalf("derived legacy personal: %v", err)
	}
	shared := &AddressBook{OwnerUserID: alice.ID, Name: "Team", DefaultPassword: "s3cret",
		LegacyCollectionID: ptr(int64(5)), LegacyGUID: fmt.Sprintf("1-%d-5", alice.ID)}
	if err := s.AddressBooks.Create(ctx, shared); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{shared.GUID, shared.LegacyGUID, fmt.Sprintf("2-%d-5", alice.ID)} {
		b, err := s.AddressBooks.GetByGUID(ctx, g)
		if err != nil || b.ID != shared.ID || b.DefaultPassword != "s3cret" {
			t.Fatalf("guid %q: %+v %v", g, b, err)
		}
	}
	for _, g := range []string{"", "nope", "1-0-0", "01-2-3", "1-2-3-4", fmt.Sprintf("1-%d-77", alice.ID)} {
		if _, err := s.AddressBooks.GetByGUID(ctx, g); !errors.Is(err, ErrNotFound) {
			t.Fatalf("guid %q: %v", g, err)
		}
	}

	// Permissions.
	if p, _ := s.AddressBooks.Permission(ctx, shared, alice.ID); p != ABFull {
		t.Fatal("owner must be full")
	}
	if p, _ := s.AddressBooks.Permission(ctx, shared, bob.ID); p != ABNone {
		t.Fatal("no grant")
	}
	_ = s.AddressBooks.AddShare(ctx, shared.ID, nil, &grp.ID, ABRead)
	_ = s.AddressBooks.AddShare(ctx, shared.ID, &bob.ID, nil, ABReadWrite)
	if p, _ := s.AddressBooks.Permission(ctx, shared, bob.ID); p != ABReadWrite {
		t.Fatalf("max(user,group) = %v", p)
	}
	_ = s.AddressBooks.AddShare(ctx, shared.ID, &bob.ID, nil, ABRead) // replace
	if p, _ := s.AddressBooks.Permission(ctx, shared, bob.ID); p != ABRead {
		t.Fatalf("replaced grant = %v", p)
	}
	if err := s.AddressBooks.AddShare(ctx, shared.ID, nil, nil, ABRead); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	list, total, err := s.AddressBooks.ListShared(ctx, bob.ID, 10, 0)
	if err != nil || total != 1 || list[0].Rule != ABRead || list[0].Name != "Team" {
		t.Fatalf("shared for bob: %v %d", err, total)
	}
	list, total, _ = s.AddressBooks.ListShared(ctx, alice.ID, 10, 0)
	if total != 1 || list[0].Rule != ABFull {
		t.Fatal("owner shared list")
	}
	if _, total, _ := s.AddressBooks.ListShared(ctx, carol.ID, 10, 0); total != 0 {
		t.Fatal("carol sees nothing")
	}

	// Entries with secrets, tags, partial updates.
	created, err := s.AddressBooks.UpsertEntry(ctx, pb.ID, "111", ABEntryPatch{Alias: ptr("pc"), Hash: ptr("aGFzaA=="),
		Tags: &[]string{"work", "home", "work"}, ForceAlwaysRelay: ptr(true)})
	if err != nil || !created {
		t.Fatal(err)
	}
	created, _ = s.AddressBooks.UpsertEntry(ctx, pb.ID, "111", ABEntryPatch{Note: ptr("n")})
	if created {
		t.Fatal("upsert of existing reported created")
	}
	e, err := s.AddressBooks.GetEntry(ctx, pb.ID, "111")
	if err != nil || e.Alias != "pc" || e.Hash != "aGFzaA==" || e.Note != "n" || !e.ForceAlwaysRelay ||
		!reflect.DeepEqual(e.Tags, []string{"home", "work"}) {
		t.Fatalf("entry %+v %v", e, err)
	}
	// Secrets are encrypted at rest and bound to the row.
	var raw []byte
	_ = s.Querier().QueryRowContext(ctx, `SELECT hash_enc FROM address_book_entries WHERE id = ?`, e.ID).Scan(&raw)
	if len(raw) == 0 || bytes.Contains(raw, []byte("aGFzaA==")) {
		t.Fatal("hash stored in plain text")
	}
	_, _ = s.AddressBooks.UpsertEntry(ctx, pb.ID, "222", ABEntryPatch{Hash: ptr("other")})
	_, _ = s.Querier().ExecContext(ctx, `UPDATE address_book_entries SET hash_enc = ? WHERE rustdesk_id = '222'`, raw)
	if _, err := s.AddressBooks.GetEntry(ctx, pb.ID, "222"); err == nil {
		t.Fatal("ciphertext moved between rows must not decrypt")
	}
	_, _ = s.AddressBooks.DeleteEntries(ctx, pb.ID, []string{"222"})

	if err := s.AddressBooks.PatchEntry(ctx, pb.ID, "nope", ABEntryPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = s.AddressBooks.PatchEntry(ctx, pb.ID, "111", ABEntryPatch{Hash: ptr(""), Tags: &[]string{"work"}})
	e, _ = s.AddressBooks.GetEntry(ctx, pb.ID, "111")
	if e.Hash != "" || !reflect.DeepEqual(e.Tags, []string{"work"}) {
		t.Fatalf("clear hash: %+v", e)
	}

	// Tags.
	tags, _ := s.AddressBooks.ListTags(ctx, pb.ID)
	if len(tags) != 2 || tags[0].Name != "work" || tags[0].Color != DefaultTagColor {
		t.Fatalf("tags %+v", tags)
	}
	if err := s.AddressBooks.RenameTag(ctx, pb.ID, "work", "home"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := s.AddressBooks.RenameTag(ctx, pb.ID, "work", "job"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddressBooks.RenameTag(ctx, pb.ID, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	e, _ = s.AddressBooks.GetEntry(ctx, pb.ID, "111")
	if !reflect.DeepEqual(e.Tags, []string{"job"}) {
		t.Fatalf("rename must follow: %v", e.Tags)
	}
	_ = s.AddressBooks.UpsertTag(ctx, pb.ID, "job", 4294967295)
	if err := s.AddressBooks.SetTagColor(ctx, pb.ID, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	tags, _ = s.AddressBooks.ListTags(ctx, pb.ID)
	if tags[0].Color != 4294967295 {
		t.Fatalf("color %d", tags[0].Color)
	}
	if n, _ := s.AddressBooks.DeleteTags(ctx, pb.ID, []string{"job", "nope"}); n != 1 {
		t.Fatal("delete tags")
	}
	e, _ = s.AddressBooks.GetEntry(ctx, pb.ID, "111")
	if len(e.Tags) != 0 {
		t.Fatal("deleted tag still on entry")
	}

	// Legacy full replace.
	err = s.AddressBooks.ReplaceAll(ctx, pb.ID, []LegacyEntry{
		{RustdeskID: "111", Patch: ABEntryPatch{Alias: ptr("renamed"), Tags: &[]string{"a"}}},
		{RustdeskID: "333", Patch: ABEntryPatch{Hostname: ptr("h3"), Tags: &[]string{"b"}}},
	}, []string{"a", "c"}, map[string]uint32{"a": 1, "c": 2})
	if err != nil {
		t.Fatal(err)
	}
	entries, total, err := s.AddressBooks.ListEntries(ctx, pb.ID, 0, 0)
	if err != nil || total != 2 || entries[0].Alias != "renamed" || entries[0].Note != "n" ||
		!reflect.DeepEqual(entries[1].Tags, []string{"b"}) {
		t.Fatalf("replace: %+v %v", entries, err)
	}
	tags, _ = s.AddressBooks.ListTags(ctx, pb.ID)
	got := map[string]uint32{}
	for _, tg := range tags {
		got[tg.Name] = tg.Color
	}
	if !reflect.DeepEqual(got, map[string]uint32{"home": 0, "a": 1, "c": 2, "b": DefaultTagColor}) &&
		!reflect.DeepEqual(got, map[string]uint32{"a": 1, "c": 2, "b": DefaultTagColor}) {
		t.Fatalf("tags after replace: %v", got)
	}
	if _, ok := got["home"]; ok {
		t.Fatal("unlisted tag kept")
	}
	page, total, _ := s.AddressBooks.ListEntries(ctx, pb.ID, 1, 1)
	if total != 2 || len(page) != 1 || page[0].RustdeskID != "333" || page[0].Tags[0] != "b" {
		t.Fatal("entry paging")
	}

	// Without secrets configured, secret writes fail loudly.
	noKey := New(s.db)
	if _, err := noKey.AddressBooks.UpsertEntry(ctx, pb.ID, "999", ABEntryPatch{Password: ptr("x")}); !errors.Is(err, ErrNoSecrets) {
		t.Fatalf("no key: %v", err)
	}
	if byName, _ := s.AddressBooks.FindSharedByName(ctx, "team"); len(byName) != 1 {
		t.Fatal("find by name")
	}
}

func TestClientAudits(t *testing.T) {
	s, clk := newSecretStore(t)
	ctx := context.Background()

	a := &ConnectionAudit{DeviceID: "123", DeviceUUID: "u", ConnID: 3, SessionID: "18446744073709551615", ControllerIP: "1.2.3.4"}
	if err := s.ConnectionAudits.Create(ctx, a); err != nil || a.GUID != "guid-1" {
		t.Fatal(err)
	}
	open, err := s.ConnectionAudits.FindOpen(ctx, "123", 3, time.Hour)
	if err != nil || open.ID != a.ID {
		t.Fatal(err)
	}
	open.ConnType, open.PrimaryAuth, open.ControllerName = 1, ptr(2), "ctl"
	open.EndedAt = ptr(clk.now())
	if err := s.ConnectionAudits.Update(ctx, open); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConnectionAudits.FindOpen(ctx, "123", 3, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatal("ended record still open")
	}
	ct := 1
	found, err := s.ConnectionAudits.FindBySession(ctx, "123", "18446744073709551615", &ct)
	if err != nil || found.GUID != "guid-1" || *found.PrimaryAuth != 2 || found.SessionID != "18446744073709551615" {
		t.Fatalf("%+v %v", found, err)
	}
	ct = 0
	if _, err := s.ConnectionAudits.FindBySession(ctx, "123", "18446744073709551615", &ct); !errors.Is(err, ErrNotFound) {
		t.Fatal("conn type filter")
	}
	if err := s.ConnectionAudits.SetNote(ctx, "guid-1", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConnectionAudits.SetNote(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	list, _ := s.ConnectionAudits.List(ctx, ClientAuditFilter{DeviceID: "123"})
	if len(list) != 1 || list[0].Note != "hello" {
		t.Fatal("list")
	}

	f := &FileTransferAudit{DeviceID: "123", Info: `{"num":2}`, IsFile: true}
	if err := s.FileTransferAudits.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	_ = s.FileTransferAudits.Create(ctx, &FileTransferAudit{DeviceID: "123", Info: "not json"})
	files, _ := s.FileTransferAudits.List(ctx, ClientAuditFilter{})
	if len(files) != 2 || files[0].Info != `{"raw":"not json"}` || !files[1].IsFile {
		t.Fatalf("files %+v", files)
	}

	if err := s.AlarmAudits.Create(ctx, &AlarmAudit{DeviceID: "123", AlarmType: 1, ConnID: ptr(int64(4))}); err != nil {
		t.Fatal(err)
	}
	alarms, _ := s.AlarmAudits.List(ctx, ClientAuditFilter{})
	if len(alarms) != 1 || alarms[0].Info != "{}" || *alarms[0].ConnID != 4 {
		t.Fatal("alarms")
	}

	if ok, err := s.AuditNonces.Claim(ctx, "n1"); !ok || err != nil {
		t.Fatal("first claim")
	}
	if ok, _ := s.AuditNonces.Claim(ctx, "n1"); ok {
		t.Fatal("second claim must be a duplicate")
	}
	// A rolled-back transaction releases the nonce.
	boom := errors.New("boom")
	_ = s.Tx(ctx, func(tx *Store) error {
		_, _ = tx.AuditNonces.Claim(ctx, "n2")
		return boom
	})
	if ok, _ := s.AuditNonces.Claim(ctx, "n2"); !ok {
		t.Fatal("nonce not released by rollback")
	}
	clk.advance(time.Hour)
	if n, _ := s.AuditNonces.PurgeBefore(ctx, clk.now()); n != 2 {
		t.Fatalf("purged %d", n)
	}
}

func TestLoginEvents(t *testing.T) {
	s, _ := newSecretStore(t)
	ctx := context.Background()
	u := &User{Username: "l"}
	_ = s.Users.Create(ctx, u)
	_ = s.LoginEvents.Record(ctx, &LoginEvent{UserID: &u.ID, Method: LoginMethodPassword, ClientKind: "client", IP: "1.1.1.1"})
	_ = s.LoginEvents.Record(ctx, &LoginEvent{UsernameAttempted: "ghost", Method: LoginMethodPassword,
		Result: LoginFailure, FailureReason: "bad_credentials"})
	if err := s.LoginEvents.Record(ctx, &LoginEvent{Method: "carrier-pigeon"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	all, _ := s.LoginEvents.List(ctx, LoginEventFilter{})
	if len(all) != 2 || all[0].Result != LoginFailure || all[1].Result != LoginSuccess {
		t.Fatalf("%+v", all)
	}
	mine, _ := s.LoginEvents.List(ctx, LoginEventFilter{UserID: &u.ID})
	if len(mine) != 1 || mine[0].IP != "1.1.1.1" {
		t.Fatal("filter")
	}
	_ = s.LoginEvents.Hide(ctx, mine[0].ID, u.ID)
	if mine, _ = s.LoginEvents.List(ctx, LoginEventFilter{UserID: &u.ID}); len(mine) != 0 {
		t.Fatal("hidden shown")
	}
}
