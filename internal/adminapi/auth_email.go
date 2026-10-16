package adminapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/loginlog"
	"github.com/bropines/rustdesk-panel/internal/mailauth"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// mfaMethodEmail marks a pending login that owes an e-mailed code
// (loginResponse.mfa_methods).
const mfaMethodEmail = "email"

// limitForgot keys the "forgot password" / reset endpoints per client IP.
const limitForgot = "panel-forgot:"

// registerEmail mounts the e-mail routes: public password reset and auth
// options, e-mail code resend for a pending login, the "my e-mail"
// security routes and the admin SMTP settings.
func (c *authCore) registerEmail(r chi.Router) {
	r.Get("/auth/options", c.handleAuthOptions)
	r.Post("/auth/mfa/resend", c.handleMFAResend)
	r.Post("/auth/password/forgot", c.handleForgotPassword)
	r.Post("/auth/password/reset", c.handleResetPasswordByEmail)
	r.Group(func(r chi.Router) {
		r.Use(requireSession)
		r.Post("/me/security/email/verify", c.handleSendEmailVerification)
		r.Post("/me/security/email/confirm", c.handleConfirmEmailVerification)
		r.Put("/me/security/email-2fa", c.handleSetEmail2FA)
	})
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/settings/email", c.handleGetEmailSettings)
		r.Put("/settings/email", c.handleUpdateEmailSettings)
		r.Post("/settings/email/test", c.handleTestEmail)
	})
}

// writeMailError maps mailauth errors to responses.
func (c *authCore) writeMailError(w http.ResponseWriter, r *http.Request, err error) {
	var rl *mailauth.RateLimitError
	switch {
	case errors.As(err, &rl):
		rateLimited(w, rl.RetryAfter())
	case errors.Is(err, mailauth.ErrDisabled):
		httpx.WriteError(w, http.StatusConflict, "email_not_configured", "e-mail (SMTP) is not configured")
	case errors.Is(err, mailauth.ErrNoAddress):
		httpx.WriteError(w, http.StatusConflict, "no_email", "the account has no e-mail address")
	case errors.Is(err, mailauth.ErrSend):
		httpx.WriteError(w, http.StatusBadGateway, "email_send_failed", "could not send the e-mail, try again later")
	default:
		c.d.Log.ErrorContext(r.Context(), "mail", slog.Any("err", err))
		internalError(w)
	}
}

// emailFactorRequired reports whether a user without TOTP owes an e-mailed
// sign-in code.
func (c *authCore) emailFactorRequired(r *http.Request, user *store.User) (bool, error) {
	return c.mail.LoginFactorRequired(r.Context(), user)
}

type authOptionsDTO struct {
	PasswordReset bool `json:"password_reset"`
}

func (c *authCore) handleAuthOptions(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, authOptionsDTO{PasswordReset: c.mail.PasswordResetAvailable(r.Context())})
}

// handleMFAResend e-mails a new sign-in code for a pending e-mail login.
func (c *authCore) handleMFAResend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if s == nil || !s.MFAPending {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "no pending login")
		return
	}
	enrolled, err := c.totp.enrolled(ctx, s.User.ID)
	if err != nil {
		internalError(w)
		return
	}
	if enrolled {
		httpx.WriteError(w, http.StatusConflict, "conflict", "this login uses an authenticator app code")
		return
	}
	if err := c.mail.ResendLoginCode(ctx, s.User, httpx.ClientIPString(r)); err != nil {
		c.writeMailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type forgotRequest struct {
	Login string `json:"login"`
}

// handleForgotPassword always answers 202 (whether or not the account
// exists, has a verified address or was e-mailed).
func (c *authCore) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req forgotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Login = strings.TrimSpace(req.Login)
	if req.Login == "" || len(req.Login) > 320 {
		badRequest(w, "login is required")
		return
	}
	if !c.mail.PasswordResetAvailable(ctx) {
		httpx.WriteError(w, http.StatusConflict, "password_reset_unavailable", "password reset by e-mail is not available")
		return
	}
	key := limitForgot + httpx.ClientIPString(r)
	if ok, wait := c.allow(key); !ok {
		rateLimited(w, wait)
		return
	}
	c.fail(key) // every request counts: the backoff throttles bulk requests
	c.mail.RequestPasswordReset(ctx, req.Login, httpx.ClientIPString(r))
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{})
}

type resetByEmailRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// handleResetPasswordByEmail sets a new password with a reset token and
// revokes every session and client token of the user.
func (c *authCore) handleResetPasswordByEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req resetByEmailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if utf8.RuneCountInString(req.NewPassword) < MinPasswordChars || len(req.NewPassword) > auth.MaxPasswordBytes {
		validationFailed(w, "invalid password", map[string]string{"new_password": "must be at least 8 characters"})
		return
	}
	key := limitForgot + httpx.ClientIPString(r)
	if ok, wait := c.allow(key); !ok {
		rateLimited(w, wait)
		return
	}
	hash, algo, err := c.d.Hasher.Hash(req.NewPassword)
	if err != nil {
		badRequest(w, "invalid password")
		return
	}
	user, revoked, err := c.mail.ResetPassword(ctx, req.Token, hash, string(algo))
	if errors.Is(err, mailauth.ErrInvalidToken) {
		c.fail(key)
		httpx.WriteError(w, http.StatusBadRequest, "invalid_token", "the reset link is invalid or has expired")
		return
	}
	if err != nil {
		c.d.Log.ErrorContext(ctx, "reset password by e-mail", slog.Any("err", err))
		internalError(w)
		return
	}
	c.sess.clearCookies(w)
	c.audit(ctx, r, "user.password_reset_email", "user", strconv.FormatInt(user.ID, 10),
		map[string]any{"username": user.Username, "revoked_sessions": revoked})
	w.WriteHeader(http.StatusNoContent)
}

// ---- my e-mail ----

func (c *authCore) handleSendEmailVerification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if s.User.EmailVerified() {
		httpx.WriteError(w, http.StatusConflict, "already_verified", "the e-mail address is already verified")
		return
	}
	if err := c.mail.SendVerification(ctx, s.User, httpx.ClientIPString(r)); err != nil {
		c.writeMailError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *authCore) handleConfirmEmailVerification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req codeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	key := userKey(s.User.Username)
	if ok, wait := c.allow(key); !ok {
		rateLimited(w, wait)
		return
	}
	ok, err := c.mail.ConfirmVerification(ctx, s.User, req.Code)
	if err != nil {
		internalError(w)
		return
	}
	if !ok {
		c.fail(key)
		httpx.WriteError(w, http.StatusBadRequest, "code_invalid", "invalid or expired code")
		return
	}
	c.succeed(key)
	c.audit(ctx, r, "user.email_verify", "user", strconv.FormatInt(s.User.ID, 10), map[string]any{"email": s.User.Email})
	c.writeSecurity(w, r)
}

type email2FARequest struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (c *authCore) handleSetEmail2FA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req email2FARequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == s.User.Email2FAEnabled {
		c.writeSecurity(w, r)
		return
	}
	if req.Enabled {
		if !c.mail.Enabled(ctx) {
			c.writeMailError(w, r, mailauth.ErrDisabled)
			return
		}
		if !s.User.EmailVerified() {
			httpx.WriteError(w, http.StatusConflict, "email_not_verified", "verify your e-mail address first")
			return
		}
	} else {
		if req2, err := c.mail.Email2FARequiredByPolicy(ctx); err != nil {
			internalError(w)
			return
		} else if req2 && !s.TOTPEnabled {
			httpx.WriteError(w, http.StatusForbidden, "email_2fa_required", "e-mail codes are required for accounts without an authenticator app")
			return
		}
		// Users who can prove nothing else (OIDC-only, no TOTP) need no reauth.
		if s.User.HasPassword() || s.TOTPEnabled {
			if !c.reauth(w, r, s.User, reauthRequest{Password: req.Password, Code: req.Code}) {
				return
			}
		}
	}
	err := c.d.Store.Users.SetEmail2FA(ctx, s.User.ID, req.Enabled)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, http.StatusConflict, "email_not_verified", "verify your e-mail address first")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	action := "user.email_2fa_disable"
	if req.Enabled {
		action = "user.email_2fa_enable"
	}
	c.audit(ctx, r, action, "user", strconv.FormatInt(s.User.ID, 10), nil)
	c.writeSecurity(w, r)
}

// writeSecurity re-reads the user and writes the SecurityOverview.
func (c *authCore) writeSecurity(w http.ResponseWriter, r *http.Request) {
	s := CurrentSession(r.Context())
	u, err := c.d.Store.Users.GetByID(r.Context(), s.User.ID)
	if err != nil {
		internalError(w)
		return
	}
	cp := *s
	cp.User = u
	out, err := c.security(r.Context(), &cp)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ---- admin SMTP settings ----

// emailSettingsDTO is components.schemas.EmailSettings.
type emailSettingsDTO struct {
	Source             string `json:"source"`
	Enabled            bool   `json:"enabled"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	PasswordSet        bool   `json:"password_set"`
	From               string `json:"from"`
	Encryption         string `json:"encryption"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	DefaultLocale      string `json:"default_locale"`
	PasswordReset      bool   `json:"password_reset_available"`
}

func (c *authCore) emailSettings(r *http.Request) (emailSettingsDTO, error) {
	ctx := r.Context()
	v, err := c.mail.View(ctx)
	if err != nil {
		return emailSettingsDTO{}, err
	}
	return emailSettingsDTO{Source: string(v.Source), Enabled: v.Source != mailauth.SourceNone, Host: v.Host,
		Port: v.Port, Username: v.Username, PasswordSet: v.PasswordSet, From: v.From, Encryption: v.Encryption,
		InsecureSkipVerify: v.InsecureSkipVerify, DefaultLocale: c.mail.DefaultLocale(ctx),
		PasswordReset: c.mail.PasswordResetAvailable(ctx)}, nil
}

func (c *authCore) handleGetEmailSettings(w http.ResponseWriter, r *http.Request) {
	out, err := c.emailSettings(r)
	if err != nil {
		c.d.Log.ErrorContext(r.Context(), "email settings", slog.Any("err", err))
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// emailSettingsInput is components.schemas.EmailSettingsInput.
type emailSettingsInput struct {
	Host               *string `json:"host"`
	Port               *int    `json:"port"`
	Username           *string `json:"username"`
	Password           *string `json:"password"`
	From               *string `json:"from"`
	Encryption         *string `json:"encryption"`
	InsecureSkipVerify *bool   `json:"insecure_skip_verify"`
	DefaultLocale      *string `json:"default_locale"`
}

func (c *authCore) handleUpdateEmailSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in emailSettingsInput
	if !decodeJSON(w, r, &in) {
		return
	}
	cur, err := c.mail.View(ctx)
	if err != nil {
		internalError(w)
		return
	}
	smtpTouched := in.Host != nil || in.Port != nil || in.Username != nil || in.Password != nil || in.From != nil ||
		in.Encryption != nil || in.InsecureSkipVerify != nil
	f := map[string]string{}
	if smtpTouched && cur.Source == mailauth.SourceConfig {
		f["host"] = "read-only: set via RDPANEL_SMTP_* (server configuration)"
	}
	if in.DefaultLocale != nil && *in.DefaultLocale != "en" && *in.DefaultLocale != "ru" {
		f["default_locale"] = "must be en or ru"
	}
	si := mailauth.SMTPInput{Host: cur.Host, Port: cur.Port, Username: cur.Username, From: cur.From,
		Encryption: cur.Encryption, InsecureSkipVerify: cur.InsecureSkipVerify, Password: in.Password}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&si.Host, in.Host)
	set(&si.Username, in.Username)
	set(&si.From, in.From)
	set(&si.Encryption, in.Encryption)
	if in.Port != nil {
		si.Port = *in.Port
	}
	if in.InsecureSkipVerify != nil {
		si.InsecureSkipVerify = *in.InsecureSkipVerify
	}
	if smtpTouched && cur.Source != mailauth.SourceConfig {
		for k, v := range mailauth.ValidateInput(&si) {
			f[k] = v
		}
	}
	if len(f) > 0 {
		validationFailed(w, "invalid e-mail settings", f)
		return
	}
	actor := CurrentUser(ctx).ID
	changes := diff{}
	if smtpTouched {
		if err := c.mail.SaveStored(ctx, si, &actor); err != nil {
			c.d.Log.ErrorContext(ctx, "save smtp settings", slog.Any("err", err))
			internalError(w)
			return
		}
		changes.val("host", cur.Host, si.Host)
		changes.val("port", cur.Port, si.Port)
		changes.val("username", cur.Username, si.Username)
		changes.val("from", cur.From, si.From)
		changes.val("encryption", cur.Encryption, si.Encryption)
		changes.val("insecure_skip_verify", cur.InsecureSkipVerify, si.InsecureSkipVerify)
		if in.Password != nil {
			changes["password"] = "changed"
		}
	}
	if in.DefaultLocale != nil {
		old := c.mail.DefaultLocale(ctx)
		if err := c.d.Store.Settings.Set(ctx, mailauth.SettingDefaultLocale, *in.DefaultLocale, &actor); err != nil {
			internalError(w)
			return
		}
		changes.val("default_locale", old, *in.DefaultLocale)
	}
	if len(changes) > 0 {
		c.audit(ctx, r, "settings.email_update", "settings", "", map[string]any{"changes": changes})
	}
	c.handleGetEmailSettings(w, r)
}

type testEmailRequest struct {
	To     string `json:"to"`
	Locale string `json:"locale"`
}

type testEmailResult struct {
	To string `json:"to"`
}

// handleTestEmail sends the test message to "to" (default: the admin's own
// address) through the effective configuration.
func (c *authCore) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req testEmailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		to = CurrentUser(ctx).Email
	}
	if a, err := mail.ParseAddress(to); to == "" || err != nil || a.Address != to || len(to) > 254 {
		validationFailed(w, "invalid recipient", map[string]string{"to": "must be a valid e-mail address"})
		return
	}
	key := "panel-mailtest:" + strconv.FormatInt(CurrentUser(ctx).ID, 10)
	if ok, wait := c.allow(key); !ok {
		rateLimited(w, wait)
		return
	}
	c.fail(key)
	err := c.mail.SendTest(ctx, to, req.Locale)
	details := map[string]any{"to": to, "ok": err == nil}
	if err != nil {
		details["error"] = clipStr(err.Error(), 300)
	}
	c.audit(ctx, r, "settings.email_test", "settings", "", details)
	if errors.Is(err, mailauth.ErrSend) {
		// Admin-only: show the SMTP error so the configuration can be fixed.
		httpx.WriteError(w, http.StatusBadGateway, "email_send_failed", clipStr(strings.TrimPrefix(err.Error(), mailauth.ErrSend.Error()+": "), 500))
		return
	}
	if err != nil {
		c.writeMailError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, testEmailResult{To: to})
}

// loginEmailHint is the masked address shown on the code prompt.
func loginEmailHint(u *store.User) string { return mailauth.MaskEmail(u.Email) }

// recordEmailFailure logs a wrong e-mail code (login_events + metrics).
func (c *authCore) recordEmailFailure(r *http.Request, u *store.User) {
	c.record(r.Context(), r, loginlog.Event{UserID: &u.ID, Method: loginlog.MethodPassword,
		UsernameAttempted: u.Username, FailureReason: "email_code_invalid"})
}
