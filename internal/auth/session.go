package auth

import (
	"strings"

	"github.com/bropines/rdpanel/internal/crypto"
)

// sessionTokenBytes is the entropy of opaque tokens (256 bits).
const sessionTokenBytes = 32

// OpaqueToken is a freshly generated bearer secret and its storage digest.
type OpaqueToken struct {
	// Plain is handed to the client exactly once (cookie, URL, ...).
	Plain string
	// Hash is sha256(Plain), the only form persisted.
	Hash []byte
}

// NewOpaqueToken generates a random 256-bit token (unpadded base64url) for
// panel sessions, CSRF secrets, share links and OAuth states.
func NewOpaqueToken() (OpaqueToken, error) {
	plain, err := crypto.RandomToken(sessionTokenBytes)
	if err != nil {
		return OpaqueToken{}, err
	}
	return OpaqueToken{Plain: plain, Hash: crypto.HashToken(plain)}, nil
}

// BearerToken extracts the token from an Authorization header value. The
// scheme must be "Bearer" (case-insensitive) followed by a single space and a
// non-empty token; anything else yields "", false.
func BearerToken(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(scheme):])
	if tok == "" || strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok, true
}
