package adminapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/clientdl"
	"github.com/bropines/rdpanel/internal/store"
)

type fakeClients struct {
	res   clientdl.Result
	file  string
	opens int
}

func (f *fakeClients) Release(context.Context) clientdl.Result { return f.res }

func (f *fakeClients) Open(_ context.Context, id string) (*os.File, clientdl.Asset, error) {
	f.opens++
	a, ok := f.res.Release.Asset(id)
	if !ok {
		return nil, clientdl.Asset{}, clientdl.ErrUnavailable
	}
	fh, err := os.Open(f.file)
	return fh, a, err
}

func TestClientDownloads(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rustdesk.exe")
	if err := os.WriteFile(exe, []byte("MZ installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClients{file: exe, res: clientdl.Result{Release: &clientdl.Release{
		Version: "1.5.0", URL: "https://github.com/rustdesk/rustdesk/releases/tag/1.5.0",
		PublishedAt: time.Date(2026, 9, 30, 16, 11, 3, 0, time.UTC),
		Assets: []clientdl.Asset{
			{ID: "windows-x64", Name: "rustdesk-1.5.0-x86_64.exe", Size: 12, URL: "https://github.com/rustdesk/rustdesk/releases/download/1.5.0/rustdesk-1.5.0-x86_64.exe"},
			{ID: "android-universal", Name: "rustdesk-1.5.0-universal-signed.apk", Size: 3, URL: "https://github.com/x.apk"},
		},
	}}}
	e := newB2Env(t, b2opts{clients: fc})
	keyFile := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte("MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.deps.Config.HBBS.PublicKeyFile = keyFile

	if code, _, _ := e.client().do(http.MethodGet, "/clients", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _, _ := e.client().do(http.MethodGet, "/clients/windows/x64", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous download: %d", code)
	}

	user, _ := e.as("bob", store.RoleUser)
	code, m, _ := user.do(http.MethodGet, "/clients", nil)
	if code != http.StatusOK || m["enabled"] != true || m["version"] != "1.5.0" || m["id_server"] != "panel.test" ||
		m["api_server"] != "http://panel.test" || m["public_key"] != "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=" {
		t.Fatalf("clients: %d %v", code, m)
	}
	name, _ := m["windows_file_name"].(string)
	got, ok := clientdl.ParseWindowsFileName(name)
	if !ok || got.Host != "panel.test" || got.API != "http://panel.test" || got.Key != "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A=" {
		t.Fatalf("file name %q -> %+v", name, got)
	}
	if cfg, _ := m["config_string"].(string); cfg == "" {
		t.Fatal("config string missing")
	}
	if assets, _ := m["assets"].([]any); len(assets) != 2 {
		t.Fatalf("assets %v", m["assets"])
	}

	code, m, resp := user.do(http.MethodGet, "/clients/windows/x64", nil)
	if code != http.StatusOK || m["_raw"] != "MZ installer" {
		t.Fatalf("download: %d %v", code, m)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="`+name+`"`) {
		t.Fatalf("Content-Disposition %q", cd)
	}

	// a range request (resumed download)
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/admin/clients/windows/x64", nil)
	req.Header.Set("Range", "bytes=3-")
	r2, err := user.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusPartialContent || string(b) != "installer" {
		t.Fatalf("range: %d %q", r2.StatusCode, b)
	}

	if code, m, _ := user.do(http.MethodGet, "/clients/windows/arm64", nil); code != http.StatusBadGateway || errCode(m) != "client_unavailable" {
		t.Fatalf("missing asset: %d %v", code, m)
	}
	if code, _, _ := user.do(http.MethodGet, "/clients/windows/x86", nil); code != http.StatusNotFound {
		t.Fatalf("unknown arch: %d", code)
	}
}

func TestClientDownloadsDisabled(t *testing.T) {
	e := newB2Env(t, b2opts{})
	user, _ := e.as("bob", store.RoleUser)
	code, m, _ := user.do(http.MethodGet, "/clients", nil)
	if code != http.StatusOK || m["enabled"] != false || m["config_string"] == "" || m["windows_file_name"] != nil {
		t.Fatalf("disabled: %d %v", code, m)
	}
	if code, _, _ := user.do(http.MethodGet, "/clients/windows/x64", nil); code != http.StatusNotFound {
		t.Fatalf("download when disabled: %d", code)
	}
}
