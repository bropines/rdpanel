package crypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KeySource tells where a key came from (for logging; never log the key itself).
type KeySource string

// Key sources.
const (
	KeyFromEnv       KeySource = "env"
	KeyFromFile      KeySource = "file"
	KeyGeneratedFile KeySource = "generated"
)

// LoadAEADKey resolves the 32-byte secrets-at-rest key: a base64 value (from
// RDPANEL_SECRET_KEY) wins; otherwise the key file is read, or created with
// mode 0600 when it does not exist yet.
func LoadAEADKey(envB64, path string) ([]byte, KeySource, error) {
	if strings.TrimSpace(envB64) != "" {
		key, err := DecodeKey(envB64)
		if err != nil {
			return nil, "", fmt.Errorf("crypto: RDPANEL_SECRET_KEY: %w", err)
		}
		return key, KeyFromEnv, nil
	}
	return loadOrCreate(path, KeySize, b64Encoding, func(raw []byte) ([]byte, error) {
		if len(raw) == KeySize { // raw binary file
			return raw, nil
		}
		return DecodeKey(string(raw))
	})
}

// LoadOrCreateTextKey reads a text key file (e.g. the generated JWT key used
// when RUSTDESK_API_JWT_KEY is unset), creating it with n random bytes encoded
// as unpadded base64url when missing. The file content (trimmed) is the key.
func LoadOrCreateTextKey(path string, n int) ([]byte, KeySource, error) {
	return loadOrCreate(path, n, textEncoding, func(raw []byte) ([]byte, error) {
		k := bytes.TrimSpace(raw)
		if len(k) == 0 {
			return nil, errors.New("key file is empty")
		}
		return k, nil
	})
}

// DecodeKey decodes a base64 (std or url, padded or not) 32-byte key.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) != KeySize {
				return nil, fmt.Errorf("key must decode to %d bytes, got %d", KeySize, len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("key is not valid base64")
}

type encodeFunc func(key []byte) []byte

func b64Encoding(key []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(key) + "\n")
}

func textEncoding(key []byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(key) + "\n")
}

func loadOrCreate(path string, n int, encode encodeFunc, parse func([]byte) ([]byte, error)) ([]byte, KeySource, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := parse(raw)
		if err != nil {
			return nil, "", fmt.Errorf("crypto: key file %s: %w", path, err)
		}
		return key, KeyFromFile, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("crypto: read key file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, "", fmt.Errorf("crypto: create key dir: %w", err)
	}
	random, err := RandomBytes(n)
	if err != nil {
		return nil, "", err
	}
	// O_EXCL: never clobber a key another process created concurrently.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return loadOrCreate(path, n, encode, parse)
		}
		return nil, "", fmt.Errorf("crypto: create key file: %w", err)
	}
	content := encode(random)
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("crypto: write key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, "", fmt.Errorf("crypto: sync key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, "", fmt.Errorf("crypto: close key file: %w", err)
	}
	key, err := parse(content)
	if err != nil {
		return nil, "", err
	}
	return key, KeyGeneratedFile, nil
}
