package auth

import (
	"encoding/base32"
	"strings"

	"github.com/bropines/rdpanel/internal/crypto"
)

// RecoveryCodeCount is the number of codes issued per enrolment.
const RecoveryCodeCount = 10

// recoveryEncoding uses lowercase-friendly base32 without padding.
var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// RecoveryCode is a one-time 2FA bypass code and its storage digest.
type RecoveryCode struct {
	// Plain is shown to the user once, formatted "xxxx-xxxx-xxxx-xxxx".
	Plain string
	// Hash is sha256 of the normalised code (80 bits of entropy, so a fast
	// hash is sufficient).
	Hash []byte
}

// GenerateRecoveryCodes returns n fresh recovery codes.
func GenerateRecoveryCodes(n int) ([]RecoveryCode, error) {
	out := make([]RecoveryCode, 0, n)
	for i := 0; i < n; i++ {
		raw, err := crypto.RandomBytes(10) // 80 bits -> 16 base32 chars
		if err != nil {
			return nil, err
		}
		s := strings.ToLower(recoveryEncoding.EncodeToString(raw))
		plain := s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16]
		out = append(out, RecoveryCode{Plain: plain, Hash: HashRecoveryCode(plain)})
	}
	return out, nil
}

// NormalizeRecoveryCode lowercases and strips separators/whitespace so users
// may type codes with or without dashes.
func NormalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(code) {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// HashRecoveryCode returns the digest stored in recovery_codes.code_sha256.
func HashRecoveryCode(code string) []byte {
	return crypto.HashToken("rdpanel-recovery:" + NormalizeRecoveryCode(code))
}
