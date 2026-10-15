package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ClientClaims are the claims of a RustDesk client token. The JSON shape is
// exactly {"user_id": <u32>, "exp": <unix seconds>}, matching legacy
// rustdesk-api and the Rust `Claims { user_id: u32, exp: usize }` that hbbs
// verifies with `jsonwebtoken` (HS256, exp required) when MUST_LOGIN is on.
type ClientClaims struct {
	UserID uint32 `json:"user_id"`
	jwt.RegisteredClaims
}

// ErrInvalidToken is returned for any malformed, mis-signed or expired token.
var ErrInvalidToken = errors.New("auth: invalid token")

// ClientJWT mints and verifies client JWTs with the shared HS256 key
// (RUSTDESK_API_JWT_KEY, raw bytes).
type ClientJWT struct {
	key []byte
	now func() time.Time
}

// NewClientJWT returns a ClientJWT for key. now may be nil (time.Now).
func NewClientJWT(key []byte, now func() time.Time) (*ClientJWT, error) {
	if len(key) == 0 {
		return nil, errors.New("auth: empty JWT key")
	}
	if now == nil {
		now = time.Now
	}
	return &ClientJWT{key: append([]byte(nil), key...), now: now}, nil
}

// Mint returns a token for userID expiring at exp (truncated to seconds).
// The header is the jsonwebtoken/golang-jwt default {"alg":"HS256","typ":"JWT"}.
func (c *ClientJWT) Mint(userID uint32, exp time.Time) (string, error) {
	if userID == 0 {
		return "", errors.New("auth: user id must be > 0")
	}
	claims := ClientClaims{
		UserID:           userID,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(exp.Truncate(time.Second))},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.key)
	if err != nil {
		return "", fmt.Errorf("auth: sign jwt: %w", err)
	}
	return s, nil
}

// Verify checks signature (HS256 only), presence and validity of exp, and a
// non-zero user_id. It does not consult the database; callers must also check
// the auth_tokens row (revocation, sliding expiry).
func (c *ClientJWT) Verify(token string) (ClientClaims, error) {
	var claims ClientClaims
	parsed, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return c.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(c.now),
	)
	if err != nil || !parsed.Valid || claims.UserID == 0 {
		return ClientClaims{}, ErrInvalidToken
	}
	return claims, nil
}

// ParseUnverified extracts claims without checking the signature or expiry.
// Only for the legacy importer (classifying old tokens); never for auth.
func ParseUnverified(token string) (ClientClaims, error) {
	var claims ClientClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil {
		return ClientClaims{}, ErrInvalidToken
	}
	return claims, nil
}
