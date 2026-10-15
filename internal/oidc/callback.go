package oidc

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/loginlog"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// flowError is a login failure with a stable code (used in panel redirects)
// and a message safe to show to the end user (client error / HTML page).
type flowError struct {
	Code string
	Msg  string
}

func (e *flowError) Error() string { return e.Msg }

var (
	errExpired       = &flowError{"expired", "This login link has expired or was already used. Please start again."}
	errDenied        = &flowError{"denied", "Login was cancelled or denied by the identity provider."}
	errFailed        = &flowError{"failed", "Login with the identity provider failed."}
	errNoAccount     = &flowError{"no_account", "No account is linked to this identity. Sign in to the panel and link it under My security, or ask an administrator."}
	errDisabled      = &flowError{"disabled", "This account is disabled."}
	errAmbiguous     = &flowError{"ambiguous", "Several accounts use this email address; ask an administrator to link your identity."}
	errAlreadyLinked = &flowError{"already_linked", "This identity is already linked to another account, or your account already has an identity of this provider."}
	errMismatch      = &flowError{"session_mismatch", "This login was started in a different browser. Please start again."}
)

func asFlowError(err error) *flowError {
	var fe *flowError
	if errors.As(err, &fe) {
		return fe
	}
	return errFailed
}

// HandleCallback serves GET /api/oidc/callback (the IdP redirect target).
func (s *Service) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" || len(state) > 256 {
		s.page(w, http.StatusBadRequest, errExpired.Msg)
		return
	}
	st, err := s.st.OAuthStates.GetActive(ctx, crypto.HashToken(state))
	if errors.Is(err, store.ErrNotFound) {
		s.page(w, http.StatusBadRequest, errExpired.Msg)
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "oidc callback: load state", slog.Any("err", err))
		s.page(w, http.StatusInternalServerError, errFailed.Msg)
		return
	}

	if st.Flow == store.OAuthFlowWeb {
		c, cerr := r.Cookie(BindingCookie)
		s.clearBinding(w)
		if cerr != nil || !crypto.EqualString(c.Value, state) {
			s.webFail(w, r, st, errMismatch)
			return
		}
		if err := s.st.OAuthStates.Consume(ctx, st.ID); err != nil {
			s.webFail(w, r, st, errExpired)
			return
		}
	} else if err := s.st.OAuthStates.Claim(ctx, st.ID); err != nil {
		s.page(w, http.StatusBadRequest, errExpired.Msg)
		return
	}

	prov, profile, err := s.complete(ctx, st, q)
	if err == nil {
		switch st.Action {
		case store.OAuthLogin:
			var user *store.User
			if user, err = s.resolveLogin(ctx, prov, profile); err == nil {
				s.finishLogin(w, r, st, prov, user)
				return
			}
		case store.OAuthBind:
			if err = s.bind(ctx, st, prov, profile); err == nil {
				http.Redirect(w, r, withQuery(SafeRedirectPath(st.RedirectPath, DefaultBindReturn), "linked", prov.Slug), http.StatusFound)
				return
			}
		default:
			err = errFailed
		}
	}
	fe := asFlowError(err)
	if fe == errFailed {
		s.log.WarnContext(ctx, "oidc login failed", slog.String("provider", provSlug(prov)), slog.Any("err", err))
	}
	if st.Action == store.OAuthLogin {
		var uname string
		if profile != nil {
			uname = profile.Username
		}
		s.recordEvent(ctx, st, nil, nil, uname, false, "oidc_"+fe.Code, r)
	}
	if st.Flow == store.OAuthFlowWeb {
		s.webFail(w, r, st, fe)
		return
	}
	if cerr := s.st.OAuthStates.Complete(ctx, st.ID, nil, fe.Msg); cerr != nil {
		s.log.WarnContext(ctx, "oidc callback: complete state", slog.Any("err", cerr))
	}
	s.page(w, http.StatusOK, fe.Msg)
}

func provSlug(p *store.IdentityProvider) string {
	if p == nil {
		return ""
	}
	return p.Slug
}

// complete exchanges the authorization code and returns the IdP profile.
func (s *Service) complete(ctx context.Context, st *store.OAuthState, q url.Values) (*store.IdentityProvider, *Profile, error) {
	if st.ProviderID == nil {
		return nil, nil, errFailed
	}
	prov, err := s.st.IdentityProviders.Get(ctx, *st.ProviderID)
	if err != nil || !prov.Enabled {
		return nil, nil, errFailed
	}
	if q.Get("error") != "" {
		return prov, nil, errDenied
	}
	code := q.Get("code")
	if code == "" || len(code) > 4096 {
		return prov, nil, errFailed
	}
	cfg, err := s.resolve(ctx, prov)
	if err != nil {
		return prov, nil, err
	}
	var verifier string
	if len(st.PKCEVerifierEnc) > 0 {
		if verifier, err = s.aead.OpenString(st.PKCEVerifierEnc, crypto.OAuthStatePKCEVerifierAD(st.ID)); err != nil {
			return prov, nil, errors.New("cannot decrypt PKCE verifier")
		}
	}
	p, err := s.fetchProfile(ctx, cfg, code, verifier, st.Nonce)
	if err != nil {
		return prov, nil, err
	}
	return prov, p, nil
}

// resolveLogin maps an IdP profile to a local user:
//  1. an existing link (provider, subject) wins;
//  2. else, if the provider enables link_by_verified_email AND the IdP asserts
//     email_verified, link to the single user with that email;
//  3. else, if auto_register is on, create a user;
//  4. else fail. Unverified emails never link to existing accounts.
func (s *Service) resolveLogin(ctx context.Context, prov *store.IdentityProvider, p *Profile) (*store.User, error) {
	var user *store.User
	err := s.st.Tx(ctx, func(tx *store.Store) error {
		ident, err := tx.UserIdentities.GetByProviderSubject(ctx, prov.ID, p.Subject)
		if err == nil {
			if user, err = tx.Users.GetByID(ctx, ident.UserID); err != nil {
				return err
			}
			applyProfile(ident, p)
			return tx.UserIdentities.RecordLogin(ctx, ident)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if prov.LinkByVerifiedEmail && p.EmailVerified && p.Email != "" {
			users, err := tx.Users.ListByEmail(ctx, p.Email)
			if err != nil {
				return err
			}
			if len(users) > 1 {
				return errAmbiguous
			}
			if len(users) == 1 {
				user = users[0]
				return s.link(ctx, tx, user.ID, prov.ID, p)
			}
		}
		if !prov.AutoRegister {
			return errNoAccount
		}
		if user, err = s.register(ctx, tx, p); err != nil {
			return err
		}
		return s.link(ctx, tx, user.ID, prov.ID, p)
	})
	if err != nil {
		return nil, err
	}
	if !user.IsActive() {
		return nil, errDisabled
	}
	return user, nil
}

func applyProfile(ident *store.UserIdentity, p *Profile) {
	ident.Email, ident.EmailVerified = p.Email, p.EmailVerified
	ident.DisplayName, ident.Username, ident.AvatarURL = p.DisplayName, p.Username, p.AvatarURL
}

func (s *Service) link(ctx context.Context, tx *store.Store, userID, providerID int64, p *Profile) error {
	now := s.now()
	ident := &store.UserIdentity{UserID: userID, ProviderID: providerID, Subject: p.Subject, LastLoginAt: &now}
	applyProfile(ident, p)
	err := tx.UserIdentities.Create(ctx, ident)
	if errors.Is(err, store.ErrConflict) {
		return errAlreadyLinked
	}
	return err
}

var usernameJunk = regexp.MustCompile(`[^a-z0-9._-]+`)

// usernameBase derives a username candidate from the profile.
func usernameBase(p *Profile) string {
	cand := p.Username
	if cand == "" && p.Email != "" {
		cand = p.Email[:max(strings.IndexByte(p.Email, '@'), 0)]
	}
	cand = usernameJunk.ReplaceAllString(strings.ToLower(cand), "")
	cand = strings.Trim(cand, ".-_")
	if len(cand) > 64 {
		cand = cand[:64]
	}
	if cand == "" {
		cand = "user"
	}
	return cand
}

// register creates a password-less user for an auto-registered identity.
// Only a verified email is copied to users.email (it drives later linking).
func (s *Service) register(ctx context.Context, tx *store.Store, p *Profile) (*store.User, error) {
	base := usernameBase(p)
	u := &store.User{DisplayName: p.DisplayName, AvatarURL: p.AvatarURL, Role: store.RoleUser, Status: store.UserActive}
	if p.EmailVerified {
		u.Email = p.Email
	}
	for i := 0; i < 20; i++ {
		u.Username = base
		if i > 0 {
			sfx, err := crypto.RandomBytes(3)
			if err != nil {
				return nil, err
			}
			u.Username = fmt.Sprintf("%s-%d", base, int(sfx[0])<<16|int(sfx[1])<<8|int(sfx[2]))
		}
		err := tx.Users.Create(ctx, u)
		if err == nil {
			_ = tx.AdminAudit.Record(ctx, &store.AuditEntry{ActorName: "oidc", Action: "user.auto_register",
				TargetType: "user", TargetID: strconv.FormatInt(u.ID, 10), Details: map[string]any{"username": u.Username}})
			return u, nil
		}
		if !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a username")
}

// bind links the IdP identity to the initiating panel user.
func (s *Service) bind(ctx context.Context, st *store.OAuthState, prov *store.IdentityProvider, p *Profile) error {
	if st.InitiatorUserID == nil {
		return errFailed
	}
	uid := *st.InitiatorUserID
	return s.st.Tx(ctx, func(tx *store.Store) error {
		ident, err := tx.UserIdentities.GetByProviderSubject(ctx, prov.ID, p.Subject)
		if err == nil {
			if ident.UserID != uid {
				return errAlreadyLinked
			}
			applyProfile(ident, p)
			return tx.UserIdentities.RecordLogin(ctx, ident)
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		existing, err := tx.UserIdentities.ListForUser(ctx, uid)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.ProviderID == prov.ID {
				return errAlreadyLinked
			}
		}
		if err := s.link(ctx, tx, uid, prov.ID, p); err != nil {
			return err
		}
		return tx.AdminAudit.Record(ctx, &store.AuditEntry{ActorUserID: &uid, ActorName: "self",
			Action: "identity.link", TargetType: "user", TargetID: strconv.FormatInt(uid, 10),
			Details: map[string]any{"provider": prov.Slug}})
	})
}

// finishLogin completes a successful login for either flow.
func (s *Service) finishLogin(w http.ResponseWriter, r *http.Request, st *store.OAuthState, prov *store.IdentityProvider, user *store.User) {
	ctx := r.Context()
	if st.Flow == store.OAuthFlowWeb {
		s.recordEvent(ctx, st, &user.ID, nil, user.Username, true, "", r)
		if s.webLogin == nil {
			s.webFail(w, r, st, errFailed)
			return
		}
		s.webLogin(w, r, user, SafeRedirectPath(st.RedirectPath, "/"))
		return
	}
	if err := s.st.OAuthStates.Complete(ctx, st.ID, &user.ID, ""); err != nil {
		s.page(w, http.StatusBadRequest, errExpired.Msg)
		return
	}
	s.log.InfoContext(ctx, "oidc client login approved", slog.String("provider", prov.Slug), slog.Int64("user_id", user.ID))
	s.page(w, http.StatusOK, "")
}

// LoginErrorPath is the SPA route panel OIDC failures redirect to.
const LoginErrorPath = "/login"

func (s *Service) webFail(w http.ResponseWriter, r *http.Request, st *store.OAuthState, fe *flowError) {
	if st.Action == store.OAuthBind {
		http.Redirect(w, r, withQuery(SafeRedirectPath(st.RedirectPath, DefaultBindReturn), "link_error", fe.Code), http.StatusFound)
		return
	}
	http.Redirect(w, r, withQuery(LoginErrorPath, "error", "oidc_"+fe.Code), http.StatusFound)
}

func (s *Service) recordEvent(ctx context.Context, st *store.OAuthState, userID, tokenID *int64, username string,
	ok bool, reason string, r *http.Request) {
	ev := loginlog.Event{UserID: userID, TokenID: tokenID, Method: loginlog.MethodOIDC, Success: ok,
		FailureReason: reason, ClientKind: st.DeviceType, DeviceRustdeskID: st.DeviceRustdeskID,
		DeviceUUID: st.DeviceUUID, Platform: st.DeviceOS}
	if !ok {
		ev.UsernameAttempted = username
	}
	if r != nil {
		ev.IP, ev.UserAgent = httpx.ClientIPString(r), r.UserAgent()
	}
	if st.Flow == store.OAuthFlowClient && ev.ClientKind == "" {
		ev.ClientKind = loginlog.ClientClient
	}
	if err := s.events.Record(ctx, ev); err != nil {
		s.log.WarnContext(ctx, "oidc: record login event", slog.Any("err", err))
	}
}

// page renders the minimal HTML result page shown in the browser.
func (s *Service) page(w http.ResponseWriter, status int, errMsg string) {
	title, body := "Login successful", "You are signed in. You can close this window and return to RustDesk."
	if errMsg != "" {
		title, body = "Login failed", errMsg
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8">`+
		`<meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head>`+
		`<body><h1>%s</h1><p>%s</p></body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}
