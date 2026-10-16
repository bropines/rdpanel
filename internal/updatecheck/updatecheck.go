// Package updatecheck looks up the latest rustdesk-panel release on GitHub for
// the About page. Answers are cached (6 h by default; failures are retried
// after 30 min while the last good answer is kept), so the panel calls the
// GitHub API at most a few times a day no matter how often the page is opened.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the GitHub "latest release" endpoint of this project.
const DefaultURL = "https://api.github.com/repos/bropines/rustdesk-panel/releases/latest"

// Defaults of a Checker.
const (
	DefaultTTL   = 6 * time.Hour
	DefaultRetry = 30 * time.Minute
	fetchTimeout = 5 * time.Second
	maxBody      = 1 << 20
)

// Release is a published GitHub release.
type Release struct {
	Version     string // tag name, e.g. "v0.2.0"
	URL         string // release page
	PublishedAt time.Time
}

// Result is the cached state of the check.
type Result struct {
	// Latest is nil until a check succeeded (or when the repository has no
	// published release yet).
	Latest *Release
	// CheckedAt is the time of the last attempt.
	CheckedAt time.Time
	// Err is the last attempt's error ("" when it succeeded).
	Err string
}

// Checker fetches and caches the latest release. The zero value is not usable;
// use New.
type Checker struct {
	URL       string
	UserAgent string
	Client    *http.Client
	TTL       time.Duration // cache lifetime of a successful answer
	Retry     time.Duration // delay before retrying after a failure
	Now       func() time.Time

	mu   sync.Mutex
	res  Result
	next time.Time // no new request before this time
}

// New returns a Checker for DefaultURL. userAgent identifies the panel to
// GitHub (its API rejects requests without one).
func New(userAgent string) *Checker {
	return &Checker{URL: DefaultURL, UserAgent: userAgent, Client: &http.Client{Timeout: fetchTimeout},
		TTL: DefaultTTL, Retry: DefaultRetry, Now: time.Now}
}

// Latest returns the cached answer, refreshing it first when it is stale.
// Concurrent callers wait for one in-flight request (bounded by a 5 s
// timeout); a cancelled caller does not abort the shared refresh.
func (c *Checker) Latest(ctx context.Context) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.Now()
	if !c.next.IsZero() && now.Before(c.next) {
		return c.res
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	rel, err := c.fetch(ctx)
	c.res.CheckedAt = now
	if err != nil {
		c.res.Err = err.Error()
		c.next = now.Add(c.Retry)
		return c.res
	}
	c.res.Latest, c.res.Err = rel, ""
	c.next = now.Add(c.TTL)
	return c.res
}

func (c *Checker) fetch(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update check: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil // no published release yet
	case resp.StatusCode != http.StatusOK:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil, fmt.Errorf("update check: GitHub answered %s", resp.Status)
	}
	var body struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("update check: decode: %w", err)
	}
	if body.TagName == "" {
		return nil, errors.New("update check: release without a tag")
	}
	if !strings.HasPrefix(body.HTMLURL, "https://github.com/") {
		body.HTMLURL = ""
	}
	return &Release{Version: body.TagName, URL: body.HTMLURL, PublishedAt: body.PublishedAt.UTC()}, nil
}

// Status compares a running version with the latest release.
type Status string

// Status values.
const (
	StatusUnknown         Status = "unknown"
	StatusUpToDate        Status = "up_to_date"
	StatusUpdateAvailable Status = "update_available"
)

// Compare reports whether latest is newer than current. Both are semantic
// versions with an optional "v" prefix; current may carry a `git describe`
// suffix ("v0.1.1-3-gabc1234-dirty"), which counts as that tag. Anything else
// (e.g. "dev") is StatusUnknown.
func Compare(current, latest string) Status {
	cur, ok1 := parse(current)
	lat, ok2 := parse(latest)
	if !ok1 || !ok2 {
		return StatusUnknown
	}
	if cmp(lat, cur) > 0 {
		return StatusUpdateAvailable
	}
	return StatusUpToDate
}

type semver struct {
	n   [3]int
	pre string
}

var (
	describeRe = regexp.MustCompile(`-[0-9]+-g[0-9a-f]{4,40}$`)
	semverRe   = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
)

func parse(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "-dirty")
	s = describeRe.ReplaceAllString(s, "")
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := range v.n {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		v.n[i] = n
	}
	v.pre = m[4]
	return v, true
}

func cmp(a, b semver) int {
	for i := range a.n {
		if a.n[i] != b.n[i] {
			if a.n[i] > b.n[i] {
				return 1
			}
			return -1
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "": // a release is newer than its pre-releases
		return 1
	case b.pre == "":
		return -1
	}
	return strings.Compare(a.pre, b.pre)
}
