package adminapi

// Public server share pages (/s/*, outside /api/v1/admin, no session):
//
//	GET /s/{token}                the page (web/share.html, a Vite entry)
//	GET /s/{token}/info           its data: connection settings and downloads
//	GET /s/{token}/windows/{arch} the preconfigured Windows installer
//	GET /s/assets/*, /s/favicon.svg  the page's static files
//
// Nothing but this server's connection settings and the official clients is
// exposed: no owner, label, counters or expiry. Unknown tokens count towards
// a per-IP backoff (auth.Limiter), every route is rate limited per IP, and
// responses are never cached or indexed. With RDPANEL_SHARE_LINKS_ENABLED
// off every path answers 404.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/clientdl"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

const (
	// sharePage is the Vite entry built next to index.html (web/share.html).
	sharePage          = "share.html"
	shareAssetsPath    = SharePath + "/assets/"
	shareFaviconPath   = SharePath + "/favicon.svg"
	shareLimitKey      = "share-ip:"
	sharePageRate      = 1.0 // page and info requests per second and IP
	sharePageBurst     = 30
	shareDownloadRate  = 1.0 / 10 // installer requests per second and IP
	shareDownloadBurst = 10
)

// sharePublicInfoDTO is components.schemas.SharePublicInfo.
type sharePublicInfoDTO struct {
	Title           string            `json:"title"`
	Description     string            `json:"description"`
	IDServer        string            `json:"id_server"`
	RelayServer     string            `json:"relay_server"`
	APIServer       string            `json:"api_server"`
	PublicKey       string            `json:"public_key"`
	ConfigString    string            `json:"config_string"`
	WindowsFileName string            `json:"windows_file_name,omitempty"`
	Windows         []shareWindowsDTO `json:"windows"`
	Version         string            `json:"version,omitempty"`
	ReleaseURL      string            `json:"release_url,omitempty"`
	Assets          []clientdl.Asset  `json:"assets"`
}

// shareWindowsDTO is one preconfigured Windows installer of SharePublicInfo.
type shareWindowsDTO struct {
	Arch string `json:"arch"`
	Size int64  `json:"size"`
}

type sharePublic struct {
	d         Deps
	log       *slog.Logger
	webFS     fs.FS
	pages     *httpx.RateLimiter
	downloads *httpx.RateLimiter

	htmlOnce sync.Once
	html     []byte
}

// RegisterSharePublic mounts the public share pages at /s on r (the root
// router, next to /api). webFS is the built SPA (web/dist).
func RegisterSharePublic(r chi.Router, d Deps, webFS fs.FS) {
	h := &sharePublic{d: d, log: d.Log, webFS: webFS,
		pages:     httpx.NewRateLimiter(sharePageRate, sharePageBurst, nil),
		downloads: httpx.NewRateLimiter(shareDownloadRate, shareDownloadBurst, nil)}
	if h.log == nil {
		h.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	r.Route(SharePath, func(r chi.Router) {
		r.Use(h.gate)
		r.NotFound(httpx.NotFound)
		r.MethodNotAllowed(httpx.MethodNotAllowed)
		r.Get("/assets/*", h.asset)
		r.Get("/favicon.svg", h.favicon)
		r.Group(func(r chi.Router) {
			r.Use(httpx.RateLimit(h.pages, "5", tooManyShareRequests))
			r.Get("/{token}", h.page)
			r.Get("/{token}/info", h.info)
		})
		r.With(httpx.RateLimit(h.downloads, "30", tooManyShareRequests)).Get("/{token}/windows/{arch}", h.windows)
	})
}

func tooManyShareRequests(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; try again shortly")
}

// gate answers 404 while share links are off and sets the headers of every
// public share response.
func (h *sharePublic) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shareLinksEnabled(r.Context(), h.d) {
			httpx.NotFound(w, r)
			return
		}
		hd := w.Header()
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
		hd.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// validShareToken reports whether s looks like a share token (22 base64url
// characters); anything else is not looked up.
func validShareToken(s string) bool {
	if len(s) != shareTokenLen {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// lookup resolves the {token} of r. It returns the link and its status, or
// the HTTP status to answer instead: 404 (unknown token, counted towards the
// caller's backoff), 429 (backoff) or 500.
func (h *sharePublic) lookup(r *http.Request) (*store.ServerShareLink, store.ShareLinkStatus, int, time.Duration) {
	key := shareLimitKey + httpx.ClientIPString(r)
	if h.d.Limiter != nil {
		if ok, wait := h.d.Limiter.Allow(key); !ok {
			return nil, "", http.StatusTooManyRequests, wait
		}
	}
	tok := chi.URLParam(r, "token")
	var (
		l   *store.ServerShareLink
		err = store.ErrNotFound
	)
	if validShareToken(tok) {
		l, err = h.d.Store.ServerShareLinks.GetByHash(r.Context(), crypto.HashToken(tok))
	}
	if errors.Is(err, store.ErrNotFound) {
		if h.d.Limiter != nil {
			h.d.Limiter.Failure(key)
		}
		return nil, "", http.StatusNotFound, 0
	}
	if err != nil {
		h.log.ErrorContext(r.Context(), "share link lookup", slog.Any("err", err))
		return nil, "", http.StatusInternalServerError, 0
	}
	return l, l.Status(h.d.Store.Now()), 0, 0
}

// page serves the share page; for unknown and invalid links with 404 / 410
// (the page then shows "This link is no longer valid").
func (h *sharePublic) page(w http.ResponseWriter, r *http.Request) {
	l, st, code, wait := h.lookup(r)
	switch {
	case code == http.StatusTooManyRequests:
		rateLimited(w, wait)
		return
	case code == http.StatusInternalServerError:
		internalError(w)
		return
	case code == 0 && st == store.ShareLinkActive:
		if err := h.d.Store.ServerShareLinks.RecordView(r.Context(), l.ID); err != nil {
			h.log.WarnContext(r.Context(), "share link view", slog.Any("err", err))
		}
		code = http.StatusOK
	case code == 0:
		code = http.StatusGone
	}
	html := h.pageHTML()
	if html == nil {
		http.Error(w, "share page UI not built (run `make web build`)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(html)
}

// pageHTML is web/share.html with its absolute /assets/ and favicon URLs moved
// under /s/, so the page needs nothing outside /s/ (nil when not built).
func (h *sharePublic) pageHTML() []byte {
	h.htmlOnce.Do(func() {
		if h.webFS == nil {
			return
		}
		b, err := fs.ReadFile(h.webFS, sharePage)
		if err != nil {
			return
		}
		b = bytes.ReplaceAll(b, []byte(`"/assets/`), []byte(`"`+shareAssetsPath))
		b = bytes.ReplaceAll(b, []byte(`"/favicon.svg"`), []byte(`"`+shareFaviconPath+`"`))
		h.html = b
	})
	return h.html
}

func (h *sharePublic) info(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, st, code, wait := h.lookup(r)
	switch {
	case code == http.StatusTooManyRequests:
		rateLimited(w, wait)
		return
	case code == http.StatusInternalServerError:
		internalError(w)
		return
	case code == http.StatusNotFound:
		httpx.WriteError(w, http.StatusNotFound, "not_found", "this link is not valid")
		return
	case st != store.ShareLinkActive:
		httpx.WriteError(w, http.StatusGone, "gone", "this link is no longer valid")
		return
	}
	cs := ClientServerSettings(h.d.Config, r)
	cfg, _ := EncodeRustdeskConfig(cs.Host, cs.Relay, cs.API, cs.Key)
	out := sharePublicInfoDTO{IDServer: cs.Host, RelayServer: cs.Relay, APIServer: cs.API, PublicKey: cs.Key,
		ConfigString: cfg, Windows: []shareWindowsDTO{}, Assets: []clientdl.Asset{}}
	var err error
	if out.Title, err = store.GetOr(ctx, h.d.Store.Settings, SettingSharePageTitle, ""); err == nil {
		out.Description, err = store.GetOr(ctx, h.d.Store.Settings, SettingShareDescription, "")
	}
	if err != nil {
		h.log.ErrorContext(ctx, "share page settings", slog.Any("err", err))
		internalError(w)
		return
	}
	if h.d.Clients != nil {
		res := h.d.Clients.Release(ctx)
		if rel := res.Release; rel != nil {
			out.Version, out.ReleaseURL = rel.Version, rel.URL
			for _, a := range rel.Assets {
				if !strings.HasPrefix(a.ID, "windows-") {
					out.Assets = append(out.Assets, a)
				}
			}
			if name, err := clientdl.WindowsFileName(cs); err == nil && cs.Key != "" {
				for _, arch := range []string{"x64", "arm64"} {
					if a, ok := rel.Asset(windowsInstallers[arch]); ok {
						out.Windows = append(out.Windows, shareWindowsDTO{Arch: arch, Size: a.Size})
					}
				}
				if len(out.Windows) > 0 {
					out.WindowsFileName = name
				}
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// windows streams the official installer named so that it uses this server.
// A request that starts a download (no Range, or one from byte 0) takes one
// download of the link; resumed ranges of a link still in use are served
// without counting.
func (h *sharePublic) windows(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := windowsInstallers[chi.URLParam(r, "arch")]
	if !ok || h.d.Clients == nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such client download")
		return
	}
	l, st, code, wait := h.lookup(r)
	switch {
	case code == http.StatusTooManyRequests:
		rateLimited(w, wait)
		return
	case code == http.StatusInternalServerError:
		internalError(w)
		return
	case code == http.StatusNotFound:
		httpx.WriteError(w, http.StatusNotFound, "not_found", "this link is not valid")
		return
	case st != store.ShareLinkActive && st != store.ShareLinkExhausted:
		httpx.WriteError(w, http.StatusGone, "gone", "this link is no longer valid")
		return
	}
	cs := ClientServerSettings(h.d.Config, r)
	name, err := clientdl.WindowsFileName(cs)
	if cs.Key == "" || err != nil {
		httpx.WriteError(w, http.StatusConflict, "no_server_key", "the installer cannot be set up for this server yet")
		return
	}
	f, _, err := h.d.Clients.Open(ctx, id)
	if err != nil {
		h.log.WarnContext(ctx, "share client download failed", "asset", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "client_unavailable", "the RustDesk installer could not be fetched; try again later")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		internalError(w)
		return
	}
	rng := strings.TrimSpace(r.Header.Get("Range"))
	if rng == "" || strings.HasPrefix(rng, "bytes=0-") {
		taken, err := h.d.Store.ServerShareLinks.TakeDownload(ctx, l.ID)
		if err != nil {
			h.log.ErrorContext(ctx, "share link download", slog.Any("err", err))
			internalError(w)
			return
		}
		if !taken {
			httpx.WriteError(w, http.StatusGone, "gone", "this link is no longer valid")
			return
		}
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// asset serves the page's hashed build files (static code and styles).
func (h *sharePublic) asset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean("/" + strings.TrimPrefix(r.URL.Path, shareAssetsPath))
	h.serveStatic(w, r, "assets"+name, "public, max-age=31536000, immutable")
}

func (h *sharePublic) favicon(w http.ResponseWriter, r *http.Request) {
	h.serveStatic(w, r, "favicon.svg", "public, max-age=86400")
}

func (h *sharePublic) serveStatic(w http.ResponseWriter, r *http.Request, name, cache string) {
	if h.webFS == nil {
		httpx.NotFound(w, r)
		return
	}
	f, err := h.webFS.Open(name)
	if err != nil {
		httpx.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	rs, ok := f.(io.ReadSeeker)
	if err != nil || st.IsDir() || !ok {
		httpx.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", cache)
	http.ServeContent(w, r, st.Name(), time.Time{}, rs)
}
