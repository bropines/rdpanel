package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// RandomBytes returns n cryptographically random bytes.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto: random: %w", err)
	}
	return b, nil
}

// RandomToken returns n random bytes encoded as unpadded base64url
// (URL-, header- and cookie-safe).
func RandomToken(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns sha256(token). Tokens (client JWTs, session tokens, share
// links, OAuth states) are stored only as this digest, never in plain text.
// A fast hash is adequate because every token has >=128 bits of entropy.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Equal compares two byte slices in constant time (for equal lengths).
func Equal(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// EqualString compares two strings in constant time (for equal lengths).
func EqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
