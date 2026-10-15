package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

// ErrNoSecrets is returned by repositories that store returnable secrets when
// the Store was built without WithSecrets.
var ErrNoSecrets = errors.New("store: secrets-at-rest key not configured")

// NewUUID returns a random RFC 4122 version-4 UUID in canonical form. It is the
// default generator for public ids (address-book guids, audit guids).
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("store: crypto/rand failed: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:], b[10:])
	return string(out[:])
}
