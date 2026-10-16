package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestSPA(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":            {Data: []byte("<html>app</html>")},
		"assets/index-abc12.js": {Data: []byte("console.log(1)")},
		"favicon.svg":           {Data: []byte("<svg/>")},
		"api-docs.html":         {Data: []byte("<html>docs</html>")},
	}
	spa := NewSPA(fsys)
	if !spa.Built() {
		t.Fatal("built UI not detected")
	}

	rec := serve(spa, http.MethodGet, "/")
	if rec.Code != 200 || rec.Body.String() != "<html>app</html>" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	rec = serve(spa, http.MethodGet, "/devices/123")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("fallback: %d", rec.Code)
	}
	rec = serve(spa, http.MethodGet, "/assets/index-abc12.js")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") ||
		!strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec = serve(spa, http.MethodGet, "/assets/missing.js"); rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
	if rec = serve(spa, http.MethodGet, "/api-docs.html"); rec.Code != 404 {
		t.Fatalf("api-docs.html is served at /api/v1/docs only: %d", rec.Code)
	}
	if rec = serve(spa, http.MethodGet, "/robots.txt"); rec.Code != 404 {
		t.Fatalf("missing file with extension: %d", rec.Code)
	}
	if rec = serve(spa, http.MethodGet, "/../../etc/passwd"); rec.Code == 200 && !strings.Contains(rec.Body.String(), "app") {
		t.Fatal("path traversal")
	}
	if rec = serve(spa, http.MethodPost, "/"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", rec.Code)
	}
	if rec = serve(spa, http.MethodGet, "/assets"); rec.Code != 404 {
		t.Fatalf("directory: %d", rec.Code)
	}
}

func TestSPAPlaceholder(t *testing.T) {
	spa := NewSPA(fstest.MapFS{".gitkeep": {}})
	if spa.Built() {
		t.Fatal("placeholder reported as built")
	}
	rec := serve(spa, http.MethodGet, "/anything")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "built without the web UI") {
		t.Fatalf("placeholder: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(true)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := serve(h, http.MethodGet, "/")
	want := map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Strict-Transport-Security": "max-age=31536000",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q", k, got)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		strings.Contains(csp, "script-src 'self' 'unsafe") {
		t.Errorf("csp = %q", csp)
	}
	rec = serve(SecurityHeaders(false)(http.NotFoundHandler()), http.MethodGet, "/")
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must be off for http")
	}
}
