package clientdl

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsFileName(t *testing.T) {
	// Short on purpose: with a 44-character key this is about what fits into
	// maxFileName (see filename_test.go for the budget and what does not fit).
	s := ServerSettings{
		Host: "d.example.org",
		Key:  "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=",
		API:  "https://p.example",
	}
	name, err := WindowsFileName(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "rustdesk-licensed-") || !strings.HasSuffix(name, ".exe") || strings.ContainsAny(name, `/\:*?"<>|+=`) {
		t.Fatalf("name %q", name)
	}
	for _, n := range []string{name, strings.TrimSuffix(name, ".exe") + " (1).exe", `C:\Users\me\Downloads\` + name, name + ".exe"} {
		if got, ok := ParseWindowsFileName(n); !ok || got != s {
			t.Fatalf("parse %q = %+v %v", n, got, ok)
		}
	}
}

// Keys with '/' (half of all keys) cannot go into the host=...,key=... form;
// the licensed form must work for any key.
func TestWindowsFileNameAnyKey(t *testing.T) {
	for i := 0; i < 2000; i++ {
		k := make([]byte, 32)
		_, _ = rand.Read(k)
		s := ServerSettings{Host: fmt.Sprintf("h%d.ex.org", i), Key: base64.StdEncoding.EncodeToString(k), API: "https://p.ex.org"}
		name, err := WindowsFileName(s)
		if err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
		if got, ok := ParseWindowsFileName(name); !ok || got != s {
			t.Fatalf("round trip %q = %+v", name, got)
		}
	}
}

// "--" (RustDesk's separator) only shows up in the encoding of non-ASCII
// JSON, e.g. an IDN host name; the alignment shift must avoid it.
func TestWindowsFileNameAvoidsSeparator(t *testing.T) {
	for r := rune(0x80); r < 0x3000; r++ {
		s := ServerSettings{Host: "d" + string(r) + ".example", Key: "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=", API: "https://p.example"}
		raw, _ := json.Marshal(s)
		if !strings.Contains(reverse(base64.RawURLEncoding.EncodeToString(raw)), "--") {
			continue
		}
		name, err := WindowsFileName(s)
		if err != nil {
			t.Fatalf("%q: %v", s.Host, err)
		}
		if got, ok := ParseWindowsFileName(name); !ok || got != s {
			t.Fatalf("round trip %q = %+v", name, got)
		}
		return
	}
	t.Fatal("found no settings whose plain encoding contains \"--\"")
}

func TestWindowsFileNameTooLong(t *testing.T) {
	_, err := WindowsFileName(ServerSettings{Host: strings.Repeat("a", 200) + ".example.com", Key: "k", API: "https://x"})
	if !errors.Is(err, ErrFileName) {
		t.Fatalf("err = %v", err)
	}
}

type fakeGitHub struct {
	srv     *httptest.Server
	api     atomic.Int32
	dl      atomic.Int32
	fail    atomic.Bool
	tag     string
	files   map[string][]byte
	digests map[string]string
}

func newFakeGitHub(t *testing.T, tag string, files map[string][]byte) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{tag: tag, files: files, digests: map[string]string{}}
	for n, b := range files {
		sum := sha256.Sum256(b)
		g.digests[n] = "sha256:" + hex.EncodeToString(sum[:])
	}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/rustdesk/rustdesk/releases/latest" || r.URL.Path == "/repos/rustdesk/rustdesk/releases/tags/"+g.tag:
			g.api.Add(1)
			if g.fail.Load() {
				http.Error(w, "rate limited", http.StatusForbidden)
				return
			}
			type asset struct {
				Name   string `json:"name"`
				Size   int    `json:"size"`
				URL    string `json:"browser_download_url"`
				Digest string `json:"digest,omitempty"`
			}
			var assets []asset
			for n, b := range g.files {
				assets = append(assets, asset{n, len(b), g.srv.URL + "/rustdesk/rustdesk/releases/download/" + g.tag + "/" + n, g.digests[n]})
			}
			assets = append(assets, asset{"rustdesk-" + g.tag + "-x86_64.msi", 10, "https://evil.example/x.msi", ""})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": g.tag, "html_url": g.srv.URL + "/rustdesk/rustdesk/releases/tag/" + g.tag,
				"published_at": "2026-09-30T16:11:03Z", "assets": assets,
			})
		case strings.HasPrefix(r.URL.Path, "/rustdesk/rustdesk/releases/download/"+g.tag+"/"):
			g.dl.Add(1)
			b, ok := g.files[filepath.Base(r.URL.Path)]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) service(dir, version string) *Service {
	s := New(dir, version, "test")
	s.API, s.Web = g.srv.URL, g.srv.URL
	return s
}

func TestReleaseLookup(t *testing.T) {
	exe := []byte("MZ windows installer")
	g := newFakeGitHub(t, "1.5.0", map[string][]byte{
		"rustdesk-1.5.0-x86_64.exe":           exe,
		"rustdesk-1.5.0-0.x86_64.rpm":         []byte("rpm"),
		"rustdesk-1.5.0-universal-signed.apk": []byte("apk"),
		"rustdesk-1.5.0-x86_64-sciter.deb":    []byte("not offered"),
	})
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := g.service(t.TempDir(), "")
	s.Now = func() time.Time { return now }

	res := s.Release(context.Background())
	if res.Err != "" || res.Release == nil || res.Release.Version != "1.5.0" {
		t.Fatalf("res %+v", res)
	}
	ids := []string{}
	for _, a := range res.Release.Assets {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "windows-x64,linux-rpm-x64,android-universal" {
		t.Fatalf("assets %v (the msi outside the repository must be dropped)", ids)
	}
	if a, _ := res.Release.Asset("windows-x64"); a.Size != int64(len(exe)) || len(a.SHA256) != 64 {
		t.Fatalf("asset %+v", a)
	}

	// cached; a failure keeps the last good answer and is retried later
	s.Release(context.Background())
	if g.api.Load() != 1 {
		t.Fatalf("api calls %d", g.api.Load())
	}
	g.fail.Store(true)
	now = now.Add(DefaultTTL + time.Minute)
	res = s.Release(context.Background())
	if res.Err == "" || res.Release == nil || res.Release.Version != "1.5.0" {
		t.Fatalf("after failure %+v", res)
	}
	s.Release(context.Background())
	if g.api.Load() != 2 {
		t.Fatalf("failure must be retried only after %v (calls %d)", DefaultRetry, g.api.Load())
	}
}

func TestReleasePinned(t *testing.T) {
	g := newFakeGitHub(t, "1.4.2", map[string][]byte{"rustdesk-1.4.2-x86_64.exe": []byte("x")})
	res := g.service(t.TempDir(), "1.4.2").Release(context.Background())
	if res.Err != "" || res.Release.Version != "1.4.2" {
		t.Fatalf("res %+v", res)
	}
}

func TestOpenDownloadsOnceAndVerifies(t *testing.T) {
	exe := []byte("MZ windows installer bytes")
	g := newFakeGitHub(t, "1.5.0", map[string][]byte{"rustdesk-1.5.0-x86_64.exe": exe})
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "1.4.2"), 0o750); err != nil {
		t.Fatal(err)
	}
	s := g.service(dir, "")

	for i := 0; i < 2; i++ {
		f, a, err := s.Open(context.Background(), "windows-x64")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(f)
		_ = f.Close()
		if string(b) != string(exe) || a.Name != "rustdesk-1.5.0-x86_64.exe" {
			t.Fatalf("got %q %+v", b, a)
		}
	}
	if g.dl.Load() != 1 {
		t.Fatalf("downloads %d", g.dl.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "1.4.2")); !os.IsNotExist(err) {
		t.Fatal("copies of other versions must be removed")
	}
	if _, _, err := s.Open(context.Background(), "macos-arm64"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing asset: %v", err)
	}
}

func TestOpenRejectsWrongDigest(t *testing.T) {
	g := newFakeGitHub(t, "1.5.0", map[string][]byte{"rustdesk-1.5.0-x86_64.exe": []byte("tampered")})
	g.digests["rustdesk-1.5.0-x86_64.exe"] = "sha256:" + strings.Repeat("ab", 32)
	dir := t.TempDir()
	_, _, err := g.service(dir, "").Open(context.Background(), "windows-x64")
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "1.5.0")); len(entries) != 0 {
		t.Fatalf("a rejected download must leave nothing behind: %v", entries)
	}
}
