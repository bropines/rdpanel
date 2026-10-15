// Package crypto provides authenticated encryption for secrets at rest, key
// file management, random tokens and token hashing.
package crypto

import (
	"crypto/cipher"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the AEAD key length in bytes.
const KeySize = chacha20poly1305.KeySize

// formatV1 prefixes every ciphertext so the format (and later the key id) can
// evolve without ambiguity: version(1) || nonce(24) || ciphertext+tag.
const formatV1 byte = 0x01

// ErrDecrypt is returned when a ciphertext is malformed, was produced with a
// different key, or its associated data does not match.
var ErrDecrypt = errors.New("crypto: decryption failed")

// AEAD seals and opens small secrets (OIDC client secrets, address-book
// passwords, TOTP seeds) with XChaCha20-Poly1305. It is safe for concurrent use.
type AEAD struct {
	aead cipher.AEAD
}

// NewAEAD returns an AEAD for a 32-byte key.
func NewAEAD(key []byte) (*AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("crypto: key must be %d bytes, got %d", KeySize, len(key))
	}
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return &AEAD{aead: a}, nil
}

// Seal encrypts plaintext bound to associated data ad (e.g. "ab_entry.password:<id>").
// Binding to ad prevents a ciphertext from being copied into another row/column.
func (a *AEAD) Seal(plaintext, ad []byte) ([]byte, error) {
	nonce, err := RandomBytes(chacha20poly1305.NonceSizeX)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+a.aead.Overhead())
	out = append(out, formatV1)
	out = append(out, nonce...)
	return a.aead.Seal(out, nonce, plaintext, ad), nil
}

// Open decrypts a ciphertext produced by Seal with the same associated data.
func (a *AEAD) Open(ciphertext, ad []byte) ([]byte, error) {
	const hdr = 1 + chacha20poly1305.NonceSizeX
	if len(ciphertext) < hdr+a.aead.Overhead() || ciphertext[0] != formatV1 {
		return nil, ErrDecrypt
	}
	pt, err := a.aead.Open(nil, ciphertext[1:hdr], ciphertext[hdr:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// SealString is Seal for string plaintexts.
func (a *AEAD) SealString(plaintext string, ad string) ([]byte, error) {
	return a.Seal([]byte(plaintext), []byte(ad))
}

// OpenString is Open returning a string.
func (a *AEAD) OpenString(ciphertext []byte, ad string) (string, error) {
	pt, err := a.Open(ciphertext, []byte(ad))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
