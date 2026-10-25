package adminapi

// Server share links (RDPANEL_SHARE_LINKS_ENABLED, off by default): public
// pages at <public_url>/s/<token> that give anyone with the link the official
// RustDesk client set up for this server and its connection settings
// (share_public.go). Admins may always create links; regular users only when
// their user group has can_share_links. Admins see and revoke every link,
// users their own.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// Admin-audit actions of share links.
const (
	ActionShareLinkCreate = "share_link.create"
	ActionShareLinkRevoke = "share_link.revoke"
)

// SharePath is where the public share pages are mounted (<base>/s/<token>).
const SharePath = "/s"

// Panel settings of the public share page (plain text, rendered escaped).
const (
	// SettingShareLinksEnabled turns the feature on. Unset, the configured
	// RDPANEL_SHARE_LINKS_ENABLED decides, so an existing deployment keeps
	// whatever it had until someone touches the switch in the panel.
	SettingShareLinksEnabled = "share_links.enabled"
	SettingSharePageTitle    = "share.page_title"
	SettingShareDescription  = "share.description"
	maxSharePageTitle        = 80
	maxShareDescription      = 500
)

const (
	// shareTokenBytes is the entropy of a share link token (128 bits).
	shareTokenBytes = 16
	// shareTokenLen is its unpadded base64url length.
	shareTokenLen       = 22
	maxShareLinkLabel   = 64
	maxShareDownloads   = 100000
	maxShareLinkFuture  = 366 * 24 * time.Hour
	minShareLinkFuture  = time.Minute
	shareLinkListLimit  = 500
	shareLinksScopeMine = "mine"
	shareLinksScopeAll  = "all"
)

// shareLinkDurations are the accepted ShareLinkInput.expires_in presets.
var shareLinkDurations = map[string]time.Duration{
	"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

// shareLinkDTO is components.schemas.ShareLink.
type shareLinkDTO struct {
	ID           int64      `json:"id"`
	Label        string     `json:"label"`
	URL          *string    `json:"url"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expires_at"`
	MaxDownloads *int       `json:"max_downloads"`
	Views        int64      `json:"views"`
	Downloads    int64      `json:"downloads"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Owner        userRefDTO `json:"owner"`
}

// shareLinkInput is components.schemas.ShareLinkInput.
type shareLinkInput struct {
	Label        *string    `json:"label"`
	ExpiresIn    *string    `json:"expires_in"`
	ExpiresAt    *time.Time `json:"expires_at"`
	MaxDownloads *int       `json:"max_downloads"`
}

func (c *b2bCore) registerShareLinks(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireUser, c.requireShare)
		r.Get("/share-links", c.handleListShareLinks)
		r.Post("/share-links", c.handleCreateShareLink)
		r.Delete("/share-links/{link_id}", c.handleRevokeShareLink)
	})
}

// shareLinksEnabled reports whether the feature is on: the panel setting when
// it has been set, otherwise RDPANEL_SHARE_LINKS_ENABLED. A store that cannot
// be read leaves the configured value in charge rather than opening the
// feature on its own.
func shareLinksEnabled(ctx context.Context, d Deps) bool {
	cfg := d.Config != nil && d.Config.ShareLinks.Enabled
	if d.Store == nil || d.Store.Settings == nil {
		return cfg
	}
	on, err := store.GetOr(ctx, d.Store.Settings, SettingShareLinksEnabled, cfg)
	if err != nil {
		return cfg
	}
	return on
}

// canShareLinks reports whether user may create share links (feature on, and
// an admin or in a user group with can_share_links).
func canShareLinks(ctx context.Context, d Deps, user *store.User) (bool, error) {
	if user == nil || !shareLinksEnabled(ctx, d) {
		return false, nil
	}
	if user.IsAdmin() {
		return true, nil
	}
	return d.Store.ServerShareLinks.CanShare(ctx, user.ID)
}

// requireShare answers 404 when share links are off and 403 when the caller
// may not share.
func (c *b2bCore) requireShare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shareLinksEnabled(r.Context(), c.d) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "share links are turned off")
			return
		}
		ok, err := canShareLinks(r.Context(), c.d, CurrentUser(r.Context()))
		if err != nil {
			c.fail(w, r, "share links permission", err)
			return
		}
		if !ok {
			forbidden(w, "your user group may not share server links")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// shareBaseURL is the public base the links point to: RDPANEL_PUBLIC_URL, or
// the request's own origin when it is unset.
func (c *b2bCore) shareBaseURL(r *http.Request) string {
	return strings.TrimRight(ClientServerSettings(c.d.Config, r).API, "/")
}

func (c *b2bCore) newShareLinkDTO(r *http.Request, l *store.ServerShareLink) shareLinkDTO {
	out := shareLinkDTO{ID: l.ID, Label: l.Label, Status: string(l.Status(c.d.Store.Now())), ExpiresAt: utcPtr(l.ExpiresAt),
		MaxDownloads: l.MaxDownloads, Views: l.Views, Downloads: l.Downloads, LastUsedAt: utcPtr(l.LastUsedAt),
		CreatedAt: l.CreatedAt.UTC(), Owner: userRefDTO{ID: l.UserID, Username: l.Username, DisplayName: l.DisplayName}}
	if c.d.AEAD != nil && len(l.TokenEnc) > 0 {
		if tok, err := c.d.AEAD.OpenString(l.TokenEnc, crypto.ServerShareLinkTokenAD(l.ID)); err == nil {
			u := c.shareBaseURL(r) + SharePath + "/" + tok
			out.URL = &u
		}
	}
	return out
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (c *b2bCore) handleListShareLinks(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	scope := q.enum("scope", shareLinksScopeMine, shareLinksScopeAll)
	if !q.ok(w) {
		return
	}
	user := CurrentUser(r.Context())
	owner := &user.ID
	if scope == shareLinksScopeAll {
		if !user.IsAdmin() {
			forbidden(w, "administrator role required")
			return
		}
		owner = nil
	}
	links, err := c.d.Store.ServerShareLinks.List(r.Context(), owner, shareLinkListLimit)
	if err != nil {
		c.fail(w, r, "list share links", err)
		return
	}
	items := make([]shareLinkDTO, 0, len(links))
	for _, l := range links {
		items = append(items, c.newShareLinkDTO(r, l))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// validLabel accepts printable text without control characters.
func validLabel(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (c *b2bCore) handleCreateShareLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in shareLinkInput
	if !decodeJSON(w, r, &in) {
		return
	}
	now := c.d.Store.Now()
	f := map[string]string{}
	label := ""
	if in.Label != nil {
		label = strings.TrimSpace(*in.Label)
		if runes(label) > maxShareLinkLabel || !validLabel(label) {
			f["label"] = "must be at most 64 characters of text"
		}
	}
	var expires *time.Time
	switch {
	case in.ExpiresIn != nil && in.ExpiresAt != nil:
		f["expires_at"] = "give either expires_in or expires_at"
	case in.ExpiresIn != nil:
		d, ok := shareLinkDurations[*in.ExpiresIn]
		if !ok {
			f["expires_in"] = "must be one of 1h, 24h, 7d, 30d"
		}
		t := now.Add(d)
		expires = &t
	case in.ExpiresAt != nil:
		t := in.ExpiresAt.UTC()
		if t.Before(now.Add(minShareLinkFuture)) || t.After(now.Add(maxShareLinkFuture)) {
			f["expires_at"] = "must be between one minute and one year from now"
		}
		expires = &t
	}
	if in.MaxDownloads != nil && (*in.MaxDownloads < 1 || *in.MaxDownloads > maxShareDownloads) {
		f["max_downloads"] = "must be between 1 and 100000"
	}
	if len(f) > 0 {
		validationFailed(w, "invalid share link", f)
		return
	}
	user := CurrentUser(ctx)
	n, err := c.d.Store.ServerShareLinks.CountOpen(ctx, user.ID)
	if err != nil {
		c.fail(w, r, "create share link", err)
		return
	}
	if n >= store.MaxServerShareLinksPerUser {
		conflict(w, "share_link_limit", "too many open share links; revoke some first")
		return
	}
	token, err := crypto.RandomToken(shareTokenBytes)
	if err != nil {
		c.fail(w, r, "create share link", err)
		return
	}
	l := &store.ServerShareLink{UserID: user.ID, Label: label, TokenSHA256: crypto.HashToken(token),
		ExpiresAt: expires, MaxDownloads: in.MaxDownloads}
	err = c.d.Store.Tx(ctx, func(tx *store.Store) error {
		if err := tx.ServerShareLinks.Create(ctx, l); err != nil {
			return err
		}
		if c.d.AEAD == nil {
			return nil
		}
		enc, err := c.d.AEAD.SealString(token, crypto.ServerShareLinkTokenAD(l.ID))
		if err != nil {
			return err
		}
		return tx.ServerShareLinks.SetTokenEnc(ctx, l.ID, enc)
	})
	if err != nil {
		c.fail(w, r, "create share link", err)
		return
	}
	row, err := c.d.Store.ServerShareLinks.Get(ctx, l.ID)
	if err != nil {
		c.fail(w, r, "create share link", err)
		return
	}
	details := map[string]any{"label": label, "expires_at": nil, "max_downloads": nil}
	if expires != nil {
		details["expires_at"] = expires.UTC().Format(time.RFC3339)
	}
	if in.MaxDownloads != nil {
		details["max_downloads"] = *in.MaxDownloads
	}
	c.audit(r, ActionShareLinkCreate, "share_link", l.ID, details)
	out := c.newShareLinkDTO(r, row)
	if out.URL == nil { // no AEAD: the link is shown only now
		u := c.shareBaseURL(r) + SharePath + "/" + token
		out.URL = &u
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (c *b2bCore) handleRevokeShareLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "link_id")
	if !ok {
		return
	}
	user := CurrentUser(ctx)
	l, err := c.d.Store.ServerShareLinks.Get(ctx, id)
	if err == nil && l.RevokedAt != nil {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && !user.IsAdmin() && l.UserID != user.ID) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such share link")
		return
	}
	if err == nil {
		err = c.d.Store.ServerShareLinks.Revoke(ctx, id)
	}
	if err != nil {
		c.fail(w, r, "revoke share link", err)
		return
	}
	c.audit(r, ActionShareLinkRevoke, "share_link", id, map[string]any{"label": l.Label, "owner": l.Username,
		"views": l.Views, "downloads": l.Downloads})
	w.WriteHeader(http.StatusNoContent)
}
