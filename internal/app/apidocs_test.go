package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bropines/rdpanel/api"
	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/store"
)

// TestEmbeddedSpecIsCurrent: the binary serves exactly api/openapi.yaml.
func TestEmbeddedSpecIsCurrent(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, api.OpenAPIYAML) {
		t.Fatal("embedded spec differs from api/openapi.yaml")
	}
}

var docsFS = fstest.MapFS{
	"api-docs.html":        {Data: []byte(`<!doctype html><script type="module" src="/assets/api-docs-x.js"></script>`)},
	"assets/api-docs-x.js": {Data: []byte(`console.log(1)`)},
}

func newDocsApp(t *testing.T, env map[string]string) (http.Handler, map[string]string) {
	t.Helper()
	a := newTestApp(t, env)
	a.WebFS = docsFS
	u := &store.User{Username: "reader", Role: store.RoleUser}
	if err := a.Store.Users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	tok, err := auth.NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.APITokens.Create(context.Background(), &store.APIToken{UserID: u.ID, Name: "r", Prefix: tok.Plain[:10],
		TokenSHA256: tok.Hash, Scope: store.APITokenRead}); err != nil {
		t.Fatal(err)
	}
	return a.Handler(), map[string]string{"Authorization": "Bearer " + tok.Plain}
}

func TestAPIDocsAuthMode(t *testing.T) {
	h, bearer := newDocsApp(t, map[string]string{
		"RDPANEL_HBBS_ENABLED": "false",
		"RDPANEL_PUBLIC_URL":   "https://desk.example.test",
		"RDPANEL_ADMIN_URL":    "https://admin.example.test",
	})
	for _, p := range []string{"/api/v1/openapi.yaml", "/api/v1/openapi.json", "/api/v1/docs"} {
		if rec := do(t, h, http.MethodGet, p, "127.0.0.1:1", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without auth: %d", p, rec.Code)
		}
	}
	rec := do(t, h, http.MethodGet, "/api/v1/docs", "127.0.0.1:1", map[string]string{"Accept": "text/html"})
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?next=%2Fapi%2Fv1%2Fdocs" {
		t.Fatalf("browser without session: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Any other host: the spec is served byte-for-byte.
	rec = do(t, h, http.MethodGet, "/api/v1/openapi.yaml", "127.0.0.1:1", bearer)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/yaml") {
		t.Fatalf("yaml: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !bytes.Equal(rec.Body.Bytes(), api.OpenAPIYAML) {
		t.Fatal("served yaml differs from api/openapi.yaml")
	}

	// The admin host: absolute server URLs.
	req := map[string]string{"Authorization": bearer["Authorization"]}
	r := do(t, hostHandler(h, "admin.example.test"), http.MethodGet, "/api/v1/openapi.json", "127.0.0.1:1", req)
	if r.Code != http.StatusOK || r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("json: %d %q", r.Code, r.Header().Get("Content-Type"))
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI == "" || len(doc.Paths) < 50 || len(doc.Servers) != 1 || doc.Servers[0].URL != "https://admin.example.test/api/v1/admin" {
		t.Fatalf("json doc: openapi=%q paths=%d servers=%+v", doc.OpenAPI, len(doc.Paths), doc.Servers)
	}

	rec = do(t, h, http.MethodGet, "/api/v1/docs", "127.0.0.1:1", bearer)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `src="/api/v1/docs/assets/api-docs-x.js"`) {
		t.Fatalf("page: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/docs/assets/api-docs-x.js", "127.0.0.1:1", nil); rec.Code != http.StatusOK {
		t.Fatalf("asset: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/docs/assets/../api-docs.html", "127.0.0.1:1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("asset traversal: %d", rec.Code)
	}
}

func TestAPIDocsPublicAndOff(t *testing.T) {
	h, _ := newDocsApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false", "RDPANEL_API_DOCS": "public"})
	for _, p := range []string{"/api/v1/openapi.yaml", "/api/v1/docs"} {
		if rec := do(t, h, http.MethodGet, p, "127.0.0.1:1", nil); rec.Code != http.StatusOK {
			t.Fatalf("public %s: %d", p, rec.Code)
		}
	}
	h, bearer := newDocsApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false", "RDPANEL_API_DOCS": "off"})
	for _, p := range []string{"/api/v1/openapi.yaml", "/api/v1/openapi.json", "/api/v1/docs", "/api/v1/docs/assets/api-docs-x.js"} {
		if rec := do(t, h, http.MethodGet, p, "127.0.0.1:1", bearer); rec.Code != http.StatusNotFound {
			t.Fatalf("off %s: %d", p, rec.Code)
		}
	}
}

// hostHandler sets the request Host (httptest defaults to example.com).
func hostHandler(h http.Handler, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = host
		h.ServeHTTP(w, r)
	})
}
