package auth

import (
	"strings"
	"testing"
)

// fastHasher keeps argon2id cheap in tests.
func fastHasher() *Hasher {
	return &Hasher{Params: Argon2Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}
}

func TestArgon2idRoundTrip(t *testing.T) {
	h := fastHasher()
	long := strings.Repeat("Ω", 100) // >32 chars and >72 bytes: no legacy caps
	for _, pw := range []string{"x", "correct horse battery staple", long} {
		hash, algo, err := h.Hash(pw)
		if err != nil {
			t.Fatal(err)
		}
		if algo != AlgoArgon2id || DetectAlgo(hash) != AlgoArgon2id || !strings.HasPrefix(hash, "$argon2id$v=19$m=1024,t=1,p=1$") {
			t.Fatalf("hash %q algo %q", hash, algo)
		}
		if !h.Verify(hash, pw) {
			t.Fatalf("verify failed for %q", pw)
		}
		if h.Verify(hash, pw+"x") {
			t.Fatal("wrong password accepted")
		}
		if h.NeedsRehash(hash) {
			t.Fatal("fresh hash should not need rehash")
		}
	}
	h2, _, _ := h.Hash("same")
	h3, _, _ := h.Hash("same")
	if h2 == h3 {
		t.Fatal("salt must be random")
	}
}

func TestHashRejectsEmptyAndHuge(t *testing.T) {
	h := fastHasher()
	if _, _, err := h.Hash(""); err != ErrEmptyPassword {
		t.Fatalf("empty: %v", err)
	}
	if _, _, err := h.Hash(strings.Repeat("a", MaxPasswordBytes+1)); err != ErrPasswordTooLong {
		t.Fatalf("huge: %v", err)
	}
}

func TestBcryptKnownAnswers(t *testing.T) {
	h := fastHasher()
	cases := []struct{ hash, pw string }{
		// OpenBSD/openwall test vector.
		{"$2a$05$CCCCCCCCCCCCCCCCCCCCC.E5YPO9kmyuRGyh0XouQYb4YMJKvyOeW", "U*U"},
		// Generated independently with pyca/bcrypt (cost 10, $2b$).
		{"$2b$10$kOKuk4t30A8NYvrccq5SPOzKDJ690do00Y1BfuReLfUCjrUQttLtS", "correct horse"},
		// Same hash with the PHP $2y$ prefix.
		{"$2y$10$kOKuk4t30A8NYvrccq5SPOzKDJ690do00Y1BfuReLfUCjrUQttLtS", "correct horse"},
	}
	for _, c := range cases {
		if DetectAlgo(c.hash) != AlgoBcrypt {
			t.Fatalf("detect %q", c.hash)
		}
		if !h.Verify(c.hash, c.pw) {
			t.Fatalf("bcrypt KAT failed: %q", c.hash)
		}
		if h.Verify(c.hash, c.pw+"!") {
			t.Fatal("bcrypt accepted wrong password")
		}
		if !h.NeedsRehash(c.hash) {
			t.Fatal("bcrypt must be rehashed to argon2id")
		}
	}
}

func TestLegacyMD5KnownAnswers(t *testing.T) {
	h := fastHasher()
	cases := []struct{ hash, pw string }{
		// md5_hex("admin123" + "rustdesk-api"), computed with coreutils md5sum.
		{"968135d06510dba9465201331e45b8e5", "admin123"},
		// Non-ASCII, spaces; uppercase stored hex is accepted too.
		{"AD305AAAE4068BB204BED81269231A34", "p@ss wörd-ПАРОЛЬ"},
	}
	for _, c := range cases {
		if DetectAlgo(c.hash) != AlgoLegacyMD5 {
			t.Fatalf("detect %q", c.hash)
		}
		if !h.Verify(c.hash, c.pw) {
			t.Fatalf("md5 KAT failed for %q", c.pw)
		}
		if h.Verify(c.hash, strings.ToUpper(c.pw)) {
			t.Fatal("md5 accepted wrong password")
		}
		if !h.NeedsRehash(c.hash) {
			t.Fatal("legacy md5 must be rehashed")
		}
	}
	// Without the salt the plain md5 must not match.
	if h.Verify("0192023a7bbd73250516f069df18b500", "admin123") {
		t.Fatal("unsalted md5 must not match")
	}
}

func TestEmptyAndGarbageNeverMatch(t *testing.T) {
	h := fastHasher()
	for _, hash := range []string{"", " ", "plaintext", "$argon2id$garbage", "$2a$10$short",
		"$argon2id$v=19$m=999999999,t=1,p=1$c2FsdA$a2V5a2V5a2V5a2V5a2V5"} {
		if h.Verify(hash, "") || h.Verify(hash, "plaintext") || h.Verify(hash, "x") {
			t.Fatalf("hash %q matched", hash)
		}
	}
	good, _, _ := h.Hash("pw")
	if h.Verify(good, "") {
		t.Fatal("empty password matched")
	}
	if DetectAlgo("") != AlgoNone || DetectAlgo("nothex-nothex-nothex-nothex-1234") != AlgoNone {
		t.Fatal("detect none")
	}
}

func TestNeedsRehashOnParamChange(t *testing.T) {
	old := fastHasher()
	hash, _, _ := old.Hash("pw")
	newer := fastHasher()
	newer.Params.Time = 2
	if !newer.NeedsRehash(hash) {
		t.Fatal("param change must trigger rehash")
	}
	if !newer.Verify(hash, "pw") {
		t.Fatal("old-param hash must still verify")
	}
}
