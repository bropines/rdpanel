package auth

import "testing"

func TestAPITokenFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		tok, err := NewAPIToken()
		if err != nil {
			t.Fatal(err)
		}
		if !IsAPIToken(tok.Plain) || len(tok.Hash) != 32 || seen[tok.Plain] {
			t.Fatalf("bad token %q", tok.Plain)
		}
		seen[tok.Plain] = true
	}
	for _, s := range []string{"", "rdp_", "rdp_short", "xyz_0123456789012345678901234567890123456789",
		"rdp_012345678901234567890123456789012345678-", "rdp_0123456789012345678901234567890123456789x"} {
		if IsAPIToken(s) {
			t.Fatalf("accepted %q", s)
		}
	}
}
