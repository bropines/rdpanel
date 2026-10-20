package websessions

import (
	"strings"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(func() time.Time { return now })
	a, err := r.Issue(7)
	if err != nil || !strings.HasPrefix(a, Prefix) || len(a) != len(Prefix)+10 {
		t.Fatalf("issue: %q %v", a, err)
	}
	b, _ := r.Issue(8)
	if a == b {
		t.Fatal("ids must differ")
	}
	if u, ok := r.User(a); !ok || u != 7 {
		t.Fatalf("user(a) = %d %v", u, ok)
	}
	if _, ok := r.User("web-0000000000x"); ok {
		t.Fatal("unknown id resolved")
	}
	now = now.Add(TTL + time.Second)
	if _, ok := r.User(a); ok {
		t.Fatal("expired id resolved")
	}
}
