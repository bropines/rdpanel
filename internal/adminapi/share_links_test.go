package adminapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/clientdl"
	"github.com/bropines/rdpanel/internal/store"
)

const shareTestKey = "MEbIUN8NZFq0Vu80BjNNIGDbQKdXxcg8QTbo69qrH+A="

// shareEnv is a b2env with share links on, the fake client downloads and the
// public /s/* routes on a second server.
type shareEnv struct {
	*b2env
	pub   *httptest.Server
	files *fakeClients
}

func newShareEnv(t *testing.T, enabled bool) *shareEnv {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "rustdesk.exe")
	if err := os.WriteFile(exe, []byte("MZ installer"), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClients{file: exe, res: clientdl.Result{Release: &clientdl.Release{
		Version: "1.5.0", URL: "https://github.com/rustdesk/rustdesk/releases/tag/1.5.0",
		Assets: []clientdl.Asset{
			{ID: "windows-x64", Name: "rustdesk-1.5.0-x86_64.exe", Size: 12, URL: "https://github.com/x.exe"},
			{ID: "macos-arm64", Name: "rustdesk-1.5.0-aarch64.dmg", Size: 5, URL: "https://github.com/x.dmg"},
		},
	}}}
	e := newB2Env(t, b2opts{clients: fc})
	keyFile := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte(shareTestKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.deps.Config.HBBS.PublicKeyFile = keyFile
	e.deps.Config.ShareLinks.Enabled = enabled
	web := fstest.MapFS{
		"share.html":          {Data: []byte(`<!doctype html><link rel="icon" href="/favicon.svg"><script type="module" src="/assets/share-x.js"></script>`)},
		"assets/share-x.js":   {Data: []byte(`console.log(1)`)},
		"favicon.svg":         {Data: []byte(`<svg/>`)},
		"index.html":          {Data: []byte(`admin`)},
		"assets/index-y.js":   {Data: []byte(`admin()`)},
		"assets/nested/z.css": {Data: []byte(`a{}`)},
	}
	r := chi.NewRouter()
	RegisterSharePublic(r, e.deps, web)
	pub := httptest.NewServer(r)
	t.Cleanup(pub.Close)
	return &shareEnv{b2env: e, pub: pub, files: fc}
}

// get calls a public path and returns the status, body and response.
func (e *shareEnv) get(path string, hdr map[string]string) (int, string, *http.Response) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.pub.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp
}

// tokenOf returns the token of a ShareLink DTO's url.
func tokenOf(t *testing.T, m map[string]any) string {
	t.Helper()
	u, _ := m["url"].(string)
	tok, ok := strings.CutPrefix(u, "http://panel.test/s/")
	if !ok || len(tok) != 22 {
		t.Fatalf("url %q", u)
	}
	return tok
}

func TestShareLinksDisabled(t *testing.T) {
	e := newShareEnv(t, false)
	admin, _ := e.as("root", store.RoleAdmin)
	code, m, _ := admin.do(http.MethodGet, "/share-links", nil)
	wantErr(t, "list when off", code, 404, m, "not_found")
	code, m, _ = admin.do(http.MethodPost, "/share-links", map[string]any{})
	wantErr(t, "create when off", code, 404, m, "not_found")
	if _, me, _ := admin.do(http.MethodGet, "/auth/me", nil); me["can_share_links"] != false {
		t.Fatalf("me: %v", me)
	}
	if _, s, _ := admin.do(http.MethodGet, "/settings", nil); s["share_links"].(map[string]any)["enabled"] != false {
		t.Fatalf("settings: %v", s["share_links"])
	}
	for _, p := range []string{"/s/AAAAAAAAAAAAAAAAAAAAAA", "/s/AAAAAAAAAAAAAAAAAAAAAA/info", "/s/assets/share-x.js", "/s/favicon.svg"} {
		if code, _, _ := e.get(p, nil); code != http.StatusNotFound {
			t.Errorf("%s when off: %d", p, code)
		}
	}
}

func TestShareLinksPermissions(t *testing.T) {
	e := newShareEnv(t, true)
	admin, _ := e.as("root", store.RoleAdmin)
	bob, bobUser := e.as("bob", store.RoleUser)

	// A user without the group right.
	if _, me, _ := bob.do(http.MethodGet, "/auth/me", nil); me["can_share_links"] != false {
		t.Fatalf("bob me: %v", me)
	}
	code, m, _ := bob.do(http.MethodGet, "/share-links", nil)
	wantErr(t, "list without right", code, 403, m, "forbidden")
	code, m, _ = bob.do(http.MethodPost, "/share-links", map[string]any{"label": "x"})
	wantErr(t, "create without right", code, 403, m, "forbidden")

	// The admin grants the right through the group editor.
	code, g, _ := admin.do(http.MethodPost, "/user-groups", map[string]any{"name": "Helpdesk", "can_share_links": true})
	want(t, "create group", code, 201, g)
	if g["can_share_links"] != true {
		t.Fatalf("group: %v", g)
	}
	code, m, _ = admin.do(http.MethodPost, "/device-groups", map[string]any{"name": "PCs", "can_share_links": true})
	wantErr(t, "device group right", code, 400, m, "validation_failed")
	code, m, _ = admin.do(http.MethodPatch, "/users/"+idStr(bobUser.ID), map[string]any{"user_group_id": b2bNum(g["id"])})
	want(t, "move bob", code, 200, m)
	if _, me, _ := bob.do(http.MethodGet, "/auth/me", nil); me["can_share_links"] != true {
		t.Fatalf("bob me with right: %v", me)
	}

	code, own, _ := bob.do(http.MethodPost, "/share-links", map[string]any{"label": "Front desk", "expires_in": "24h"})
	want(t, "bob create", code, 201, own)
	if own["status"] != "active" || own["label"] != "Front desk" || own["expires_at"] == nil ||
		own["owner"].(map[string]any)["username"] != "bob" {
		t.Fatalf("created: %v", own)
	}
	tokenOf(t, own)
	code, adm, _ := admin.do(http.MethodPost, "/share-links", map[string]any{})
	want(t, "admin create", code, 201, adm)
	if adm["expires_at"] != nil || adm["max_downloads"] != nil {
		t.Fatalf("permanent: %v", adm)
	}

	// Users see their own links only; scope=all is for admins.
	code, m, _ = bob.do(http.MethodGet, "/share-links", nil)
	if items := b2bItems(m); code != 200 || len(items) != 1 || items[0]["id"] != own["id"] || items[0]["url"] != own["url"] {
		t.Fatalf("bob list: %d %v", code, m)
	}
	code, m, _ = bob.do(http.MethodGet, "/share-links?scope=all", nil)
	wantErr(t, "bob scope all", code, 403, m, "forbidden")
	code, m, _ = admin.do(http.MethodGet, "/share-links?scope=all", nil)
	if items := b2bItems(m); code != 200 || len(items) != 2 {
		t.Fatalf("admin all: %d %v", code, m)
	}
	code, m, _ = admin.do(http.MethodGet, "/share-links?scope=nope", nil)
	wantErr(t, "bad scope", code, 400, m, "validation_failed")

	// Users cannot revoke others' links (404, nothing revealed); admins can.
	code, m, _ = bob.do(http.MethodDelete, "/share-links/"+idOf(adm), nil)
	wantErr(t, "bob revokes admin link", code, 404, m, "not_found")
	code, _, _ = admin.do(http.MethodDelete, "/share-links/"+idOf(own), nil)
	want(t, "admin revokes bob link", code, 204, nil)
	code, m, _ = bob.do(http.MethodDelete, "/share-links/"+idOf(own), nil)
	wantErr(t, "revoke twice", code, 404, m, "not_found")
	if _, m, _ := bob.do(http.MethodGet, "/share-links", nil); len(b2bItems(m)) != 0 {
		t.Fatalf("revoked still listed: %v", m)
	}

	// Taking the right away again: 403, and existing links stop working.
	code, own2, _ := bob.do(http.MethodPost, "/share-links", map[string]any{})
	want(t, "bob create 2", code, 201, own2)
	code, m, _ = admin.do(http.MethodPatch, "/user-groups/"+idOf(g), map[string]any{"can_share_links": false})
	want(t, "revoke right", code, 200, m)
	code, m, _ = bob.do(http.MethodGet, "/share-links", nil)
	wantErr(t, "list after right removed", code, 403, m, "forbidden")
	if code, _, _ := e.get("/s/"+tokenOf(t, own2)+"/info", nil); code != http.StatusGone {
		t.Fatalf("suspended link: %d", code)
	}
	code, m, _ = admin.do(http.MethodGet, "/share-links?scope=all", nil)
	var found bool
	for _, it := range b2bItems(m) {
		if it["id"] == own2["id"] {
			found = it["status"] == "suspended"
		}
	}
	if code != 200 || !found {
		t.Fatalf("admin sees suspended: %v", m)
	}

	// Every create and revoke is audited, without the token.
	rows, _, err := e.st.AdminAudit.ListPanel(e.ctx, store.AdminAuditQuery{ActionPrefix: "share_link."})
	if err != nil || len(rows) != 4 {
		t.Fatalf("audit rows %d %v", len(rows), err)
	}
	for _, r := range rows {
		b, _ := json.Marshal(r.Details)
		for _, link := range []map[string]any{own, adm, own2} {
			if strings.Contains(string(b), tokenOf(t, link)) {
				t.Fatalf("token in audit: %s", b)
			}
		}
	}
}

func TestShareLinkValidation(t *testing.T) {
	e := newShareEnv(t, true)
	admin, _ := e.as("root", store.RoleAdmin)
	now := e.clk.now()
	for name, body := range map[string]map[string]any{
		"label":       {"label": strings.Repeat("x", 65)},
		"control":     {"label": "a\x07b"},
		"preset":      {"expires_in": "2d"},
		"past":        {"expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
		"far":         {"expires_at": now.Add(400 * 24 * time.Hour).Format(time.RFC3339)},
		"both":        {"expires_in": "1h", "expires_at": now.Add(time.Hour).Format(time.RFC3339)},
		"downloads 0": {"max_downloads": 0},
		"unknown":     {"password": "x"},
	} {
		code, m, _ := admin.do(http.MethodPost, "/share-links", body)
		wantErr(t, name, code, 400, m, "validation_failed")
	}
	at := now.Add(48 * time.Hour).Truncate(time.Second)
	code, m, _ := admin.do(http.MethodPost, "/share-links", map[string]any{"expires_at": at.Format(time.RFC3339), "max_downloads": 3})
	want(t, "custom date", code, 201, m)
	if got, _ := time.Parse(time.RFC3339, m["expires_at"].(string)); !got.Equal(at) || b2bNum(m["max_downloads"]) != 3 {
		t.Fatalf("custom: %v", m)
	}
}

func TestSharePublicEndpoints(t *testing.T) {
	e := newShareEnv(t, true)
	admin, _ := e.as("root", store.RoleAdmin)
	code, m, _ := admin.do(http.MethodPatch, "/settings", map[string]any{"share_links": map[string]any{
		"page_title": "Acme <support>", "description": "Call us\r\nanytime"}})
	want(t, "settings", code, 200, m)
	if sl := m["share_links"].(map[string]any); sl["enabled"] != true || sl["description"] != "Call us\nanytime" {
		t.Fatalf("settings: %v", sl)
	}
	// The switch is a panel setting: turning it off takes the public pages with it,
	// and turning it back on brings them back, whatever the environment said.
	code, m, _ = admin.do(http.MethodPatch, "/settings", map[string]any{"share_links": map[string]any{"enabled": false}})
	want(t, "turn off", code, 200, m)
	if sl := m["share_links"].(map[string]any); sl["enabled"] != false {
		t.Fatalf("turn off: %v", sl)
	}
	if code, _, _ := e.get("/s/whatever", nil); code != 404 {
		t.Fatalf("public page while off: %d", code)
	}
	if code, m, _ := admin.do(http.MethodPost, "/share-links", map[string]any{"label": "while-off"}); code != 404 {
		t.Fatalf("create while off: %d %v", code, m)
	}
	code, m, _ = admin.do(http.MethodPatch, "/settings", map[string]any{"share_links": map[string]any{"enabled": true}})
	want(t, "turn on", code, 200, m)
	if sl := m["share_links"].(map[string]any); sl["enabled"] != true {
		t.Fatalf("turn on: %v", sl)
	}

	code, link, _ := admin.do(http.MethodPost, "/share-links", map[string]any{"label": "secret-label", "max_downloads": 2})
	want(t, "create", code, 201, link)
	tok := tokenOf(t, link)

	// The page: the Vite entry with its URLs moved under /s/, never cached or indexed.
	code, body, resp := e.get("/s/"+tok, nil)
	if code != 200 || !strings.Contains(body, `src="/s/assets/share-x.js"`) || !strings.Contains(body, `href="/s/favicon.svg"`) {
		t.Fatalf("page: %d %s", code, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(resp.Header.Get("X-Robots-Tag"), "noindex") ||
		resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("headers: %v", resp.Header)
	}
	if code, body, _ := e.get("/s/assets/share-x.js", nil); code != 200 || body != "console.log(1)" {
		t.Fatalf("asset: %d %s", code, body)
	}
	if code, _, _ := e.get("/s/assets/../share.html", nil); code != 404 {
		t.Fatalf("asset traversal: %d", code)
	}
	if code, _, _ := e.get("/s/favicon.svg", nil); code != 200 {
		t.Fatalf("favicon: %d", code)
	}

	// Info: connection settings and downloads only.
	code, body, _ = e.get("/s/"+tok+"/info", nil)
	var info map[string]any
	_ = json.Unmarshal([]byte(body), &info)
	if code != 200 || info["id_server"] != "panel.test" || info["api_server"] != "http://panel.test" ||
		info["public_key"] != shareTestKey || info["config_string"] == "" || info["title"] != "Acme <support>" ||
		info["version"] != "1.5.0" || info["windows_file_name"] == nil {
		t.Fatalf("info: %d %s", code, body)
	}
	if assets, _ := info["assets"].([]any); len(assets) != 1 || assets[0].(map[string]any)["id"] != "macos-arm64" {
		t.Fatalf("assets (no unconfigured Windows installers): %v", info["assets"])
	}
	if win, _ := info["windows"].([]any); len(win) != 1 || win[0].(map[string]any)["arch"] != "x64" {
		t.Fatalf("windows: %v", info["windows"])
	}
	for _, leak := range []string{"secret-label", "root", "max_downloads", "views", "expires"} {
		if strings.Contains(body, leak) {
			t.Fatalf("info leaks %q: %s", leak, body)
		}
	}

	// Downloads: counted, resumed ranges free, refused once used up.
	code, body, resp = e.get("/s/"+tok+"/windows/x64", nil)
	if code != 200 || body != "MZ installer" || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="rustdesk-licensed-`) ||
		resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %s %v", code, body, resp.Header)
	}
	if got, ok := clientdl.ParseWindowsFileName(strings.TrimSuffix(strings.TrimPrefix(resp.Header.Get("Content-Disposition"),
		`attachment; filename="`), `"`)); !ok || got.Host != "panel.test" || got.Key != shareTestKey {
		t.Fatalf("file name: %v", resp.Header.Get("Content-Disposition"))
	}
	if code, body, _ := e.get("/s/"+tok+"/windows/x64", map[string]string{"Range": "bytes=3-"}); code != 206 || body != "installer" {
		t.Fatalf("range: %d %s", code, body)
	}
	if code, _, _ := e.get("/s/"+tok+"/windows/arm64", nil); code != http.StatusBadGateway {
		t.Fatalf("missing asset: %d", code)
	}
	if code, _, _ := e.get("/s/"+tok+"/windows/x86", nil); code != 404 {
		t.Fatalf("unknown arch: %d", code)
	}
	if code, _, _ := e.get("/s/"+tok+"/windows/x64", map[string]string{"Range": "bytes=0-"}); code != 206 {
		t.Fatalf("second download: %d", code)
	}
	if code, _, _ := e.get("/s/"+tok+"/windows/x64", nil); code != http.StatusGone {
		t.Fatalf("third download: %d", code)
	}
	if code, _, _ := e.get("/s/"+tok+"/windows/x64", map[string]string{"Range": "bytes=5-"}); code != 206 {
		t.Fatalf("resume after the last download: %d", code)
	}
	code, body, _ = e.get("/s/"+tok, nil)
	if code != http.StatusGone || !strings.Contains(body, "/s/assets/share-x.js") {
		t.Fatalf("exhausted page: %d %s", code, body)
	}
	_, m, _ = admin.do(http.MethodGet, "/share-links", nil)
	if it := b2bItems(m)[0]; it["status"] != "exhausted" || b2bNum(it["views"]) != 1 || b2bNum(it["downloads"]) != 2 {
		t.Fatalf("counters: %v", it)
	}

	// Expiry and revocation.
	code, tmp, _ := admin.do(http.MethodPost, "/share-links", map[string]any{"expires_in": "1h"})
	want(t, "temporary", code, 201, tmp)
	code, perm, _ := admin.do(http.MethodPost, "/share-links", map[string]any{})
	want(t, "permanent", code, 201, perm)
	if code, _, _ := e.get("/s/"+tokenOf(t, tmp)+"/info", nil); code != 200 {
		t.Fatalf("temporary before expiry: %d", code)
	}
	e.clk.advance(2 * time.Hour)
	if code, _, _ := e.get("/s/"+tokenOf(t, tmp)+"/info", nil); code != http.StatusGone {
		t.Fatalf("expired info: %d", code)
	}
	if code, _, _ := e.get("/s/"+tokenOf(t, tmp)+"/windows/x64", nil); code != http.StatusGone {
		t.Fatalf("expired download: %d", code)
	}
	if code, _, _ := e.get("/s/"+tokenOf(t, perm), nil); code != 200 {
		t.Fatalf("permanent: %d", code)
	}
	code, _, _ = admin.do(http.MethodDelete, "/share-links/"+idOf(perm), nil)
	want(t, "revoke", code, 204, nil)
	if code, _, _ := e.get("/s/"+tokenOf(t, perm)+"/info", nil); code != http.StatusGone {
		t.Fatalf("revoked: %d", code)
	}

	// Unknown and malformed tokens: 404 with the same page.
	if code, body, _ := e.get("/s/AAAAAAAAAAAAAAAAAAAAAA", nil); code != 404 || !strings.Contains(body, "share-x.js") {
		t.Fatalf("unknown page: %d %s", code, body)
	}
	for _, p := range []string{"/s/AAAAAAAAAAAAAAAAAAAAAA/info", "/s/short/info", "/s/" + strings.Repeat("A", 22) + "!/info"} {
		if code, _, _ := e.get(p, nil); code != 404 {
			t.Fatalf("%s: %d", p, code)
		}
	}
}

func TestSharePublicGuessBackoff(t *testing.T) {
	e := newShareEnv(t, true)
	admin, _ := e.as("root", store.RoleAdmin)
	_, link, _ := admin.do(http.MethodPost, "/share-links", map[string]any{})
	// A strict backoff: three free failures.
	d := e.deps
	d.Limiter = auth.NewBackoff(auth.BackoffConfig{Free: 3, Base: time.Minute, Max: time.Hour, Forget: time.Hour}, e.clk.now)
	r := chi.NewRouter()
	RegisterSharePublic(r, d, fstest.MapFS{})
	e.pub = httptest.NewServer(r)
	t.Cleanup(e.pub.Close)
	for i := 0; i < 4; i++ {
		if code, _, _ := e.get("/s/BBBBBBBBBBBBBBBBBBBBBB/info", nil); code != http.StatusNotFound {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, body, _ := e.get("/s/BBBBBBBBBBBBBBBBBBBBBB/info", nil); code != http.StatusTooManyRequests || !strings.Contains(body, "rate_limited") {
		t.Fatalf("no backoff after repeated unknown tokens: %d %s", code, body)
	}
	// The same client is held off even with a valid token until the backoff ends.
	if code, _, resp := e.get("/s/"+tokenOf(t, link)+"/info", nil); code != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("valid token during backoff: %d", code)
	}
}
