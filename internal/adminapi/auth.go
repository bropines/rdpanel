package adminapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/loginlog"
	"github.com/bropines/rdpanel/internal/mailauth"
	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/oidc"
	"github.com/bropines/rdpanel/internal/store"
)

// Generic credential failure: never reveal whether the user exists, is
// disabled or has no password.
const msgBadCredentials = "invalid username or password"

// Rate-limit key prefixes (shared Limiter with the client API).
const (
	limitIP   = "panel-ip:"
	limitUser = "panel-user:"
)

// authCore bundles the collaborators of the auth and security handlers.
type authCore struct {
	d        Deps
	sess     *sessions
	totp     *TOTPService
	mail     *mailauth.Service
	limiter  auth.Limiter
	events   loginlog.Recorder
	pk       *passkeys
	dummyMu  sync.Once
	dummyPwH string
}

func newAuthCore(d Deps) *authCore {
	totp := d.TOTP
	if totp == nil {
		totp = NewTOTPService(d.Store, d.AEAD)
	}
	mail := d.Mail
	if mail == nil {
		mail = mailauth.New(mailauth.Options{Store: d.Store, AEAD: d.AEAD, Config: d.Config, Log: d.Log})
	}
	c := &authCore{d: d, sess: newSessions(d, totp), totp: totp, mail: mail, limiter: d.Limiter, events: d.LoginEvents,
		pk: newPasskeys(d.Config)}
	if c.limiter == nil {
		c.limiter = auth.NewBackoff(auth.DefaultBackoffConfig, d.Store.Now)
	}
	c.sess.limiter = c.limiter
	if c.events == nil {
		c.events = loginlog.DB{Q: d.Store.Querier(), Now: d.Store.Now}
	}
	return c
}

// dummyHash is a real argon2id hash of a random password, verified when the
// username does not exist so both paths take the same time.
func (c *authCore) dummyHash() string {
	c.dummyMu.Do(func() {
		pw, _ := crypto.RandomToken(16)
		c.dummyPwH, _, _ = c.d.Hasher.Hash(pw)
	})
	return c.dummyPwH
}

func (c *authCore) record(ctx context.Context, r *http.Request, ev loginlog.Event) {
	ev.ClientKind = loginlog.ClientWebAdmin
	ev.IP, ev.UserAgent = httpx.ClientIPString(r), r.UserAgent()
	if err := c.events.Record(ctx, ev); err != nil {
		c.d.Log.WarnContext(ctx, "record login event", slog.Any("err", err))
	}
	if !ev.Success {
		factor := ev.Method
		switch {
		case strings.HasPrefix(ev.FailureReason, "totp"):
			factor = metrics.MethodTOTP
		case strings.HasPrefix(ev.FailureReason, "email_code"):
			factor = metrics.MethodEmail
		}
		c.d.Telemetry.Login(metrics.SurfacePanel, factor, metrics.ResultFailure)
	}
}

func (c *authCore) allow(keys ...string) (bool, time.Duration) {
	var wait time.Duration
	for _, k := range keys {
		if ok, d := c.limiter.Allow(k); !ok && d > wait {
			wait = d
		}
	}
	return wait == 0, wait
}

func (c *authCore) fail(keys ...string) {
	for _, k := range keys {
		c.limiter.Failure(k)
	}
}

func (c *authCore) succeed(keys ...string) {
	for _, k := range keys {
		c.limiter.Success(k)
	}
}

func userKey(name string) string { return limitUser + strings.ToLower(strings.TrimSpace(name)) }

func (c *authCore) me(ctx context.Context, user *store.User, csrf string) (meDTO, error) {
	enrolled, err := c.totp.enrolled(ctx, user.ID)
	if err != nil {
		return meDTO{}, err
	}
	return meDTO{User: NewUserDTO(user, enrolled), CSRFToken: csrf, MustEnrollTOTP: c.sess.mustEnroll(ctx, user, enrolled)}, nil
}

// registerAuth mounts /auth/* routes.
func (c *authCore) registerAuth(r chi.Router) {
	r.Get("/auth/csrf", c.handleCSRF)
	r.Post("/auth/login", c.handleLogin)
	r.Post("/auth/mfa", c.handleMFA)
	r.Post("/auth/mfa/passkey/begin", c.handlePasskeyMFABegin)
	r.Post("/auth/mfa/passkey/finish", c.handlePasskeyMFAFinish)
	r.Post("/auth/passkey/begin", c.handlePasskeyLoginBegin)
	r.Post("/auth/passkey/finish", c.handlePasskeyLoginFinish)
	r.Post("/auth/logout", c.handleLogout)
	r.With(requireSessionOrToken).Get("/auth/me", c.handleMe)
	r.Get("/auth/providers", c.handleProviders)
	r.Post("/auth/oidc/{slug}/start", c.handleOIDCStart)
	r.Get("/auth/oidc/complete", c.handleOIDCComplete)
}

// handleOIDCComplete finishes a panel OIDC login/link on the admin host
// (hand-off from the public-host callback, see oidc.Service.CompleteWeb).
func (c *authCore) handleOIDCComplete(w http.ResponseWriter, r *http.Request) {
	if c.d.OIDC == nil {
		http.Redirect(w, r, "/login?error=oidc_failed", http.StatusFound)
		return
	}
	c.d.OIDC.CompleteWeb(w, r)
}

func (c *authCore) handleCSRF(w http.ResponseWriter, r *http.Request) {
	tok := csrfFromRequest(r)
	if tok == "" {
		var err error
		if tok, err = setCSRFCookie(w, c.sess.secure); err != nil {
			internalError(w)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"csrf_token": tok})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

// loginResponse: either mfa_required (session cookie is a pending session)
// or the authenticated Me.
type loginResponse struct {
	MFARequired bool `json:"mfa_required"`
	// MFAMethods lists the usable second factors ("totp", "passkey", or
	// "email" when a code was e-mailed) when MFARequired.
	MFAMethods []string `json:"mfa_methods,omitempty"`
	// EmailHint is the masked address the code was sent to ("email").
	EmailHint string `json:"email_hint,omitempty"`
	Me        *meDTO `json:"me,omitempty"`
}

func (c *authCore) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || len(req.Username) > 128 || req.Password == "" || len(req.Password) > auth.MaxPasswordBytes {
		badRequest(w, "username and password are required")
		return
	}
	ipKey, uKey := limitIP+httpx.ClientIPString(r), userKey(req.Username)
	if ok, wait := c.allow(ipKey, uKey); !ok {
		c.d.Telemetry.Login(metrics.SurfacePanel, metrics.MethodPassword, metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}

	user, err := c.d.Store.Users.GetByUsername(ctx, req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		c.d.Log.ErrorContext(ctx, "login: load user", slog.Any("err", err))
		internalError(w)
		return
	}
	// localOK: the password matched the stored local hash (not LDAP).
	var ok, localOK bool
	reason := "bad_credentials"
	// The directory is asked only for unknown usernames and LDAP-provisioned
	// users: a local account (is_ldap = 0) is never authenticated, or linked,
	// through LDAP.
	tryLDAP := func() bool {
		if c.d.LDAP == nil || (user != nil && !user.IsLDAP) {
			return false
		}
		ldapUser, err := c.d.LDAP.Authenticate(ctx, req.Username, req.Password)
		if err != nil {
			return false
		}
		user = ldapUser
		return true
	}
	switch {
	case user == nil:
		ok = tryLDAP()
		if !ok {
			c.d.Hasher.Verify(c.dummyHash(), req.Password)
			reason = "unknown_user"
		}
	case !user.HasPassword():
		ok = tryLDAP()
		if !ok {
			c.d.Hasher.Verify(c.dummyHash(), req.Password)
			reason = "no_password"
		}
	default:
		ok = c.d.Hasher.Verify(user.PasswordHash, req.Password)
		localOK = ok
		if !ok {
			ok = tryLDAP()
		}
	}
	if ok && !user.IsActive() {
		ok, reason = false, "disabled"
	}
	if !ok {
		c.fail(ipKey, uKey)
		ev := loginlog.Event{Method: loginlog.MethodPassword, UsernameAttempted: req.Username, FailureReason: reason}
		if user != nil {
			ev.UserID = &user.ID
		}
		c.record(ctx, r, ev)
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", msgBadCredentials)
		return
	}
	// Checked after the password so the answer reveals nothing to guessers.
	if allowed, err := c.d.Store.Settings.PasswordLoginAllowed(ctx, user); err != nil {
		internalError(w)
		return
	} else if !allowed {
		c.record(ctx, r, loginlog.Event{UserID: &user.ID, Method: loginlog.MethodPassword,
			UsernameAttempted: req.Username, FailureReason: "password_login_disabled"})
		httpx.WriteError(w, http.StatusForbidden, "password_login_disabled",
			"password sign-in is disabled; use an identity provider")
		return
	}
	// Upgrade the local hash only when it verified this password: a password
	// checked by LDAP must never be stored as a local one.
	if localOK && user.HasPassword() && c.d.Hasher.NeedsRehash(user.PasswordHash) {
		if h, algo, err := c.d.Hasher.Hash(req.Password); err == nil {
			if err := c.d.Store.Users.RehashPassword(ctx, user.ID, h, string(algo)); err != nil {
				c.d.Log.WarnContext(ctx, "login: rehash", slog.Any("err", err))
			}
		}
	}

	enrolled, err := c.totp.enrolled(ctx, user.ID)
	if err != nil {
		internalError(w)
		return
	}
	if enrolled {
		if req.TOTPCode == "" {
			// Password step done: issue a short-lived pending session.
			if _, _, err := c.sess.create(ctx, w, r, user, true); err != nil {
				internalError(w)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, loginResponse{MFARequired: true, MFAMethods: c.mfaMethods(ctx, user.ID)})
			return
		}
		valid, err := c.totp.verify(ctx, user.ID, req.TOTPCode)
		if err != nil {
			internalError(w)
			return
		}
		if !valid {
			c.fail(ipKey, uKey)
			c.record(ctx, r, loginlog.Event{UserID: &user.ID, Method: loginlog.MethodPassword,
				UsernameAttempted: req.Username, FailureReason: "totp_invalid"})
			httpx.WriteError(w, http.StatusUnauthorized, "totp_invalid", "invalid verification code")
			return
		}
	}
	if !enrolled {
		need, err := c.emailFactorRequired(r, user)
		if err != nil {
			internalError(w)
			return
		}
		if need {
			// Password step done: e-mail a code and issue a pending session.
			if err := c.mail.SendLoginCode(ctx, user, httpx.ClientIPString(r)); err != nil {
				c.writeMailError(w, r, err)
				return
			}
			if _, _, err := c.sess.create(ctx, w, r, user, true); err != nil {
				internalError(w)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, loginResponse{MFARequired: true, MFAMethods: []string{mfaMethodEmail},
				EmailHint: loginEmailHint(user)})
			return
		}
	}
	factor := metrics.MethodPassword
	if enrolled {
		factor = metrics.MethodTOTP
	}
	c.completeLogin(w, r, user, loginlog.MethodPassword, factor, ipKey, uKey)
}

// completeLogin issues a full session and writes Me (inside loginResponse).
// method is stored in login_events; factor (the step that completed the
// login: password or totp) labels the login metric.
func (c *authCore) completeLogin(w http.ResponseWriter, r *http.Request, user *store.User, method, factor string, keys ...string) {
	ctx := r.Context()
	tok, csrf, err := c.sess.create(ctx, w, r, user, false)
	if err != nil {
		internalError(w)
		return
	}
	c.succeed(keys...)
	if err := c.d.Store.Users.TouchLastLogin(ctx, user.ID); err != nil {
		c.d.Log.WarnContext(ctx, "touch last login", slog.Any("err", err))
	}
	c.record(ctx, r, loginlog.Event{UserID: &user.ID, TokenID: &tok.ID, Method: method, Success: true})
	c.d.Telemetry.Login(metrics.SurfacePanel, factor, metrics.ResultSuccess)
	me, err := c.me(ctx, user, csrf)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, loginResponse{Me: &me})
}

type codeRequest struct {
	Code string `json:"code"`
}

// handleMFA completes a pending login with a TOTP or recovery code.
func (c *authCore) handleMFA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if s == nil || !s.MFAPending {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "no pending login")
		return
	}
	var req codeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uKey := userKey(s.User.Username)
	ipKey := limitIP + httpx.ClientIPString(r)
	if ok, wait := c.allow(ipKey, uKey); !ok {
		c.d.Telemetry.Login(metrics.SurfacePanel, metrics.MethodTOTP, metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}
	// The pending login owes TOTP when enrolled, otherwise an e-mailed code.
	enrolled, err := c.totp.enrolled(ctx, s.User.ID)
	if err != nil {
		internalError(w)
		return
	}
	if !enrolled {
		valid, err := c.mail.VerifyLoginCode(ctx, s.User.ID, req.Code)
		if err != nil {
			internalError(w)
			return
		}
		if !valid {
			c.fail(ipKey, uKey)
			c.recordEmailFailure(r, s.User)
			httpx.WriteError(w, http.StatusUnauthorized, "code_invalid", "invalid or expired code")
			return
		}
		c.completeLogin(w, r, s.User, loginlog.MethodPassword, metrics.MethodEmail, ipKey, uKey)
		return
	}
	valid, err := c.totp.verify(ctx, s.User.ID, req.Code)
	if err != nil {
		internalError(w)
		return
	}
	if !valid {
		c.fail(ipKey, uKey)
		c.record(ctx, r, loginlog.Event{UserID: &s.User.ID, Method: loginlog.MethodPassword,
			UsernameAttempted: s.User.Username, FailureReason: "totp_invalid"})
		httpx.WriteError(w, http.StatusUnauthorized, "totp_invalid", "invalid verification code")
		return
	}
	// sess.create revokes the pending session (rotation).
	c.completeLogin(w, r, s.User, loginlog.MethodPassword, metrics.MethodTOTP, ipKey, uKey)
}

func (c *authCore) handleLogout(w http.ResponseWriter, r *http.Request) {
	c.sess.revokePresented(r.Context(), r, "logout")
	c.sess.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (c *authCore) handleMe(w http.ResponseWriter, r *http.Request) {
	s := CurrentSession(r.Context())
	csrf := csrfFromRequest(r)
	if csrf == "" && s.APIToken == nil {
		var err error
		if csrf, err = setCSRFCookie(w, c.sess.secure); err != nil {
			internalError(w)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, meDTO{User: NewUserDTO(s.User, s.TOTPEnabled), CSRFToken: csrf,
		MustEnrollTOTP: s.MustEnrollTOTP})
}

type providerOptionDTO struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
}

// loginOptionsDTO is the /auth/providers body: provider buttons plus whether
// "Sign in with a passkey" is offered.
type loginOptionsDTO struct {
	Items   []providerOptionDTO `json:"items"`
	Passkey bool                `json:"passkey"`
}

// handleProviders lists enabled providers for the login page buttons.
func (c *authCore) handleProviders(w http.ResponseWriter, r *http.Request) {
	provs, err := c.d.Store.IdentityProviders.List(r.Context(), true)
	if err != nil {
		internalError(w)
		return
	}
	out := []providerOptionDTO{}
	if c.d.OIDC != nil && c.d.OIDC.CallbackURL() != "" {
		for _, p := range provs {
			if oidc.SupportedKind(p.Kind) {
				name := p.DisplayName
				if name == "" {
					name = p.Slug
				}
				out = append(out, providerOptionDTO{Slug: p.Slug, DisplayName: name, Kind: string(p.Kind)})
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, loginOptionsDTO{Items: out, Passkey: c.pk.available()})
}

type oidcStartRequest struct {
	Next string `json:"next"`
}

type urlResponse struct {
	URL string `json:"url"`
}

func (c *authCore) writeOIDCStartError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, oidc.ErrUnknownProvider):
		httpx.WriteError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, oidc.ErrNotConfigured):
		httpx.WriteError(w, http.StatusConflict, "oidc_not_configured", err.Error())
	case errors.Is(err, oidc.ErrProviderUnavailable):
		httpx.WriteError(w, http.StatusBadGateway, "provider_unavailable", err.Error())
	default:
		c.d.Log.ErrorContext(r.Context(), "oidc start", slog.Any("err", err))
		internalError(w)
	}
}

// handleOIDCStart begins a panel login with provider {slug}; the SPA then
// navigates to the returned URL.
func (c *authCore) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if c.d.OIDC == nil {
		httpx.WriteError(w, http.StatusConflict, "oidc_not_configured", oidc.ErrNotConfigured.Error())
		return
	}
	var req oidcStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := c.d.OIDC.BeginWebLogin(r.Context(), w, chi.URLParam(r, "slug"), req.Next)
	if err != nil {
		c.writeOIDCStartError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, urlResponse{URL: u})
}

// MFAPath is the SPA route that asks for the second factor after an OIDC
// login of a user with TOTP enabled ("/login?mfa=1&next=...").
const MFAPath = "/login"

// webLogin completes a panel OIDC login (oidc.WebLoginFunc).
func (c *authCore) webLogin(w http.ResponseWriter, r *http.Request, user *store.User, next string) {
	ctx := r.Context()
	enrolled, err := c.totp.enrolled(ctx, user.ID)
	if err != nil {
		internalError(w)
		return
	}
	emailCode := false
	if !enrolled {
		if emailCode, err = c.emailFactorRequired(r, user); err != nil {
			internalError(w)
			return
		}
		if emailCode {
			if err := c.mail.SendLoginCode(ctx, user, httpx.ClientIPString(r)); err != nil {
				c.d.Log.WarnContext(ctx, "oidc login: send e-mail code", slog.Any("err", err))
				http.Redirect(w, r, MFAPath+"?error=email_failed", http.StatusFound)
				return
			}
		}
	}
	if _, _, err := c.sess.create(ctx, w, r, user, enrolled || emailCode); err != nil {
		internalError(w)
		return
	}
	if enrolled {
		http.Redirect(w, r, MFAPath+"?mfa=1&next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if emailCode {
		http.Redirect(w, r, MFAPath+"?mfa=email&next="+url.QueryEscape(next), http.StatusFound)
		return
	}
	if err := c.d.Store.Users.TouchLastLogin(ctx, user.ID); err != nil {
		c.d.Log.WarnContext(ctx, "touch last login", slog.Any("err", err))
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// OIDCWebLogin returns the oidc.WebLoginFunc that turns a successful panel
// OIDC login into a session (pending second factor when TOTP is enabled).
// Wire it into oidc.Options.WebLogin.
func OIDCWebLogin(d Deps) oidc.WebLoginFunc {
	return newAuthCore(d).webLogin
}
