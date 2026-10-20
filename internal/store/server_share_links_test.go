package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestServerShareLinks(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	g := &UserGroup{Name: "Helpdesk"}
	if err := s.UserGroups.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	admin := &User{Username: "root", Role: RoleAdmin}
	user := &User{Username: "bob", Role: RoleUser, UserGroupID: &g.ID}
	for _, u := range []*User{admin, user} {
		if err := s.Users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	// The group right: off by default, then granted.
	if ok, err := s.ServerShareLinks.CanShare(ctx, user.ID); err != nil || ok {
		t.Fatalf("user without right: %v %v", ok, err)
	}
	if ok, _ := s.ServerShareLinks.CanShare(ctx, admin.ID); !ok {
		t.Fatal("admin may share")
	}
	if ok, err := s.ServerShareLinks.CanShare(ctx, 9999); err != nil || ok {
		t.Fatalf("unknown user: %v %v", ok, err)
	}
	g.CanShareLinks = true
	if err := s.UserGroups.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserGroups.Get(ctx, g.ID); !got.CanShareLinks {
		t.Fatal("can_share_links not stored")
	}
	if row, _ := s.UserGroups.GetRow(ctx, g.ID); !row.CanShareLinks {
		t.Fatal("can_share_links missing from GetRow")
	}
	if ok, _ := s.ServerShareLinks.CanShare(ctx, user.ID); !ok {
		t.Fatal("user with right")
	}

	hash := bytes.Repeat([]byte{7}, 32)
	exp := clk.now().Add(time.Hour)
	maxDL := 2
	l := &ServerShareLink{UserID: user.ID, Label: "Front desk", TokenSHA256: hash, ExpiresAt: &exp, MaxDownloads: &maxDL}
	if err := s.ServerShareLinks.Create(ctx, l); err != nil || l.ID == 0 {
		t.Fatalf("create: %v", err)
	}
	if err := s.ServerShareLinks.Create(ctx, &ServerShareLink{UserID: admin.ID, TokenSHA256: hash}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate hash: %v", err)
	}
	if err := s.ServerShareLinks.SetTokenEnc(ctx, l.ID, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	perm := &ServerShareLink{UserID: admin.ID, TokenSHA256: bytes.Repeat([]byte{8}, 32)}
	if err := s.ServerShareLinks.Create(ctx, perm); err != nil {
		t.Fatal(err)
	}

	got, err := s.ServerShareLinks.GetByHash(ctx, hash)
	if err != nil || got.ID != l.ID || got.Username != "bob" || !got.OwnerAllowed || string(got.TokenEnc) != "sealed" ||
		got.MaxDownloads == nil || *got.MaxDownloads != 2 || got.Status(clk.now()) != ShareLinkActive {
		t.Fatalf("get by hash: %+v %v", got, err)
	}
	if _, err := s.ServerShareLinks.GetByHash(ctx, bytes.Repeat([]byte{9}, 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}

	if err := s.ServerShareLinks.RecordView(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, true, false} {
		ok, err := s.ServerShareLinks.TakeDownload(ctx, l.ID)
		if err != nil || ok != want {
			t.Fatalf("download %d: %v %v", i, ok, err)
		}
	}
	got, _ = s.ServerShareLinks.Get(ctx, l.ID)
	if got.Views != 1 || got.Downloads != 2 || got.LastUsedAt == nil || got.Status(clk.now()) != ShareLinkExhausted {
		t.Fatalf("counters: %+v %s", got, got.Status(clk.now()))
	}

	// Lists: per user and all, newest first.
	if list, _ := s.ServerShareLinks.List(ctx, &user.ID, 0); len(list) != 1 || list[0].ID != l.ID {
		t.Fatalf("user list: %v", list)
	}
	if list, _ := s.ServerShareLinks.List(ctx, nil, 0); len(list) != 2 || list[0].ID != perm.ID {
		t.Fatalf("all: %v", list)
	}
	if n, _ := s.ServerShareLinks.CountOpen(ctx, user.ID); n != 1 {
		t.Fatalf("open: %d", n)
	}

	// Expiry: refused downloads, not counted as open.
	clk.advance(2 * time.Hour)
	got, _ = s.ServerShareLinks.Get(ctx, l.ID)
	if got.Status(clk.now()) != ShareLinkExpired {
		t.Fatalf("status %s", got.Status(clk.now()))
	}
	if n, _ := s.ServerShareLinks.CountOpen(ctx, user.ID); n != 0 {
		t.Fatalf("open after expiry: %d", n)
	}

	// The owner loses the right: links are suspended.
	g.CanShareLinks = false
	if err := s.UserGroups.Update(ctx, g); err != nil {
		t.Fatal(err)
	}
	user2 := &User{Username: "carol", Role: RoleUser, UserGroupID: &g.ID}
	if err := s.Users.Create(ctx, user2); err != nil {
		t.Fatal(err)
	}
	l2 := &ServerShareLink{UserID: user2.ID, TokenSHA256: bytes.Repeat([]byte{3}, 32)}
	if err := s.ServerShareLinks.Create(ctx, l2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ServerShareLinks.Get(ctx, l2.ID); got.OwnerAllowed || got.Status(clk.now()) != ShareLinkSuspended {
		t.Fatalf("suspended: %+v", got)
	}

	// Revoke: idempotent, hidden from lists, downloads refused.
	if err := s.ServerShareLinks.Revoke(ctx, perm.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ServerShareLinks.Revoke(ctx, perm.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ServerShareLinks.Revoke(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
	if ok, _ := s.ServerShareLinks.TakeDownload(ctx, perm.ID); ok {
		t.Fatal("download of a revoked link")
	}
	if got, _ := s.ServerShareLinks.Get(ctx, perm.ID); got.Status(clk.now()) != ShareLinkRevoked {
		t.Fatalf("revoked: %s", got.Status(clk.now()))
	}
	if list, _ := s.ServerShareLinks.List(ctx, &admin.ID, 0); len(list) != 0 {
		t.Fatalf("revoked listed: %v", list)
	}

	// Deleting the owner removes their links.
	if err := s.Users.Delete(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ServerShareLinks.Get(ctx, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after owner delete: %v", err)
	}
}
