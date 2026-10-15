package auth

import (
	"bytes"
	"regexp"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/crypto"
)

func TestOpaqueToken(t *testing.T) {
	a, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewOpaqueToken()
	if a.Plain == b.Plain || len(a.Plain) != 43 {
		t.Fatalf("tokens %q %q", a.Plain, b.Plain)
	}
	if !bytes.Equal(a.Hash, crypto.HashToken(a.Plain)) {
		t.Fatal("hash mismatch")
	}
}

func TestBearerToken(t *testing.T) {
	ok := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER a.b.c": "a.b.c",
		"Bearer  abc ": "abc",
	}
	for in, want := range ok {
		got, valid := BearerToken(in)
		if !valid || got != want {
			t.Errorf("%q -> %q %v", in, got, valid)
		}
	}
	for _, in := range []string{"", "Bearer", "Bearer ", "Basic abc", "Token abc", "Bearerabc", "xxxxxxxabc", "Bearer a b"} {
		if _, valid := BearerToken(in); valid {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestTOTP(t *testing.T) {
	key, err := GenerateTOTP("rdpanel", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^otpauth://totp/rdpanel:alice\?`).MatchString(key.URL) {
		t.Fatalf("url %s", key.URL)
	}
	now := time.Unix(1_700_000_000, 0)
	code, _ := TOTPCode(key.Secret, now)

	step, err := ValidateTOTP(key.Secret, code, now, 0)
	if err != nil || step != now.Unix()/30 {
		t.Fatalf("step %d err %v", step, err)
	}
	// Replay of the same step is rejected.
	if _, err := ValidateTOTP(key.Secret, code, now, step); err != ErrTOTPInvalid {
		t.Fatal("replay accepted")
	}
	// One step of skew is accepted, two are not.
	if _, err := ValidateTOTP(key.Secret, code, now.Add(30*time.Second), 0); err != nil {
		t.Fatalf("skew +1: %v", err)
	}
	if _, err := ValidateTOTP(key.Secret, code, now.Add(90*time.Second), 0); err != ErrTOTPInvalid {
		t.Fatal("skew +3 accepted")
	}
	// Formatting tolerance and garbage.
	if _, err := ValidateTOTP(key.Secret, " "+code[:3]+" "+code[3:]+" ", now, 0); err != nil {
		t.Fatalf("spaced code: %v", err)
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, err := ValidateTOTP(key.Secret, bad, now, 0); err != ErrTOTPInvalid {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 Appendix B, SHA1, T=59 -> 94287082 (8 digits); with 6 digits -> 287082.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // base32("12345678901234567890")
	code, err := TOTPCode(secret, time.Unix(59, 0))
	if err != nil || code != "287082" {
		t.Fatalf("code %q err %v", code, err)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}-[a-z2-7]{4}$`)
	seen := map[string]bool{}
	for _, c := range codes {
		if !re.MatchString(c.Plain) || seen[c.Plain] {
			t.Fatalf("bad/duplicate code %q", c.Plain)
		}
		seen[c.Plain] = true
		if !bytes.Equal(c.Hash, HashRecoveryCode(c.Plain)) {
			t.Fatal("hash mismatch")
		}
		typed := " " + c.Plain[:4] + c.Plain[5:9] + "-" + c.Plain[10:]
		if !bytes.Equal(HashRecoveryCode(typed), c.Hash) {
			t.Fatalf("normalisation failed for %q", typed)
		}
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestBackoff(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	b := NewBackoff(BackoffConfig{Free: 2, Base: time.Second, Max: 8 * time.Second, Forget: time.Minute}, c.now)
	key := "ip:1.2.3.4"

	for i := 0; i < 2; i++ {
		if ok, _ := b.Allow(key); !ok {
			t.Fatalf("free attempt %d blocked", i)
		}
		b.Failure(key)
	}
	if ok, _ := b.Allow(key); !ok {
		t.Fatal("still within free attempts")
	}
	wantDelays := []time.Duration{1, 2, 4, 8, 8}
	for i, want := range wantDelays {
		b.Failure(key)
		ok, wait := b.Allow(key)
		if ok || wait != want*time.Second {
			t.Fatalf("failure %d: ok=%v wait=%v want %v", i+3, ok, wait, want*time.Second)
		}
		c.advance(wait)
		if ok, _ := b.Allow(key); !ok {
			t.Fatalf("should be allowed after waiting %v", wait)
		}
	}
	// Other keys are independent.
	if ok, _ := b.Allow("user:alice"); !ok {
		t.Fatal("independent key blocked")
	}
	// Success resets.
	b.Success(key)
	b.Failure(key)
	if ok, _ := b.Allow(key); !ok {
		t.Fatal("reset failed")
	}
	// History is forgotten after the quiet period.
	for i := 0; i < 5; i++ {
		b.Failure(key)
	}
	c.advance(2 * time.Minute)
	if ok, _ := b.Allow(key); !ok {
		t.Fatal("forget failed")
	}
	b.Failure("k2")
	c.advance(2 * time.Minute)
	b.Prune()
	if b.Len() != 0 {
		t.Fatalf("prune left %d keys", b.Len())
	}
}
