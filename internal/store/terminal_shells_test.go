package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
)

func TestTerminalShells(t *testing.T) {
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	s, ctx := New(dbtest.Open(t), WithSecrets(aead)), context.Background()
	u, other := &User{Username: "alice"}, &User{Username: "bob"}
	for _, x := range []*User{u, other} {
		if err := s.Users.Create(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	r := s.TerminalShells

	if err := r.Save(ctx, &TerminalShell{UserID: u.ID, RustdeskID: "123", ServiceID: "svc-a", Hostname: "pc", Tabs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, &TerminalShell{UserID: other.ID, RustdeskID: "123", ServiceID: "svc-b"}); err != nil {
		t.Fatal(err)
	}
	// update by service id
	if err := r.Save(ctx, &TerminalShell{UserID: u.ID, RustdeskID: "123", ServiceID: "svc-a", Hostname: "pc", Tabs: 3}); err != nil {
		t.Fatal(err)
	}
	list, err := r.List(ctx, u.ID, "123")
	if err != nil || len(list) != 1 || list[0].ServiceID != "svc-a" || list[0].Tabs != 3 {
		t.Fatalf("list %+v %v", list, err)
	}
	// another user's sessions stay private
	if l, _ := r.List(ctx, other.ID, "123"); len(l) != 1 || l[0].ServiceID != "svc-b" {
		t.Fatalf("other %+v", l)
	}
	// cap
	for i := 0; i < MaxTerminalShells+3; i++ {
		if err := r.Save(ctx, &TerminalShell{UserID: u.ID, RustdeskID: "123", ServiceID: fmt.Sprintf("svc-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := r.List(ctx, u.ID, "123"); len(l) != MaxTerminalShells {
		t.Fatalf("cap: %d", len(l))
	}
	if err := r.Delete(ctx, u.ID, "123", fmt.Sprintf("svc-%d", MaxTerminalShells+2)); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.List(ctx, u.ID, "123"); len(l) != MaxTerminalShells-1 {
		t.Fatalf("after delete: %d", len(l))
	}
}
