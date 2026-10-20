// Package clientdl offers the official RustDesk clients set up for this
// server. It looks up a RustDesk release on GitHub (cached like
// updatecheck: 6 h, failures retried after 30 min while the last good answer
// is kept) and keeps verified copies of the Windows installers, which the
// panel serves under a file name that carries the server settings.
package clientdl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Defaults of a Service.
const (
	DefaultAPI   = "https://api.github.com"
	DefaultWeb   = "https://github.com"
	DefaultRepo  = "rustdesk/rustdesk"
	DefaultTTL   = 6 * time.Hour
	DefaultRetry = 30 * time.Minute
	// MaxAssetSize bounds a downloaded installer.
	MaxAssetSize = 300 << 20

	apiTimeout      = 10 * time.Second
	downloadTimeout = 10 * time.Minute
	maxBody         = 4 << 20
)

// VersionRe matches the RustDesk release tags this package accepts ("1.4.2").
var VersionRe = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}$`)

// ErrUnavailable is returned when no release (or no such asset) is known.
var ErrUnavailable = errors.New("client download unavailable")

// Asset is one downloadable client of a release.
type Asset struct {
	// ID names the platform, e.g. "windows-x64" (see Platforms).
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// URL is GitHub's download URL of the asset.
	URL string `json:"url"`
	// SHA256 is GitHub's digest of the asset (hex); "" when GitHub has none.
	SHA256 string `json:"sha256,omitempty"`
}

// Release is a RustDesk release with the assets of known platforms.
type Release struct {
	Version     string
	URL         string
	PublishedAt time.Time
	Assets      []Asset // in Platforms order
}

// Asset returns the asset of a platform.
func (r *Release) Asset(id string) (Asset, bool) {
	if r != nil {
		for _, a := range r.Assets {
			if a.ID == id {
				return a, true
			}
		}
	}
	return Asset{}, false
}

// Result is the cached state of the release lookup.
type Result struct {
	// Release is nil until a lookup succeeded.
	Release   *Release
	CheckedAt time.Time
	// Err is the last attempt's error ("" when it succeeded).
	Err string
}

// Platforms are the assets offered, by ID, with their file name patterns
// (%s: the version). Official release names since RustDesk 1.2.
var Platforms = []struct{ ID, Pattern string }{
	{"windows-x64", `rustdesk-%s-x86_64\.exe`},
	{"windows-x64-msi", `rustdesk-%s-x86_64\.msi`},
	{"windows-arm64", `rustdesk-%s-aarch64\.exe`},
	{"windows-arm64-msi", `rustdesk-%s-aarch64\.msi`},
	{"macos-arm64", `rustdesk-%s-aarch64\.dmg`},
	{"macos-x64", `rustdesk-%s-x86_64\.dmg`},
	{"linux-deb-x64", `rustdesk-%s-x86_64\.deb`},
	{"linux-deb-arm64", `rustdesk-%s-aarch64\.deb`},
	{"linux-rpm-x64", `rustdesk-%s(-0)?\.x86_64\.rpm`},
	{"linux-rpm-arm64", `rustdesk-%s(-0)?\.aarch64\.rpm`},
	{"linux-appimage-x64", `rustdesk-%s-x86_64\.AppImage`},
	{"linux-appimage-arm64", `rustdesk-%s-aarch64\.AppImage`},
	{"android-universal", `rustdesk-%s-universal-signed\.apk`},
	{"android-arm64", `rustdesk-%s-aarch64-signed\.apk`},
	{"android-armv7", `rustdesk-%s-armv7-signed\.apk`},
	{"android-x64", `rustdesk-%s-x86_64-signed\.apk`},
}

// Service looks up releases and keeps verified installer copies. The zero
// value is not usable; use New.
type Service struct {
	API       string // GitHub API base URL
	Web       string // GitHub base URL: assets must come from <Web>/<Repo>/releases/download/
	Repo      string // owner/name
	Version   string // pinned release tag; "" = the latest release
	UserAgent string
	Client    *http.Client // API requests
	Download  *http.Client // asset downloads
	// Dir keeps downloaded installers (<Dir>/<version>/<asset name>).
	Dir   string
	TTL   time.Duration
	Retry time.Duration
	Now   func() time.Time

	mu   sync.Mutex
	res  Result
	next time.Time

	dlMu sync.Mutex // one download at a time
}

// New returns a Service for the official repository. dir keeps installer
// copies; version pins a release ("" = latest).
func New(dir, version, userAgent string) *Service {
	return &Service{
		API: DefaultAPI, Web: DefaultWeb, Repo: DefaultRepo, Version: version, UserAgent: userAgent,
		Client:   &http.Client{Timeout: apiTimeout},
		Download: &http.Client{Timeout: downloadTimeout},
		Dir:      dir, TTL: DefaultTTL, Retry: DefaultRetry, Now: time.Now,
	}
}

// Release returns the cached lookup, refreshing it first when it is stale.
// Concurrent callers wait for one in-flight request; a cancelled caller
// does not abort the shared refresh.
func (s *Service) Release(ctx context.Context) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if !s.next.IsZero() && now.Before(s.next) {
		return s.res
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), apiTimeout)
	defer cancel()
	rel, err := s.fetch(ctx)
	s.res.CheckedAt = now
	if err != nil {
		s.res.Err = err.Error()
		s.next = now.Add(s.Retry)
		return s.res
	}
	s.res.Release, s.res.Err = rel, ""
	s.next = now.Add(s.TTL)
	return s.res
}

func (s *Service) fetch(ctx context.Context) (*Release, error) {
	u := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(s.API, "/"), s.Repo)
	if s.Version != "" {
		u = fmt.Sprintf("%s/repos/%s/releases/tags/%s", strings.TrimRight(s.API, "/"), s.Repo, s.Version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("RustDesk release lookup: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil, fmt.Errorf("RustDesk release lookup: GitHub answered %s", resp.Status)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("RustDesk release lookup: decode: %w", err)
	}
	if !VersionRe.MatchString(body.TagName) {
		return nil, fmt.Errorf("RustDesk release lookup: unexpected tag %q", body.TagName)
	}
	rel := &Release{Version: body.TagName, PublishedAt: body.PublishedAt.UTC()}
	web := strings.TrimRight(s.Web, "/") + "/"
	if strings.HasPrefix(body.HTMLURL, web) {
		rel.URL = body.HTMLURL
	}
	prefix := web + s.Repo + "/releases/download/"
	for _, p := range Platforms {
		re := regexp.MustCompile("^" + fmt.Sprintf(p.Pattern, regexp.QuoteMeta(rel.Version)) + "$")
		for _, a := range body.Assets {
			if !re.MatchString(a.Name) || !strings.HasPrefix(a.URL, prefix) || a.Size <= 0 {
				continue
			}
			sum := ""
			if d, ok := strings.CutPrefix(a.Digest, "sha256:"); ok && len(d) == 64 {
				if _, err := hex.DecodeString(d); err == nil {
					sum = strings.ToLower(d)
				}
			}
			rel.Assets = append(rel.Assets, Asset{ID: p.ID, Name: a.Name, Size: a.Size, URL: a.URL, SHA256: sum})
			break
		}
	}
	return rel, nil
}

// Open returns a verified local copy of a release asset, downloading it on
// first use (size and, when GitHub publishes one, SHA-256 checked). Copies
// of other versions are removed after a new version is stored.
func (s *Service) Open(ctx context.Context, id string) (*os.File, Asset, error) {
	res := s.Release(ctx)
	a, ok := res.Release.Asset(id)
	if !ok {
		if res.Err != "" {
			return nil, Asset{}, fmt.Errorf("%w: %s", ErrUnavailable, res.Err)
		}
		return nil, Asset{}, ErrUnavailable
	}
	if a.Size > MaxAssetSize {
		return nil, Asset{}, fmt.Errorf("%w: %s is too large", ErrUnavailable, a.Name)
	}
	version := res.Release.Version
	path := filepath.Join(s.Dir, version, a.Name)
	if f, ok := openComplete(path, a.Size); ok {
		return f, a, nil
	}
	s.dlMu.Lock()
	defer s.dlMu.Unlock()
	if f, ok := openComplete(path, a.Size); ok {
		return f, a, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), downloadTimeout)
	defer cancel()
	if err := s.download(ctx, a, path); err != nil {
		return nil, Asset{}, err
	}
	s.prune(version)
	f, ok := openComplete(path, a.Size)
	if !ok {
		return nil, Asset{}, fmt.Errorf("%w: stored copy of %s is missing", ErrUnavailable, a.Name)
	}
	return f, a, nil
}

func openComplete(path string, size int64) (*os.File, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	if st, err := f.Stat(); err != nil || st.Size() != size {
		_ = f.Close()
		return nil, false
	}
	return f, true
}

func (s *Service) download(ctx context.Context, a Asset, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.Download.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: GitHub answered %s", a.Name, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	if n != a.Size {
		return fmt.Errorf("download %s: got %d bytes, GitHub lists %d", a.Name, n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); a.SHA256 != "" && got != a.SHA256 {
		return fmt.Errorf("download %s: SHA-256 %s does not match GitHub's %s", a.Name, got, a.SHA256)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	keep = true
	return nil
}

// prune removes stored copies of other versions.
func (s *Service) prune(keep string) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep && VersionRe.MatchString(e.Name()) {
			_ = os.RemoveAll(filepath.Join(s.Dir, e.Name()))
		}
	}
}
