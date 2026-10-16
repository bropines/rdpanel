// Package web serves the embedded single-page app and sets security headers.
package web

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// SPA serves static files from fsys with an index.html fallback for client
// routes. Hashed assets under /assets/ are cached immutably; index.html is
// never cached so deployments take effect immediately.
type SPA struct {
	fsys  fs.FS
	index []byte
}

// NewSPA builds the handler. When fsys has no index.html (UI not built), a
// placeholder page is served instead.
func NewSPA(fsys fs.FS) *SPA {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		index = []byte(placeholder)
	}
	return &SPA{fsys: fsys, index: index}
}

// Built reports whether a real UI build is embedded.
func (s *SPA) Built() bool { return !bytes.Equal(s.index, []byte(placeholder)) }

// ServeHTTP implements http.Handler.
func (s *SPA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "index.html" {
		s.serveIndex(w, r)
		return
	}
	f, err := s.fsys.Open(name)
	if err == nil {
		defer f.Close()
		st, statErr := f.Stat()
		if statErr == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=3600")
			}
			if rs, ok := f.(io.ReadSeeker); ok {
				http.ServeContent(w, r, st.Name(), time.Time{}, rs)
				return
			}
		}
	}
	// Missing files that look like assets are real 404s; everything else is a
	// client-side route handled by the SPA router.
	if name == "assets" || strings.HasPrefix(name, "assets/") || path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	s.serveIndex(w, r)
}

func (s *SPA) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(s.index))
}

const placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>RD Panel</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>RD Panel</h1>
<p>The API server is running, but this binary was built without the web UI.</p>
<p>Build it with <code>make web build</code> (or <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code>, then rebuild the Go binary).</p>
</body></html>
`
