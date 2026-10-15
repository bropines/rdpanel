// Package auth implements credential primitives: password hashing and
// verification (argon2id, bcrypt, legacy rustdesk-api md5), client JWTs,
// opaque session tokens, TOTP, recovery codes and login rate limiting.
package auth

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"

	"github.com/bropines/rustdesk-panel/internal/crypto"
)

// Algo identifies a stored password hash format. Values match users.password_algo.
type Algo string

// Supported password hash formats.
const (
	AlgoNone      Algo = ""
	AlgoArgon2id  Algo = "argon2id"
	AlgoBcrypt    Algo = "bcrypt"
	AlgoLegacyMD5 Algo = "legacy_md5_rustdesk_api"
)

// legacyMD5Salt is appended to the password by lejianwen/rustdesk-api before md5.
const legacyMD5Salt = "rustdesk-api"

// MaxPasswordBytes bounds password input to keep hashing cost predictable.
// It is far above anything a human types; there is no 32-char cap (legacy bug).
const MaxPasswordBytes = 1024

// ErrPasswordTooLong is returned by Hash for inputs above MaxPasswordBytes.
var ErrPasswordTooLong = errors.New("auth: password too long")

// ErrEmptyPassword is returned by Hash for an empty password.
var ErrEmptyPassword = errors.New("auth: empty password")

// Argon2Params are the argon2id cost parameters for new hashes.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultArgon2Params follow the OWASP/RFC 9106 guidance for interactive logins
// on a small server (64 MiB, 2 passes, 2 lanes).
var DefaultArgon2Params = Argon2Params{Memory: 64 * 1024, Time: 2, Threads: 2, SaltLen: 16, KeyLen: 32}

// Hasher hashes and verifies passwords.
type Hasher struct {
	Params Argon2Params
}

// NewHasher returns a Hasher using DefaultArgon2Params.
func NewHasher() *Hasher { return &Hasher{Params: DefaultArgon2Params} }

// Hash returns an argon2id PHC string ($argon2id$v=19$m=..,t=..,p=..$salt$key).
func (h *Hasher) Hash(password string) (string, Algo, error) {
	if password == "" {
		return "", AlgoNone, ErrEmptyPassword
	}
	if len(password) > MaxPasswordBytes {
		return "", AlgoNone, ErrPasswordTooLong
	}
	p := h.Params
	salt, err := crypto.RandomBytes(int(p.SaltLen))
	if err != nil {
		return "", AlgoNone, err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), AlgoArgon2id, nil
}

// DetectAlgo sniffs the format of a stored hash.
func DetectAlgo(hash string) Algo {
	switch {
	case hash == "":
		return AlgoNone
	case strings.HasPrefix(hash, "$argon2id$"):
		return AlgoArgon2id
	case len(hash) == 60 && (strings.HasPrefix(hash, "$2a$") || strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$")):
		return AlgoBcrypt
	case isHex32(hash):
		return AlgoLegacyMD5
	default:
		return AlgoNone
	}
}

// Verify checks password against a stored hash of any supported format. An
// empty or unrecognised hash never matches, and an empty password never matches.
func (h *Hasher) Verify(hash, password string) bool {
	if hash == "" || password == "" || len(password) > MaxPasswordBytes {
		return false
	}
	switch DetectAlgo(hash) {
	case AlgoArgon2id:
		return verifyArgon2id(hash, password)
	case AlgoBcrypt:
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	case AlgoLegacyMD5:
		sum := md5.Sum([]byte(password + legacyMD5Salt))
		want := hex.EncodeToString(sum[:])
		return subtle.ConstantTimeCompare([]byte(strings.ToLower(hash)), []byte(want)) == 1
	default:
		return false
	}
}

// NeedsRehash reports whether a (successfully verified) hash should be
// replaced by a fresh argon2id hash with the current parameters.
func (h *Hasher) NeedsRehash(hash string) bool {
	if DetectAlgo(hash) != AlgoArgon2id {
		return true
	}
	p, _, _, err := parseArgon2id(hash)
	if err != nil {
		return true
	}
	want := h.Params
	return p.Memory != want.Memory || p.Time != want.Time || p.Threads != want.Threads || p.KeyLen != want.KeyLen
}

func verifyArgon2id(hash, password string) bool {
	p, salt, key, err := parseArgon2id(hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return subtle.ConstantTimeCompare(got, key) == 1
}

// parseArgon2id parses "$argon2id$v=19$m=65536,t=2,p=2$<salt>$<key>".
func parseArgon2id(hash string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("auth: malformed argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, errors.New("auth: unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, errors.New("auth: malformed argon2id params")
	}
	// Refuse absurd parameters from a tampered DB row (DoS guard).
	if p.Memory == 0 || p.Memory > 1<<21 || p.Time == 0 || p.Time > 16 || p.Threads == 0 {
		return p, nil, nil, errors.New("auth: argon2id params out of range")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, err
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, errors.New("auth: malformed argon2id key")
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))
	return p, salt, key, nil
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
