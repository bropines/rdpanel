package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/bropines/rdpanel/api"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/httpx"
)

// API docs routes (under /api: the operator's reverse proxy decides where they
// are reachable, e.g. a public vhost that 404s /api/v1/* keeps them internal).
const (
	apiDocsPath       = "/api/v1/docs"
	apiDocsAssetsPath = apiDocsPath + "/assets/"
	// apiDocsPage is the Vite entry built next to index.html (web/api-docs.html).
	apiDocsPage = "api-docs.html"
)

// serverURLLine matches the relative `servers` entries of api/openapi.yaml
// ("- url: /api/v1/admin", "- url: /api/v1").
var serverURLLine = regexp.MustCompile(`(?m)^(\s*- url: )(/api/v1\S*)[ \t]*$`)

// specDoc is one rendering of the contract (YAML as served and its JSON form).
type specDoc struct {
	once       sync.Once
	yaml, json []byte
	err        error
}

func (d *specDoc) load(src []byte) ([]byte, []byte, error) {
	d.once.Do(func() {
		d.yaml = src
		var doc any
		if err := yaml.Unmarshal(src, &doc); err != nil {
			d.err = fmt.Errorf("parse openapi.yaml: %w", err)
			return
		}
		d.json, d.err = json.Marshal(doc)
	})
	return d.yaml, d.json, d.err
}

// apiDocs serves the embedded contract and the Swagger UI page built into the
// SPA bundle (web/api-docs.html).
type apiDocs struct {
	mode  string
	auth  events.Authenticator
	webFS fs.FS
	// adminOrigin/adminHost come from RDPANEL_ADMIN_URL. Requests addressed to
	// that host get absolute server URLs; any other host keeps the relative
	// ones (resolved against wherever the spec was loaded from).
	adminOrigin, adminHost string
	relative, absolute     specDoc
}

func newAPIDocs(cfg *config.Config, auth events.Authenticator, webFS fs.FS) *apiDocs {
	d := &apiDocs{mode: cfg.APIDocs, auth: auth, webFS: webFS}
	if u, err := url.Parse(cfg.AdminURL); err == nil && u.Scheme != "" && u.Host != "" {
		d.adminOrigin, d.adminHost = u.Scheme+"://"+u.Host, u.Host
	}
	return d
}

// register mounts the routes on the /api router; mode "off" mounts nothing (404).
func (d *apiDocs) register(r chi.Router) {
	if d.mode == config.APIDocsOff {
		return
	}
	r.Get("/v1/openapi.yaml", d.spec(false))
	r.Get("/v1/openapi.json", d.spec(true))
	r.Get("/v1/docs", d.page)
	r.Get("/v1/docs/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, apiDocsPath, http.StatusMovedPermanently)
	})
	r.Get("/v1/docs/assets/*", d.asset)
}

func (d *apiDocs) signedIn(r *http.Request) bool {
	if d.mode == config.APIDocsPublic {
		return true
	}
	_, err := d.auth.Authenticate(r)
	return err == nil
}

func (d *apiDocs) spec(asJSON bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.signedIn(r) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		doc, src := &d.relative, api.OpenAPIYAML
		if d.adminHost != "" && strings.EqualFold(r.Host, d.adminHost) {
			doc = &d.absolute
			src = serverURLLine.ReplaceAll(api.OpenAPIYAML, []byte("${1}"+d.adminOrigin+"${2}"))
		}
		y, j, err := doc.load(src)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		w.Header().Set("Vary", "Cookie, Authorization")
		if asJSON {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(j)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(y)
	}
}

// page serves the docs page. Without a session, browsers go to the panel login
// (which returns here afterwards) and API clients get 401.
func (d *apiDocs) page(w http.ResponseWriter, r *http.Request) {
	if !d.signedIn(r) {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(apiDocsPath), http.StatusFound)
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	html, err := fs.ReadFile(d.webFS, apiDocsPage)
	if err != nil {
		http.Error(w, "API docs UI not built (run `make web build`)", http.StatusNotFound)
		return
	}
	// Vite emits absolute /assets/ URLs; serve them from under the docs path so
	// the page needs nothing outside /api/v1/.
	html = bytes.ReplaceAll(html, []byte(`"/assets/`), []byte(`"`+apiDocsAssetsPath))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(html)
}

// asset serves hashed build assets (no auth: static library code and styles).
func (d *apiDocs) asset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean("/" + strings.TrimPrefix(r.URL.Path, apiDocsAssetsPath))
	f, err := d.webFS.Open("assets" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	rs, ok := f.(io.ReadSeeker)
	if err != nil || st.IsDir() || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, st.Name(), time.Time{}, rs)
}
