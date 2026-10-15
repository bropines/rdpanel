package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// MinPasswordChars is the minimum length of a password set from the panel.
const MinPasswordChars = 8

// defaultTOTPIssuer labels the otpauth URI when no branding title is set.
const defaultTOTPIssuer = "RustDesk Panel"

// registerSecurity mounts the "my security" routes. They are reachable while
// a forced 2FA enrolment is pending (requireSession, not RequireUser).
func (c *authCore) registerSecurity(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireSession)
		r.Get("/me/security", c.handleSecurity)
		r.Post("/me/security/totp/enroll", c.handleTOTPEnroll)
		r.Post("/me/security/totp/confirm", c.handleTOTPConfirm)
		r.Post("/me/security/totp/disable", c.handleTOTPDisable)
		r.Post("/me/security/recovery-codes", c.handleRecoveryCodes)
		r.Get("/me/security/sessions", c.handleListSessions)
		r.Post("/me/security/sessions/revoke-others", c.handleRevokeOtherSessions)
		r.Delete("/me/security/sessions/{id}", c.handleRevokeSession)
		r.Post("/me/security/password", c.handleChangePassword)
		r.Get("/me/security/identities", c.handleListIdentities)
		r.Delete("/me/security/identities/{id}", c.handleUnlinkIdentity)
		r.Post("/me/security/identities/link/{slug}", c.handleLinkIdentity)
	})
	r.With(RequireUser).Patch("/me", c.handleUpdateProfile)
}

type profileRequest struct {
	DisplayName *string     `json:"display_name"`
	Email       opt[string] `json:"email"`
}

// handleUpdateProfile lets a user edit their own display name and email.
func (c *authCore) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req profileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	f := map[string]string{}
	u := *s.User
	if req.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*req.DisplayName)
		if utf8.RuneCountInString(u.DisplayName) > 128 {
			f["display_name"] = "must be at most 128 characters"
		}
	}
	if req.Email.Set {
		u.Email = ""
		if !req.Email.Null {
			u.Email = strings.TrimSpace(req.Email.V)
		}
		if e := u.Email; e != "" {
			if addr, err := mail.ParseAddress(e); err != nil || addr.Address != e || len(e) > 254 {
				f["email"] = "must be a valid email address"
			}
		}
	}
	if len(f) > 0 {
		validationFailed(w, "invalid profile", f)
		return
	}
	if err := c.d.Store.Users.Update(ctx, &u); err != nil {
		internalError(w)
		return
	}
	ch := diff{}
	ch.val("display_name", s.User.DisplayName, u.DisplayName)
	ch.val("email", s.User.Email, u.Email)
	if len(ch) > 0 {
		c.audit(ctx, r, "user.profile_update", "user", strconv.FormatInt(u.ID, 10), map[string]any{"changes": ch})
	}
	fresh, err := c.d.Store.Users.GetByID(ctx, u.ID)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, NewUserDTO(fresh, s.TOTPEnabled))
}

// handleRevokeOtherSessions revokes every session and client token of the
// user except the current session.
func (c *authCore) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var revoked int64
	err := c.d.Store.Tx(ctx, func(tx *store.Store) error {
		toks, err := tx.AuthTokens.ListActiveForUser(ctx, s.User.ID, "")
		if err != nil {
			return err
		}
		for _, t := range toks {
			if t.ID == s.Token.ID {
				continue
			}
			if err := tx.AuthTokens.Revoke(ctx, t.ID, "revoked by user"); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	if err != nil {
		internalError(w)
		return
	}
	if revoked > 0 {
		c.audit(ctx, r, "session.revoke_others", "user", strconv.FormatInt(s.User.ID, 10), map[string]any{"revoked": revoked})
	}
	httpx.WriteJSON(w, http.StatusOK, revokeResultDTO{Revoked: revoked})
}

type securityDTO struct {
	HasPassword            bool `json:"has_password"`
	TOTPEnabled            bool `json:"totp_enabled"`
	TOTPPending            bool `json:"totp_pending"`
	TOTPRequired           bool `json:"totp_required"`
	MustEnrollTOTP         bool `json:"must_enroll_totp"`
	RecoveryCodesRemaining int  `json:"recovery_codes_remaining"`
}

func (c *authCore) handleSecurity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	out := securityDTO{HasPassword: s.User.HasPassword(), TOTPEnabled: s.TOTPEnabled, MustEnrollTOTP: s.MustEnrollTOTP}
	out.TOTPRequired = s.User.TOTPRequired || c.sess.mustEnroll(ctx, s.User, false)
	ts, err := c.d.Store.TOTPSecrets.Get(ctx, s.User.ID)
	if err == nil {
		out.TOTPPending = !ts.Confirmed()
	} else if !errors.Is(err, store.ErrNotFound) {
		internalError(w)
		return
	}
	if out.RecoveryCodesRemaining, err = c.d.Store.RecoveryCodes.CountUnused(ctx, s.User.ID); err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *authCore) audit(ctx context.Context, r *http.Request, action, targetType, targetID string, details map[string]any) {
	recordAudit(ctx, c.d, r, action, targetType, targetID, details)
}

type totpEnrollDTO struct {
	Secret     string `json:"secret"`
	OTPAuthURI string `json:"otpauth_uri"`
}

func (c *authCore) handleTOTPEnroll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	issuer, _ := store.GetOr(ctx, c.d.Store.Settings, store.SettingBrandingTitle, defaultTOTPIssuer)
	if issuer == "" {
		issuer = defaultTOTPIssuer
	}
	key, err := c.totp.enroll(ctx, s.User, issuer)
	if errors.Is(err, store.ErrConflict) {
		httpx.WriteError(w, http.StatusConflict, "conflict", "two-factor authentication is already enabled")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, totpEnrollDTO{Secret: key.Secret, OTPAuthURI: key.URL})
}

type recoveryCodesDTO struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (c *authCore) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
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
	ok, err := c.totp.verifyTOTP(ctx, s.User.ID, req.Code, false)
	if err != nil {
		internalError(w)
		return
	}
	if !ok {
		c.fail(key)
		httpx.WriteError(w, http.StatusUnauthorized, "totp_invalid", "invalid verification code")
		return
	}
	c.succeed(key)
	codes, err := c.totp.newRecoveryCodes(ctx, c.d.Store, s.User.ID)
	if err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.totp_enable", "user", strconv.FormatInt(s.User.ID, 10), nil)
	httpx.WriteJSON(w, http.StatusOK, recoveryCodesDTO{RecoveryCodes: codes})
}

type reauthRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// reauth confirms the user's identity with the password or a TOTP/recovery
// code before a sensitive change. It writes the error response on failure.
func (c *authCore) reauth(w http.ResponseWriter, r *http.Request, user *store.User, req reauthRequest) bool {
	ctx := r.Context()
	key := userKey(user.Username)
	if ok, wait := c.allow(key); !ok {
		rateLimited(w, wait)
		return false
	}
	ok := false
	if req.Password != "" && user.HasPassword() {
		ok = c.d.Hasher.Verify(user.PasswordHash, req.Password)
	}
	if !ok && req.Code != "" {
		v, err := c.totp.verify(ctx, user.ID, req.Code)
		if err != nil {
			internalError(w)
			return false
		}
		ok = v
	}
	if !ok {
		c.fail(key)
		httpx.WriteError(w, http.StatusUnauthorized, "reauth_failed", "password or verification code is incorrect")
		return false
	}
	return true
}

func (c *authCore) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req reauthRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.TOTPEnabled {
		// Also clears an unconfirmed enrolment.
		if err := c.totp.disable(ctx, s.User.ID); err != nil {
			internalError(w)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.User.TOTPRequired || c.sess.mustEnroll(ctx, s.User, false) {
		httpx.WriteError(w, http.StatusForbidden, "totp_required", "two-factor authentication is required for this account")
		return
	}
	if !c.reauth(w, r, s.User, req) {
		return
	}
	if err := c.totp.disable(ctx, s.User.ID); err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.totp_disable", "user", strconv.FormatInt(s.User.ID, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (c *authCore) handleRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req reauthRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.TOTPEnabled {
		httpx.WriteError(w, http.StatusConflict, "conflict", "two-factor authentication is not enabled")
		return
	}
	if !c.reauth(w, r, s.User, req) {
		return
	}
	codes, err := c.totp.newRecoveryCodes(ctx, c.d.Store, s.User.ID)
	if err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.recovery_codes_regenerate", "user", strconv.FormatInt(s.User.ID, 10), nil)
	httpx.WriteJSON(w, http.StatusOK, recoveryCodesDTO{RecoveryCodes: codes})
}

func (c *authCore) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	toks, err := c.d.Store.AuthTokens.ListActiveForUser(ctx, s.User.ID, "")
	if err != nil {
		internalError(w)
		return
	}
	out := make([]sessionDTO, 0, len(toks))
	now := c.d.Store.Now()
	for _, t := range toks {
		if t.MFAPending || (t.JWTExpiresAt != nil && !t.JWTExpiresAt.After(now)) {
			continue
		}
		out = append(out, newSessionDTO(t, s.Token.ID))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[sessionDTO]{Items: out})
}

func (c *authCore) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		httpx.NotFound(w, r)
		return
	}
	t, err := c.d.Store.AuthTokens.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.UserID != s.User.ID) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	if err := c.d.Store.AuthTokens.Revoke(ctx, id, "revoked by user"); err != nil {
		internalError(w)
		return
	}
	if id == s.Token.ID {
		c.sess.clearCookies(w)
	}
	c.audit(ctx, r, "session.revoke", "auth_token", strconv.FormatInt(id, 10), map[string]any{"kind": string(t.Kind)})
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword sets a new password and revokes every other session
// and client token of the user.
func (c *authCore) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if utf8.RuneCountInString(req.NewPassword) < MinPasswordChars || len(req.NewPassword) > auth.MaxPasswordBytes {
		badRequest(w, "new password must be at least 8 characters")
		return
	}
	if s.User.HasPassword() {
		key := userKey(s.User.Username)
		if ok, wait := c.allow(key); !ok {
			rateLimited(w, wait)
			return
		}
		if !c.d.Hasher.Verify(s.User.PasswordHash, req.CurrentPassword) {
			c.fail(key)
			httpx.WriteError(w, http.StatusUnauthorized, "reauth_failed", "current password is incorrect")
			return
		}
	}
	hash, algo, err := c.d.Hasher.Hash(req.NewPassword)
	if err != nil {
		badRequest(w, "invalid password")
		return
	}
	var revoked int64
	err = c.d.Store.Tx(ctx, func(tx *store.Store) error {
		if err := tx.Users.SetPassword(ctx, s.User.ID, hash, string(algo)); err != nil {
			return err
		}
		toks, err := tx.AuthTokens.ListActiveForUser(ctx, s.User.ID, "")
		if err != nil {
			return err
		}
		for _, t := range toks {
			if t.ID == s.Token.ID {
				continue
			}
			if err := tx.AuthTokens.Revoke(ctx, t.ID, "password changed"); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	if err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.password_change", "user", strconv.FormatInt(s.User.ID, 10),
		map[string]any{"revoked_sessions": revoked})
	w.WriteHeader(http.StatusNoContent)
}

// IdentityDTO is components.schemas.UserIdentity (a linked IdP identity),
// used by /me/security/identities and the admin user detail.
type IdentityDTO struct {
	ID            int64      `json:"id"`
	ProviderID    int64      `json:"provider_id"`
	ProviderSlug  string     `json:"provider_slug"`
	ProviderKind  string     `json:"provider_kind"`
	ProviderName  string     `json:"provider_name"`
	Subject       string     `json:"subject"`
	Email         *string    `json:"email"`
	EmailVerified bool       `json:"email_verified"`
	Username      *string    `json:"username"`
	DisplayName   string     `json:"display_name"`
	AvatarURL     string     `json:"avatar_url"`
	LastLoginAt   *time.Time `json:"last_login_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

// IdentitiesOf lists a user's linked identities as API objects (also for
// the admin users page).
func IdentitiesOf(ctx context.Context, st *store.Store, userID int64) ([]IdentityDTO, error) {
	ids, err := st.UserIdentities.ListForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	provs, err := st.IdentityProviders.List(ctx, false)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*store.IdentityProvider{}
	for _, p := range provs {
		byID[p.ID] = p
	}
	out := make([]IdentityDTO, 0, len(ids))
	for _, i := range ids {
		d := IdentityDTO{ID: i.ID, ProviderID: i.ProviderID, Subject: i.Subject, Email: strPtr(i.Email),
			EmailVerified: i.EmailVerified, Username: strPtr(i.Username), DisplayName: i.DisplayName,
			AvatarURL: i.AvatarURL, LastLoginAt: i.LastLoginAt, CreatedAt: i.CreatedAt}
		if p := byID[i.ProviderID]; p != nil {
			d.ProviderSlug, d.ProviderName, d.ProviderKind = p.Slug, p.DisplayName, string(p.Kind)
			if d.ProviderName == "" {
				d.ProviderName = p.Slug
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (c *authCore) handleListIdentities(w http.ResponseWriter, r *http.Request) {
	out, err := IdentitiesOf(r.Context(), c.d.Store, CurrentSession(r.Context()).User.ID)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[IdentityDTO]{Items: out})
}

func (c *authCore) handleUnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		httpx.NotFound(w, r)
		return
	}
	var slug string
	err := c.d.Store.Tx(ctx, func(tx *store.Store) error {
		ident, err := tx.UserIdentities.Get(ctx, id)
		if err != nil || ident.UserID != s.User.ID {
			return store.ErrNotFound
		}
		if !s.User.HasPassword() {
			n, err := tx.UserIdentities.CountForUser(ctx, s.User.ID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return store.ErrConflict
			}
		}
		if p, err := tx.IdentityProviders.Get(ctx, ident.ProviderID); err == nil {
			slug = p.Slug
		}
		return tx.UserIdentities.DeleteForUser(ctx, s.User.ID, id)
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.NotFound(w, r)
	case errors.Is(err, store.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "conflict", "cannot unlink the last sign-in method; set a password first")
	case err != nil:
		internalError(w)
	default:
		c.audit(ctx, r, "identity.unlink", "user", strconv.FormatInt(s.User.ID, 10), map[string]any{"provider": slug})
		w.WriteHeader(http.StatusNoContent)
	}
}

func (c *authCore) handleLinkIdentity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if c.d.OIDC == nil {
		httpx.WriteError(w, http.StatusConflict, "oidc_not_configured", "OIDC is not available")
		return
	}
	var req oidcStartRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := c.d.OIDC.BeginBind(ctx, w, chi.URLParam(r, "slug"), s.User.ID, req.Next)
	if err != nil {
		c.writeOIDCStartError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, urlResponse{URL: u})
}
