package clientapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// touchInterval throttles last_used_at writes for client tokens.
const touchInterval = time.Minute

var errUnauthorized = errors.New("unauthorized")

type ctxKey int

const sessionKey ctxKey = iota

// session is the authenticated caller of a Bearer endpoint.
type session struct {
	user  *store.User
	token *store.AuthToken
}

func sessionFrom(ctx context.Context) *session {
	s, _ := ctx.Value(sessionKey).(*session)
	return s
}

// requireUser authenticates "Authorization: Bearer <client JWT>". Invalid,
// expired, revoked or disabled-user tokens get 401 {"error":"Unauthorized"},
// which logs the client out; storage failures are 500 (never 400: a 400 from
// /api/currentUser also logs the user out).
func (h *handler) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := h.authenticate(r, true)
		if errors.Is(err, errUnauthorized) {
			writeError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if err != nil {
			h.log.ErrorContext(r.Context(), "client auth", slog.Any("err", err))
			internalError(w)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, s)))
	}
}

// authenticate validates the Bearer token: JWT signature and exp, an active
// auth_tokens row (by sha256) for the same user, and an active user. When
// touch is set it records use and slides the expiry once less than a third of
// the lifetime is left.
func (h *handler) authenticate(r *http.Request, touch bool) (*session, error) {
	raw, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		return nil, errUnauthorized
	}
	claims, err := h.JWT.Verify(raw)
	if err != nil {
		return nil, errUnauthorized
	}
	ctx := r.Context()
	tok, err := h.st.AuthTokens.GetActive(ctx, store.TokenClient, crypto.HashToken(raw))
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if tok.UserID != int64(claims.UserID) {
		return nil, errUnauthorized
	}
	u, err := h.st.Users.GetByID(ctx, tok.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errUnauthorized
	}
	if err != nil {
		return nil, err
	}
	if !u.IsActive() {
		return nil, errUnauthorized
	}
	if touch {
		h.touchToken(ctx, tok)
	}
	return &session{user: u, token: tok}, nil
}

func (h *handler) touchToken(ctx context.Context, tok *store.AuthToken) {
	now := h.Now()
	life := h.tokens.tokenLifetime(ctx)
	var newExp *time.Time
	if tok.ExpiresAt.Sub(now) < life/3 {
		e := now.Add(life)
		newExp = &e
	} else if tok.LastUsedAt != nil && now.Sub(*tok.LastUsedAt) < touchInterval {
		return
	}
	if err := h.st.AuthTokens.Touch(ctx, tok.ID, newExp); err != nil {
		h.log.WarnContext(ctx, "touch client token", slog.Any("err", err))
	}
}

// TokenIssuer mints client tokens: an HS256 JWT {"user_id","exp"} (what hbbs
// verifies) plus an auth_tokens row holding only sha256(token) with a sliding
// expiry. It also records the login event, binds the device to the user and
// stamps last_login_at. Stream B's OIDC auth-query uses it too.
type TokenIssuer struct {
	st        *store.Store
	jwt       *auth.ClientJWT
	log       *slog.Logger
	now       func() time.Time
	tokenLife time.Duration
	jwtLife   time.Duration

	mu       sync.Mutex
	cachedAt time.Time
	cLife    time.Duration
}

// lifetimes returns the effective token and JWT lifetimes (the
// auth.client_token_lifetime_hours setting overrides the configuration).
func (ti *TokenIssuer) lifetimes(ctx context.Context) (token, jwt time.Duration) {
	return ti.st.Settings.ClientTokenLifetimes(ctx, ti.tokenLife, ti.jwtLife)
}

// tokenLifetime is the sliding token lifetime, cached briefly (it is read on
// every authenticated client request).
func (ti *TokenIssuer) tokenLifetime(ctx context.Context) time.Duration {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	if ti.cLife == 0 || time.Since(ti.cachedAt) > 10*time.Second {
		ti.cLife, _ = ti.lifetimes(ctx)
		ti.cachedAt = time.Now()
	}
	return ti.cLife
}

// NewTokenIssuer builds a TokenIssuer from d (Store, JWT, Config, Log, Now).
func NewTokenIssuer(d Deps) *TokenIssuer {
	ti := &TokenIssuer{st: d.Store, jwt: d.JWT, log: d.Log, now: d.Now, tokenLife: 4380 * time.Hour}
	if ti.now == nil {
		ti.now = time.Now
	}
	if d.Config != nil && d.Config.Auth.TokenLifetime > 0 {
		ti.tokenLife = d.Config.Auth.TokenLifetime
	}
	ti.jwtLife = ti.tokenLife
	if d.Config != nil && d.Config.Auth.JWTLifetime > 0 {
		ti.jwtLife = d.Config.Auth.JWTLifetime
	}
	return ti
}

// LoginContext describes where a client login came from.
type LoginContext struct {
	// Method is store.LoginMethodPassword / LoginMethodOIDC / LoginMethodWebAuth.
	Method string
	// DeviceID and DeviceUUID are the client's RustDesk id and base64 uuid.
	DeviceID   string
	DeviceUUID string
	// Platform is deviceInfo.os; ClientKind is deviceInfo.type ("client", "browser").
	Platform   string
	ClientKind string
	IP         string
	UserAgent  string
}

// LoginContextFromRequest fills IP and UserAgent from r.
func LoginContextFromRequest(r *http.Request, method string) LoginContext {
	return LoginContext{Method: method, IP: httpx.ClientIPString(r), UserAgent: clip(r.UserAgent(), 512)}
}

// Issue creates a client token for u and returns it (shown to the client once).
func (ti *TokenIssuer) Issue(ctx context.Context, u *store.User, lc LoginContext) (string, error) {
	if u.ID <= 0 || u.ID > int64(^uint32(0)) {
		return "", fmt.Errorf("clientapi: user id %d does not fit u32", u.ID)
	}
	now := ti.now()
	tokenLife, jwtLife := ti.lifetimes(ctx)
	jwtExp := now.Add(jwtLife).Truncate(time.Second)
	// The JWT is {user_id, exp} only (hbbs compatibility), so two logins of
	// one user within the same second would mint the same token: on a
	// token_sha256 conflict move exp one second later and retry.
	for attempt := 0; ; attempt++ {
		raw, err := ti.issue(ctx, u, lc, now, jwtExp, tokenLife)
		if errors.Is(err, store.ErrConflict) && attempt < 30 {
			jwtExp = jwtExp.Add(time.Second)
			continue
		}
		return raw, err
	}
}

func (ti *TokenIssuer) issue(ctx context.Context, u *store.User, lc LoginContext, now, jwtExp time.Time, tokenLife time.Duration) (string, error) {
	raw, err := ti.jwt.Mint(uint32(u.ID), jwtExp)
	if err != nil {
		return "", err
	}
	err = ti.st.Tx(ctx, func(tx *store.Store) error {
		tok := &store.AuthToken{
			UserID: u.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken(raw), JWTExpiresAt: &jwtExp,
			ExpiresAt: now.Add(tokenLife), DeviceRustdeskID: clip(lc.DeviceID, maxIDLen),
			DeviceUUID: clip(lc.DeviceUUID, 256), ClientPlatform: clip(lc.Platform, 64), UserAgent: lc.UserAgent,
			IP: lc.IP, CreatedAt: now, LastUsedAt: &now,
		}
		if err := tx.AuthTokens.Create(ctx, tok); err != nil {
			return err
		}
		if err := tx.LoginEvents.Record(ctx, &store.LoginEvent{
			UserID: &u.ID, TokenID: &tok.ID, Method: lc.Method, Result: store.LoginSuccess,
			ClientKind: clip(lc.ClientKind, 64), DeviceRustdeskID: tok.DeviceRustdeskID, DeviceUUID: tok.DeviceUUID,
			Platform: tok.ClientPlatform, IP: lc.IP, UserAgent: lc.UserAgent, CreatedAt: now,
		}); err != nil {
			return err
		}
		if _, err := tx.Devices.BindOwner(ctx, lc.DeviceID, lc.DeviceUUID, u.ID); err != nil {
			return err
		}
		return tx.Users.TouchLastLogin(ctx, u.ID)
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// UserPayload is the user object of login, currentUser and OIDC auth-query
// responses. status: 1 normal, 0 disabled (-1 unverified is unused). info is
// always an object: the Rust client (OIDC auth-query) requires it.
type UserPayload struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Avatar      string   `json:"avatar"`
	Email       string   `json:"email"`
	Note        string   `json:"note"`
	Status      int      `json:"status"`
	IsAdmin     bool     `json:"is_admin"`
	Info        UserInfo `json:"info"`
}

// UserInfo is UserPayload.info.
type UserInfo struct {
	EmailVerification      bool `json:"email_verification"`
	EmailAlarmNotification bool `json:"email_alarm_notification"`
}

// NewUserPayload maps a user to the client payload.
func NewUserPayload(u *store.User) UserPayload {
	return UserPayload{
		Name: u.Username, DisplayName: u.DisplayName, Avatar: u.AvatarURL, Email: u.Email, Note: u.Note,
		Status: userStatus(u), IsAdmin: u.IsAdmin(),
	}
}

func userStatus(u *store.User) int {
	if u.IsActive() {
		return 1
	}
	return 0
}

// AuthBody is the login response: {"type":"access_token","access_token",...}
// or a second-factor challenge {"type":"email_check","tfa_type":"tfa_check","secret"}.
type AuthBody struct {
	Type        string       `json:"type"`
	AccessToken string       `json:"access_token,omitempty"`
	TfaType     string       `json:"tfa_type,omitempty"`
	Secret      string       `json:"secret,omitempty"`
	User        *UserPayload `json:"user,omitempty"`
}

// requireAdminToken authenticates a Bearer client token of an active admin
// (deploy/assign tokens are admin client tokens).
func (h *handler) requireAdminToken(r *http.Request) (*session, error) {
	s, err := h.authenticate(r, true)
	if err != nil {
		return nil, err
	}
	if !s.user.IsAdmin() {
		return nil, errUnauthorized
	}
	return s, nil
}
