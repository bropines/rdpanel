package auth

import (
	"crypto/rand"
	"strings"

	"github.com/bropines/rdpanel/internal/crypto"
)

// Personal API tokens: "rdp_" followed by apiTokenChars base62 characters
// (~238 bits of entropy).
const (
	APITokenPrefix = "rdp_"
	apiTokenChars  = 40
	// APITokenLen is the length of a well-formed API token.
	APITokenLen = len(APITokenPrefix) + apiTokenChars
	// APITokenDisplayLen is how many leading characters are kept for display.
	APITokenDisplayLen = len(APITokenPrefix) + 6
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// NewAPIToken generates a personal API token and its sha256 digest.
func NewAPIToken() (OpaqueToken, error) {
	var sb strings.Builder
	sb.Grow(APITokenLen)
	sb.WriteString(APITokenPrefix)
	buf := make([]byte, 64)
	for sb.Len() < APITokenLen {
		if _, err := rand.Read(buf); err != nil {
			return OpaqueToken{}, err
		}
		for _, b := range buf {
			// 248 = 4*62: rejection sampling keeps the distribution uniform.
			if b < 248 && sb.Len() < APITokenLen {
				sb.WriteByte(base62[b%62])
			}
		}
	}
	plain := sb.String()
	return OpaqueToken{Plain: plain, Hash: crypto.HashToken(plain)}, nil
}

// IsAPIToken reports whether s is a well-formed personal API token.
func IsAPIToken(s string) bool {
	if len(s) != APITokenLen || !strings.HasPrefix(s, APITokenPrefix) {
		return false
	}
	for i := len(APITokenPrefix); i < len(s); i++ {
		if strings.IndexByte(base62, s[i]) < 0 {
			return false
		}
	}
	return true
}
