package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtKey = []byte("rustdesk-test-key")

// Computed independently (Python hmac/base64) for key "rustdesk-test-key",
// claims {"user_id":7,"exp":2000000000}; this is byte-for-byte what legacy
// rustdesk-api issues and what hbbs' jsonwebtoken accepts.
const knownToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyX2lkIjo3LCJleHAiOjIwMDAwMDAwMDB9.j8pTn418AaVgohfj6h7GWPkyqlmApzztT1hHXgQc_9I"

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestMintKnownAnswer(t *testing.T) {
	j, err := NewClientJWT(jwtKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := j.Mint(7, time.Unix(2000000000, 999_000_000))
	if err != nil {
		t.Fatal(err)
	}
	if tok != knownToken {
		t.Fatalf("token mismatch\n got %s\nwant %s", tok, knownToken)
	}
	parts := strings.Split(tok, ".")
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if string(hdr) != `{"alg":"HS256","typ":"JWT"}` || string(payload) != `{"user_id":7,"exp":2000000000}` {
		t.Fatalf("header %s payload %s", hdr, payload)
	}
}

func TestVerify(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	j, _ := NewClientJWT(jwtKey, fixedNow(now))
	c, err := j.Verify(knownToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != 7 || c.ExpiresAt.Unix() != 2000000000 {
		t.Fatalf("claims %+v", c)
	}

	// Expired.
	late, _ := NewClientJWT(jwtKey, fixedNow(time.Unix(2000000001, 0)))
	if _, err := late.Verify(knownToken); err != ErrInvalidToken {
		t.Fatalf("expired: %v", err)
	}
	// Wrong key.
	other, _ := NewClientJWT([]byte("other"), fixedNow(now))
	if _, err := other.Verify(knownToken); err != ErrInvalidToken {
		t.Fatalf("wrong key: %v", err)
	}
	// Garbage.
	if _, err := j.Verify("not.a.jwt"); err != ErrInvalidToken {
		t.Fatal("garbage accepted")
	}
}

func TestVerifyRejectsAlgConfusionAndMissingClaims(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	j, _ := NewClientJWT(jwtKey, fixedNow(now))

	sign := func(method jwt.SigningMethod, claims jwt.Claims, key any) string {
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	exp := float64(2000000000)
	cases := map[string]string{
		"none alg":      sign(jwt.SigningMethodNone, jwt.MapClaims{"user_id": 7, "exp": exp}, jwt.UnsafeAllowNoneSignatureType),
		"HS512":         sign(jwt.SigningMethodHS512, jwt.MapClaims{"user_id": 7, "exp": exp}, jwtKey),
		"no exp":        sign(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": 7}, jwtKey),
		"no user_id":    sign(jwt.SigningMethodHS256, jwt.MapClaims{"exp": exp}, jwtKey),
		"user_id > u32": sign(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": 1 << 33, "exp": exp}, jwtKey),
		"negative uid":  sign(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": -1, "exp": exp}, jwtKey),
		"string uid":    sign(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": "7", "exp": exp}, jwtKey),
	}
	for name, tok := range cases {
		if _, err := j.Verify(tok); err != ErrInvalidToken {
			t.Errorf("%s: accepted (err=%v)", name, err)
		}
	}
	// Extra claims are tolerated (as by hbbs).
	extra := sign(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": 7, "exp": exp, "jti": "abc"}, jwtKey)
	if _, err := j.Verify(extra); err != nil {
		t.Errorf("extra claims rejected: %v", err)
	}
}

func TestMintRejectsZeroUser(t *testing.T) {
	j, _ := NewClientJWT(jwtKey, nil)
	if _, err := j.Mint(0, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("user 0 accepted")
	}
	if _, err := NewClientJWT(nil, nil); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestParseUnverified(t *testing.T) {
	c, err := ParseUnverified(knownToken)
	if err != nil || c.UserID != 7 || c.ExpiresAt.Unix() != 2000000000 {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ParseUnverified("0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("md5 token parsed as jwt")
	}
}
