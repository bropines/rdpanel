package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/store"
)

// limitAPIToken keys failed API token attempts per client IP.
const limitAPIToken = "panel-apitoken-ip:"

// apiTokenTouchEvery throttles the last_used_at/last_used_ip update.
const apiTokenTouchEvery = time.Minute

// maxAPITokenDays bounds expires_in_days.
const maxAPITokenDays = 3650

// bearerFrom returns the token of an "Authorization: Bearer ..." header.
func bearerFrom(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	return auth.BearerToken(h)
}

// loadAPIToken authenticates r by a personal API token. Failures are
// rate-limited per client IP like logins; the token is never logged.
func (m *sessions) loadAPIToken(w http.ResponseWriter, r *http.Request, next http.Handler, plain string) {
	ctx := r.Context()
	key := limitAPIToken + httpx.ClientIPString(r)
	if ok, wait := m.limiter.Allow(key); !ok {
		m.d.Telemetry.APITokenAuth(metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}
	info, err := m.lookupAPIToken(ctx, r, plain)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			m.d.Log.ErrorContext(ctx, "api token lookup", slog.Any("err", err))
			internalError(w)
			return
		}
		m.limiter.Failure(key)
		m.d.Telemetry.APITokenAuth(metrics.ResultFailure)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid, expired or revoked API token")
		return
	}
	m.limiter.Success(key)
	m.d.Telemetry.APITokenAuth(metrics.ResultSuccess)
	if info.APIToken.Scope != store.APITokenWrite && r.Method != http.MethodGet && r.Method != http.MethodHead {
		httpx.WriteError(w, http.StatusForbidden, "insufficient_scope", "this API token is read-only")
		return
	}
	next.ServeHTTP(w, r.WithContext(WithSession(ctx, info)))
}

func (m *sessions) lookupAPIToken(ctx context.Context, r *http.Request, plain string) (*SessionInfo, error) {
	if !auth.IsAPIToken(plain) {
		return nil, store.ErrNotFound
	}
	t, err := m.d.Store.APITokens.GetActive(ctx, crypto.HashToken(plain))
	if err != nil {
		return nil, err
	}
	user, err := m.d.Store.Users.GetByID(ctx, t.UserID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, store.ErrNotFound
	}
	now := m.now()
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= apiTokenTouchEvery {
		if err := m.d.Store.APITokens.Touch(ctx, t.ID, httpx.ClientIPString(r)); err != nil {
			m.d.Log.WarnContext(ctx, "api token touch", slog.Any("err", err))
		}
	}
	info := &SessionInfo{User: user, APIToken: t,
		Token: &store.AuthToken{UserID: user.ID, Kind: "api", CreatedAt: t.CreatedAt, ExpiresAt: now}}
	if info.TOTPEnabled, err = m.totp.enrolled(ctx, user.ID); err != nil {
		return nil, err
	}
	info.MustEnrollTOTP = m.mustEnroll(ctx, user, info.TOTPEnabled)
	return info, nil
}

// ---- token management endpoints ----

// apiTokenDTO is components.schemas.ApiToken (never the secret).
type apiTokenDTO struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP *string    `json:"last_used_ip"`
	Expired    bool       `json:"expired"`
}

func newAPITokenDTO(t *store.APIToken, now time.Time) apiTokenDTO {
	return apiTokenDTO{ID: t.ID, Name: t.Name, Prefix: t.Prefix, Scope: string(t.Scope), CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, LastUsedIP: strPtr(t.LastUsedIP), Expired: t.Expired(now)}
}

// apiTokenCreatedDTO is components.schemas.ApiTokenCreated.
type apiTokenCreatedDTO struct {
	APIToken apiTokenDTO `json:"api_token"`
	// Token is the plain secret, returned exactly once.
	Token string `json:"token"`
}

type createAPITokenRequest struct {
	Name          string `json:"name"`
	Scope         string `json:"scope"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

// registerAPITokens mounts the personal API token routes. Minting and
// revoking need a cookie session: a token cannot create or revoke tokens.
func registerAPITokens(r chi.Router, c *authCore) {
	a := &resources{authCore: c}
	r.Group(func(r chi.Router) {
		r.Use(RequireUser)
		r.Get("/me/api-tokens", a.handleListMyAPITokens)
		r.With(requireCookieSession).Post("/me/api-tokens", a.handleCreateAPIToken)
		r.With(requireCookieSession).Delete("/me/api-tokens/{token_id}", a.handleRevokeMyAPIToken)
	})
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/users/{user_id}/api-tokens", a.handleListUserAPITokens)
		r.With(requireCookieSession).Delete("/users/{user_id}/api-tokens/{token_id}", a.handleRevokeUserAPIToken)
	})
}

func (a *resources) writeAPITokens(w http.ResponseWriter, r *http.Request, userID int64) {
	toks, err := a.d.Store.APITokens.ListForUser(r.Context(), userID)
	if err != nil {
		a.fail500(w, r, "list api tokens", err)
		return
	}
	now := a.d.Store.Now()
	out := make([]apiTokenDTO, 0, len(toks))
	for _, t := range toks {
		out = append(out, newAPITokenDTO(t, now))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[apiTokenDTO]{Items: out})
}

func (a *resources) handleListMyAPITokens(w http.ResponseWriter, r *http.Request) {
	a.writeAPITokens(w, r, CurrentUser(r.Context()).ID)
}

func (a *resources) handleListUserAPITokens(w http.ResponseWriter, r *http.Request) {
	if u := a.loadUser(w, r); u != nil {
		a.writeAPITokens(w, r, u.ID)
	}
}

func (a *resources) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	var req createAPITokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	f := map[string]string{}
	req.Name = strings.TrimSpace(req.Name)
	if n := runes(req.Name); n < 1 || n > 64 {
		f["name"] = "must be 1-64 characters"
	}
	scope := store.APITokenScope(req.Scope)
	if scope != store.APITokenRead && scope != store.APITokenWrite {
		f["scope"] = "must be read or write"
	}
	var expires *time.Time
	if req.ExpiresInDays != nil {
		if d := *req.ExpiresInDays; d < 1 || d > maxAPITokenDays {
			f["expires_in_days"] = "must be between 1 and 3650, or null for no expiry"
		} else {
			t := a.d.Store.Now().AddDate(0, 0, d)
			expires = &t
		}
	}
	if len(f) > 0 {
		validationFailed(w, "invalid API token", f)
		return
	}
	n, err := a.d.Store.APITokens.CountForUser(ctx, user.ID)
	if err != nil {
		a.fail500(w, r, "count api tokens", err)
		return
	}
	if n >= store.MaxAPITokensPerUser {
		conflict(w, "api_token_limit", "too many API tokens; revoke unused ones first")
		return
	}
	tok, err := auth.NewAPIToken()
	if err != nil {
		a.fail500(w, r, "generate api token", err)
		return
	}
	row := &store.APIToken{UserID: user.ID, Name: req.Name, Prefix: tok.Plain[:auth.APITokenDisplayLen],
		TokenSHA256: tok.Hash, Scope: scope, ExpiresAt: expires}
	if err := a.d.Store.APITokens.Create(ctx, row); err != nil {
		a.fail500(w, r, "create api token", err)
		return
	}
	details := map[string]any{"name": row.Name, "scope": string(row.Scope), "prefix": row.Prefix}
	if expires != nil {
		details["expires_at"] = expires.UTC().Format(time.RFC3339)
	}
	a.audit(ctx, r, "api_token.create", "api_token", idStr(row.ID), details)
	httpx.WriteJSON(w, http.StatusCreated, apiTokenCreatedDTO{APIToken: newAPITokenDTO(row, a.d.Store.Now()),
		Token: tok.Plain})
}

// revokeAPIToken revokes token_id when it belongs to owner (404 otherwise).
func (a *resources) revokeAPIToken(w http.ResponseWriter, r *http.Request, owner *store.User, by string) {
	ctx := r.Context()
	id, ok := pathID(w, r, "token_id")
	if !ok {
		return
	}
	t, err := a.d.Store.APITokens.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (t.UserID != owner.ID || t.RevokedAt != nil)) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "load api token", err)
		return
	}
	if err := a.d.Store.APITokens.Revoke(ctx, id); err != nil {
		a.writeStoreErr(w, r, "revoke api token", err)
		return
	}
	a.audit(ctx, r, "api_token.revoke", "api_token", idStr(id), map[string]any{"name": t.Name,
		"prefix": t.Prefix, "user_id": owner.ID, "username": owner.Username, "by": by})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleRevokeMyAPIToken(w http.ResponseWriter, r *http.Request) {
	a.revokeAPIToken(w, r, CurrentUser(r.Context()), "user")
}

func (a *resources) handleRevokeUserAPIToken(w http.ResponseWriter, r *http.Request) {
	if u := a.loadUser(w, r); u != nil {
		a.revokeAPIToken(w, r, u, "admin")
	}
}
