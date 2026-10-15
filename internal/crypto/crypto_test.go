package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	return bytes.Repeat([]byte{0x42}, KeySize)
}

func TestSealOpenRoundTrip(t *testing.T) {
	a, err := NewAEAD(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range [][]byte{nil, []byte(""), []byte("hunter2"), bytes.Repeat([]byte("x"), 4096)} {
		ct, err := a.Seal(pt, []byte("ab_entry.password:1"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := a.Open(ct, []byte("ab_entry.password:1"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatalf("round trip mismatch: %q != %q", got, pt)
		}
	}
}

func TestSealIsRandomized(t *testing.T) {
	a, _ := NewAEAD(testKey(t))
	c1, _ := a.Seal([]byte("same"), nil)
	c2, _ := a.Seal([]byte("same"), nil)
	if bytes.Equal(c1, c2) {
		t.Fatal("two seals of the same plaintext must differ (random nonce)")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	a, _ := NewAEAD(testKey(t))
	ct, _ := a.SealString("secret", "ad-1")

	if _, err := a.OpenString(ct, "ad-2"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong AD: err = %v", err)
	}
	flipped := bytes.Clone(ct)
	flipped[len(flipped)-1] ^= 1
	if _, err := a.OpenString(flipped, "ad-1"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("tampered: err = %v", err)
	}
	if _, err := a.OpenString(ct[:10], "ad-1"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("truncated: err = %v", err)
	}
	other, _ := NewAEAD(bytes.Repeat([]byte{1}, KeySize))
	if _, err := other.OpenString(ct, "ad-1"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong key: err = %v", err)
	}
	badVersion := bytes.Clone(ct)
	badVersion[0] = 0x02
	if _, err := a.OpenString(badVersion, "ad-1"); !errors.Is(err, ErrDecrypt) {
		t.Errorf("unknown version: err = %v", err)
	}
}

func TestNewAEADKeyLength(t *testing.T) {
	if _, err := NewAEAD(make([]byte, 16)); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestLoadAEADKeyGeneratesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "secret.key")
	k1, src, err := LoadAEADKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	if src != KeyGeneratedFile || len(k1) != KeySize {
		t.Fatalf("src=%s len=%d", src, len(k1))
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %o, want 600", perm)
		}
	}
	k2, src, err := LoadAEADKey("", path)
	if err != nil {
		t.Fatal(err)
	}
	if src != KeyFromFile || !bytes.Equal(k1, k2) {
		t.Fatal("second load must return the same key from file")
	}
}

func TestLoadAEADKeyFromEnv(t *testing.T) {
	want := bytes.Repeat([]byte{7}, KeySize)
	got, src, err := LoadAEADKey(base64.StdEncoding.EncodeToString(want), filepath.Join(t.TempDir(), "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if src != KeyFromEnv || !bytes.Equal(got, want) {
		t.Fatal("env key not used")
	}
	if _, _, err := LoadAEADKey("bm90LTMyLWJ5dGVz", ""); err == nil {
		t.Fatal("expected error for wrong-length env key")
	}
}

func TestLoadAEADKeyRawBinaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	want := bytes.Repeat([]byte{9}, KeySize)
	_ = os.WriteFile(path, want, 0o600)
	got, _, err := LoadAEADKey("", path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("raw key: %v", err)
	}
}

func TestLoadOrCreateTextKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jwt.key")
	k1, src, err := LoadOrCreateTextKey(path, 48)
	if err != nil || src != KeyGeneratedFile {
		t.Fatalf("err=%v src=%s", err, src)
	}
	if len(k1) != 64 { // 48 bytes -> 64 base64url chars
		t.Errorf("len = %d", len(k1))
	}
	k2, src, _ := LoadOrCreateTextKey(path, 48)
	if src != KeyFromFile || !bytes.Equal(k1, k2) {
		t.Fatal("reload mismatch")
	}
}

func TestHashToken(t *testing.T) {
	// sha256("abc") known answer.
	want, _ := hex.DecodeString("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	if !Equal(HashToken("abc"), want) {
		t.Fatal("sha256 KAT mismatch")
	}
	if Equal([]byte("a"), []byte("ab")) || !EqualString("x", "x") || EqualString("x", "y") {
		t.Fatal("Equal helpers wrong")
	}
}

func TestRandomToken(t *testing.T) {
	a, _ := RandomToken(32)
	b, _ := RandomToken(32)
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
}
