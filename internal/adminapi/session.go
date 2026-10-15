package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// mfaPendingTTL bounds a session that passed the password step but still
// owes the second factor.
const mfaPendingTTL = 5 * time.Minute

// touchSlack avoids a DB write on every request: the idle expiry is slid
// only when it is more than this much behind "now + idle".
const touchSlack = time.Minute

// SessionInfo describes the authenticated panel session of a request.
type SessionInfo struct {
	Token *store.AuthToken
	User  *store.User
	// MFAPending: password accepted, TOTP/recovery code still owed.
	MFAPending bool
	// TOTPEnabled: the user has confirmed TOTP.
	TOTPEnabled bool
	// MustEnrollTOTP: policy requires 2FA but the user has not enrolled; only
	// the auth and "my security" endpoints are reachable.
	MustEnrollTOTP bool
}

type sessionCtxKey struct{}

// CurrentSession returns the request's session (nil when anonymous).
func CurrentSession(ctx context.Context) *SessionInfo {
	s, _ := ctx.Value(sessionCtxKey{}).(*SessionInfo)
	return s
}

// CurrentUser returns the fully authenticated user of the request, or nil
// (anonymous or second factor pending).
func CurrentUser(ctx context.Context) *store.User {
	if s := CurrentSession(ctx); s != nil && !s.MFAPending {
		return s.User
	}
	return nil
}

// WithSession returns ctx carrying s (tests of other handler packages).
func WithSession(ctx context.Context, s *SessionInfo) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, s)
}

// RequireUser admits fully authenticated users (no pending second factor,
// no pending forced 2FA enrolment). Mount it on every panel route except
// the auth and "my security" endpoints.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := CurrentSession(r.Context())
		switch {
		case s == nil || s.MFAPending:
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		case s.MustEnrollTOTP:
			httpx.WriteError(w, http.StatusForbidden, "totp_enrollment_required", "two-factor authentication must be set up first")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// RequireAdmin is RequireUser plus the admin role.
func RequireAdmin(next http.Handler) http.Handler {
	return RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CurrentSession(r.Context()).User.IsAdmin() {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "administrator role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// requireSession admits authenticated sessions even when 2FA enrolment is
// still owed (used by /auth/me, /auth/logout and the "my security" routes).
func requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := CurrentSession(r.Context()); s == nil || s.MFAPending {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SessionMiddleware returns the session loader for routers mounted outside
// Register (e.g. /api/v1/events). Combine it with RequireUser/RequireAdmin.
func SessionMiddleware(d Deps) func(http.Handler) http.Handler {
	return newAuthCore(d).sess.load
}

// sessions creates, loads and revokes panel sessions (auth_tokens kind=web).
type sessions struct {
	d    Deps
	totp *TOTPService
	// idle/absolute are the configured defaults; the auth.session_idle_hours
	// and auth.session_absolute_days settings override them (lifetimes).
	idle     time.Duration
	absolute time.Duration
	secure   bool

	mu       sync.Mutex
	cachedAt time.Time
	cIdle    time.Duration
}

// lifetimeCacheTTL bounds how long lookup uses a cached idle timeout.
const lifetimeCacheTTL = 10 * time.Second

// lifetimes reads the effective session lifetimes from the settings.
func (m *sessions) lifetimes(ctx context.Context) (idle, absolute time.Duration) {
	idle, absolute = m.idle, m.absolute
	st := m.d.Store.Settings
	if h, err := store.GetOr(ctx, st, store.SettingSessionIdleHours, 0); err == nil && h > 0 {
		idle = time.Duration(h) * time.Hour
	}
	if d, err := store.GetOr(ctx, st, store.SettingSessionAbsoluteDays, 0); err == nil && d > 0 {
		absolute = time.Duration(d) * 24 * time.Hour
	}
	if idle > absolute {
		idle = absolute
	}
	return idle, absolute
}

// idleTimeout is lifetimes' idle value, cached briefly (read per request).
func (m *sessions) idleTimeout(ctx context.Context) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cIdle == 0 || time.Since(m.cachedAt) > lifetimeCacheTTL {
		m.cIdle, _ = m.lifetimes(ctx)
		m.cachedAt = time.Now()
	}
	return m.cIdle
}

func newSessions(d Deps, totp *TOTPService) *sessions {
	m := &sessions{d: d, totp: totp, idle: 12 * time.Hour, absolute: 7 * 24 * time.Hour}
	if d.Config != nil {
		if d.Config.Auth.SessionIdle > 0 {
			m.idle = d.Config.Auth.SessionIdle
		}
		if d.Config.Auth.SessionAbsolute > 0 {
			m.absolute = d.Config.Auth.SessionAbsolute
		}
		m.secure = d.Config.AdminHTTPS()
	}
	return m
}

func (m *sessions) now() time.Time { return m.d.Store.Now() }

// create issues a new session for user, revoking the one presented by the
// request (session fixation / rotation), and rotates the CSRF token.
// It returns the new CSRF token.
func (m *sessions) create(ctx context.Context, w http.ResponseWriter, r *http.Request, user *store.User, mfaPending bool) (*store.AuthToken, string, error) {
	m.revokePresented(ctx, r, "rotated")
	tok, err := auth.NewOpaqueToken()
	if err != nil {
		return nil, "", err
	}
	now := m.now()
	idle, abs := m.lifetimes(ctx)
	if mfaPending {
		idle, abs = mfaPendingTTL, mfaPendingTTL
	}
	absAt := now.Add(abs)
	row := &store.AuthToken{UserID: user.ID, Kind: store.TokenWeb, TokenSHA256: tok.Hash,
		ExpiresAt: now.Add(idle), AbsoluteExpiresAt: &absAt, MFAPending: mfaPending,
		IP: httpx.ClientIPString(r), UserAgent: clipStr(r.UserAgent(), 512), LastUsedAt: &now}
	if err := m.d.Store.AuthTokens.Create(ctx, row); err != nil {
		return nil, "", err
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: tok.Plain, Path: "/", Expires: absAt,
		MaxAge: int(abs / time.Second), HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode})
	csrf, err := setCSRFCookie(w, m.secure)
	if err != nil {
		return nil, "", err
	}
	return row, csrf, nil
}

// revokePresented revokes the session cookie sent with r, if any.
func (m *sessions) revokePresented(ctx context.Context, r *http.Request, reason string) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return
	}
	t, err := m.d.Store.AuthTokens.GetActive(ctx, store.TokenWeb, crypto.HashToken(c.Value))
	if err != nil {
		return
	}
	if err := m.d.Store.AuthTokens.Revoke(ctx, t.ID, reason); err != nil {
		m.d.Log.WarnContext(ctx, "revoke session", slog.Any("err", err))
	}
}

func (m *sessions) clearCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.secure, SameSite: http.SameSiteLaxMode})
}

// mustEnroll reports whether policy forces user to enrol TOTP.
func (m *sessions) mustEnroll(ctx context.Context, user *store.User, enrolled bool) bool {
	if enrolled {
		return false
	}
	if user.TOTPRequired {
		return true
	}
	if user.IsAdmin() {
		req, err := store.GetOr(ctx, m.d.Store.Settings, store.SettingRequireAdmin2FA, false)
		return err == nil && req
	}
	return false
}

// load is middleware attaching the session (if any) to the request context.
func (m *sessions) load(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" || len(c.Value) > 128 {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		info, err := m.lookup(ctx, c.Value)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				m.d.Log.ErrorContext(ctx, "session lookup", slog.Any("err", err))
				httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithSession(ctx, info)))
	})
}

func (m *sessions) lookup(ctx context.Context, plain string) (*SessionInfo, error) {
	t, err := m.d.Store.AuthTokens.GetActive(ctx, store.TokenWeb, crypto.HashToken(plain))
	if err != nil {
		return nil, err
	}
	user, err := m.d.Store.Users.GetByID(ctx, t.UserID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		_ = m.d.Store.AuthTokens.Revoke(ctx, t.ID, "user disabled")
		return nil, store.ErrNotFound
	}
	info := &SessionInfo{Token: t, User: user, MFAPending: t.MFAPending}
	if !t.MFAPending {
		now := m.now()
		if want := now.Add(m.idleTimeout(ctx)); want.Sub(t.ExpiresAt) > touchSlack {
			if err := m.d.Store.AuthTokens.Touch(ctx, t.ID, &want); err != nil {
				return nil, err
			}
		}
		if info.TOTPEnabled, err = m.totp.enrolled(ctx, user.ID); err != nil {
			return nil, err
		}
		info.MustEnrollTOTP = m.mustEnroll(ctx, user, info.TOTPEnabled)
	}
	return info, nil
}
