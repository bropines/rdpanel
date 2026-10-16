package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	return New(d).WithClock(clk.now), clk
}

func TestUsersCRUD(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	g := &UserGroup{Name: "Share group", Visibility: VisibilityShared}
	if err := s.UserGroups.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	u := &User{Username: "Alice", Email: "alice@example.test", PasswordHash: "$argon2id$x", PasswordAlgo: "argon2id",
		Role: RoleAdmin, UserGroupID: &g.ID}
	if err := s.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.CreatedAt.IsZero() {
		t.Fatalf("id/created not set: %+v", u)
	}
	got, err := s.Users.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || got.Email != u.Email || !got.IsAdmin() || !got.IsActive() || *got.UserGroupID != g.ID {
		t.Fatalf("got %+v", got)
	}
	if !got.CreatedAt.Equal(u.CreatedAt) || got.CreatedAt.Location() != time.UTC {
		t.Fatalf("timestamps %v vs %v", got.CreatedAt, u.CreatedAt)
	}

	// Case-insensitive uniqueness.
	if err := s.Users.Create(ctx, &User{Username: "ALICE"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup: %v", err)
	}
	// Preset id (legacy import).
	legacy := &User{ID: 42, Username: "legacy"}
	if err := s.Users.Create(ctx, legacy); err != nil || legacy.ID != 42 {
		t.Fatalf("legacy id: %v %d", err, legacy.ID)
	}
	// Unknown group -> FK error.
	bad := int64(999)
	if err := s.Users.Create(ctx, &User{Username: "x", UserGroupID: &bad}); !errors.Is(err, ErrReferenced) {
		t.Fatalf("fk: %v", err)
	}
	// Invalid enum -> check error.
	if err := s.Users.Create(ctx, &User{Username: "y", Role: "root"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("check: %v", err)
	}

	got.DisplayName, got.Status, got.Email = "Alice A.", UserDisabled, ""
	if err := s.Users.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Users.GetByID(ctx, u.ID)
	if again.DisplayName != "Alice A." || again.IsActive() || again.Email != "" {
		t.Fatalf("update: %+v", again)
	}

	if err := s.Users.SetPassword(ctx, u.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	again, _ = s.Users.GetByID(ctx, u.ID)
	if again.HasPassword() || again.PasswordChangedAt != nil {
		t.Fatal("password not cleared")
	}
	if err := s.Users.SetPassword(ctx, u.ID, "$2a$10$x", "bcrypt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Users.RehashPassword(ctx, u.ID, "$argon2id$new", "argon2id"); err != nil {
		t.Fatal(err)
	}
	again, _ = s.Users.GetByID(ctx, u.ID)
	if again.PasswordAlgo != "argon2id" || again.PasswordChangedAt == nil {
		t.Fatalf("rehash: %+v", again)
	}

	users, total, err := s.Users.List(ctx, ListUsersParams{Query: "LEG"})
	if err != nil || total != 1 || len(users) != 1 || users[0].ID != 42 {
		t.Fatalf("list: %d %v", total, err)
	}
	if _, total, _ := s.Users.List(ctx, ListUsersParams{Query: "%"}); total != 0 {
		t.Fatalf("LIKE wildcard must be escaped, got %d", total)
	}

	if n, _ := s.Users.CountActiveAdmins(ctx); n != 0 {
		t.Fatalf("admins = %d", n)
	}
	if err := s.Users.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Users.GetByID(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Users.Delete(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestUserGroups(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	g := &UserGroup{Name: "Default"}
	if err := s.UserGroups.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := s.UserGroups.Create(ctx, &UserGroup{Name: "default"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup name: %v", err)
	}
	u := &User{Username: "m", UserGroupID: &g.ID}
	_ = s.Users.Create(ctx, u)
	if n, _ := s.UserGroups.MemberCount(ctx, g.ID); n != 1 {
		t.Fatalf("members = %d", n)
	}
	g.Visibility = VisibilityShared
	if err := s.UserGroups.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	byName, err := s.UserGroups.GetByName(ctx, "DEFAULT")
	if err != nil || byName.Visibility != VisibilityShared {
		t.Fatalf("%+v %v", byName, err)
	}
	if err := s.UserGroups.Delete(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	u2, _ := s.Users.GetByID(ctx, u.ID)
	if u2.UserGroupID != nil {
		t.Fatal("group delete must null user_group_id")
	}
	list, _ := s.UserGroups.List(ctx)
	if len(list) != 0 {
		t.Fatal("list not empty")
	}
}

func TestAuthTokens(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	u := &User{Username: "tok"}
	_ = s.Users.Create(ctx, u)

	hash := crypto.HashToken("plain-token")
	jwtExp := clk.now().Add(48 * time.Hour)
	tok := &AuthToken{UserID: u.ID, Kind: TokenClient, TokenSHA256: hash, JWTExpiresAt: &jwtExp,
		ExpiresAt: clk.now().Add(time.Hour), DeviceUUID: "uuid-1", DeviceRustdeskID: "123456789"}
	if err := s.AuthTokens.Create(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthTokens.Create(ctx, &AuthToken{UserID: u.ID, Kind: TokenClient, TokenSHA256: hash,
		ExpiresAt: clk.now()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup hash: %v", err)
	}

	got, err := s.AuthTokens.GetActive(ctx, TokenClient, hash)
	if err != nil || got.ID != tok.ID || got.DeviceUUID != "uuid-1" || !got.JWTExpiresAt.Equal(jwtExp) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.AuthTokens.GetActive(ctx, TokenWeb, hash); !errors.Is(err, ErrNotFound) {
		t.Fatal("kind must be checked")
	}

	// Sliding expiry.
	clk.advance(50 * time.Minute)
	ext := clk.now().Add(time.Hour)
	if err := s.AuthTokens.Touch(ctx, tok.ID, &ext); err != nil {
		t.Fatal(err)
	}
	clk.advance(30 * time.Minute) // past the original expiry
	if _, err := s.AuthTokens.GetActive(ctx, TokenClient, hash); err != nil {
		t.Fatalf("extended token inactive: %v", err)
	}
	clk.advance(time.Hour)
	if _, err := s.AuthTokens.GetActive(ctx, TokenClient, hash); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired token active")
	}

	// Web session: idle slides but never past absolute.
	abs := clk.now().Add(2 * time.Hour)
	web := &AuthToken{UserID: u.ID, Kind: TokenWeb, TokenSHA256: crypto.HashToken("web"),
		ExpiresAt: clk.now().Add(time.Hour), AbsoluteExpiresAt: &abs}
	_ = s.AuthTokens.Create(ctx, web)
	far := clk.now().Add(10 * time.Hour)
	_ = s.AuthTokens.Touch(ctx, web.ID, &far)
	w, _ := s.AuthTokens.Get(ctx, web.ID)
	if !w.ExpiresAt.Equal(abs) {
		t.Fatalf("idle expiry %v must be capped at %v", w.ExpiresAt, abs)
	}

	// Revocation.
	c2 := &AuthToken{UserID: u.ID, Kind: TokenClient, TokenSHA256: crypto.HashToken("c2"),
		ExpiresAt: clk.now().Add(time.Hour), DeviceUUID: "uuid-2"}
	_ = s.AuthTokens.Create(ctx, c2)
	active, _ := s.AuthTokens.ListActiveForUser(ctx, u.ID, "")
	if len(active) != 2 {
		t.Fatalf("active = %d", len(active))
	}
	if n, _ := s.AuthTokens.RevokeForDevice(ctx, "uuid-2", "device deleted"); n != 1 {
		t.Fatalf("revoke device = %d", n)
	}
	if n, _ := s.AuthTokens.RevokeForUser(ctx, u.ID, TokenWeb, "password reset"); n != 1 {
		t.Fatalf("revoke user web = %d", n)
	}
	if _, err := s.AuthTokens.GetActive(ctx, TokenWeb, crypto.HashToken("web")); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked session active")
	}
	if err := s.AuthTokens.Revoke(ctx, 99999, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoke unknown")
	}

	clk.advance(time.Hour)
	n, err := s.AuthTokens.PurgeInactive(ctx, clk.now())
	if err != nil || n != 3 {
		t.Fatalf("purged %d %v", n, err)
	}
}

func TestSettings(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Settings.GetRaw(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	v, err := GetOr(ctx, s.Settings, SettingSessionIdleHours, 90)
	if err != nil || v != 90 {
		t.Fatalf("default: %d %v", v, err)
	}
	if err := s.Settings.Set(ctx, SettingSessionIdleHours, 30, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings.Set(ctx, SettingBrandingTitle, "My Desk", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings.Set(ctx, SettingSessionIdleHours, 14, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = GetOr(ctx, s.Settings, SettingSessionIdleHours, 90)
	if v != 14 {
		t.Fatalf("upsert: %d", v)
	}
	all, _ := s.Settings.All(ctx)
	if len(all) != 2 || string(all[SettingBrandingTitle]) != `"My Desk"` {
		t.Fatalf("all: %v", all)
	}
	_ = s.Settings.Delete(ctx, SettingBrandingTitle)
	if _, err := s.Settings.GetRaw(ctx, SettingBrandingTitle); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete failed")
	}
}

func TestAdminAuditAndTx(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	admin := &User{Username: "root", Role: RoleAdmin}
	_ = s.Users.Create(ctx, admin)

	// Tx commits both writes or neither.
	err := s.Tx(ctx, func(tx *Store) error {
		u := &User{Username: "newbie"}
		if err := tx.Users.Create(ctx, u); err != nil {
			return err
		}
		return tx.AdminAudit.Record(ctx, &AuditEntry{ActorUserID: &admin.ID, ActorName: admin.Username,
			Action: "user.create", TargetType: "user", TargetID: "2", Details: map[string]any{"role": "user"}})
	})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = s.Tx(ctx, func(tx *Store) error {
		_ = tx.Users.Create(ctx, &User{Username: "ghost"})
		_ = tx.AdminAudit.Record(ctx, &AuditEntry{ActorName: "cli", Action: "user.create"})
		// Nested Tx reuses the transaction.
		return tx.Tx(ctx, func(inner *Store) error { return boom })
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Users.GetByUsername(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatal("rolled back user exists")
	}

	clk.advance(time.Minute)
	_ = s.AdminAudit.Record(ctx, &AuditEntry{ActorName: "cli", Action: "settings.update"})
	all, err := s.AdminAudit.List(ctx, AuditFilter{})
	if err != nil || len(all) != 2 || all[0].Action != "settings.update" {
		t.Fatalf("list: %v %v", all, err)
	}
	users, _ := s.AdminAudit.List(ctx, AuditFilter{ActionPrefix: "user."})
	if len(users) != 1 || users[0].Details["role"] != "user" || *users[0].ActorUserID != admin.ID {
		t.Fatalf("filter: %+v", users)
	}
	page2, _ := s.AdminAudit.List(ctx, AuditFilter{BeforeID: all[0].ID})
	if len(page2) != 1 || page2[0].ID != all[1].ID {
		t.Fatal("keyset paging")
	}
	// Deleting the actor keeps the entry with a NULL actor.
	_ = s.Users.Delete(ctx, admin.ID)
	users, _ = s.AdminAudit.List(ctx, AuditFilter{ActionPrefix: "user."})
	if users[0].ActorUserID != nil || users[0].ActorName != "root" {
		t.Fatalf("actor snapshot: %+v", users[0])
	}
	if n, _ := s.AdminAudit.PurgeBefore(ctx, clk.now()); n != 1 {
		t.Fatalf("purge = %d", n)
	}
}
