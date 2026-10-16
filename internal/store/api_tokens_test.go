package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	s, clk := newTestStore(t)
	ctx := context.Background()
	u := &User{Username: "ci", Role: RoleAdmin}
	if err := s.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	hash := bytes.Repeat([]byte{1}, 32)
	exp := clk.now().Add(time.Hour)
	tok := &APIToken{UserID: u.ID, Name: "ci", Prefix: "rdp_abcdef", TokenSHA256: hash, Scope: APITokenRead, ExpiresAt: &exp}
	if err := s.APITokens.Create(ctx, tok); err != nil || tok.ID == 0 {
		t.Fatalf("create: %v", err)
	}
	// Same hash twice: unique.
	if err := s.APITokens.Create(ctx, &APIToken{UserID: u.ID, Name: "x", Prefix: "p", TokenSHA256: hash, Scope: APITokenWrite}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup: %v", err)
	}
	got, err := s.APITokens.GetActive(ctx, hash)
	if err != nil || got.ID != tok.ID || got.Scope != APITokenRead || got.ExpiresAt == nil {
		t.Fatalf("get active: %+v %v", got, err)
	}
	if err := s.APITokens.Touch(ctx, tok.ID, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.APITokens.Get(ctx, tok.ID)
	if got.LastUsedIP != "10.0.0.1" || got.LastUsedAt == nil {
		t.Fatalf("touch: %+v", got)
	}

	// Expired: not active, still listed (flagged by the caller).
	clk.advance(2 * time.Hour)
	if _, err := s.APITokens.GetActive(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired: %v", err)
	}
	if !got.Expired(clk.now()) {
		t.Fatal("Expired() false")
	}
	list, err := s.APITokens.ListForUser(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}

	// Revoked: neither active nor listed nor counted.
	if err := s.APITokens.Revoke(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.APITokens.CountForUser(ctx, u.ID); n != 0 {
		t.Fatalf("count after revoke: %d", n)
	}
	if list, _ := s.APITokens.ListForUser(ctx, u.ID); len(list) != 0 {
		t.Fatalf("list after revoke: %v", list)
	}
	if err := s.APITokens.Revoke(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}

	// Deleting the user removes the tokens.
	if err := s.Users.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.APITokens.Get(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after user delete: %v", err)
	}
}
