// Package oidc implements login with external identity providers (GitHub,
// Google, generic OpenID Connect with discovery) for both the RustDesk client
// (/api/oidc/*, polling) and the panel (cookie session), plus linking
// identities to existing accounts.
//
// Security properties:
//   - state is a 256-bit random value stored only as sha256 in oauth_states,
//     5 minute TTL, single use (atomic claim/consume);
//   - PKCE (S256 by default) with the verifier AEAD-encrypted at rest;
//   - a nonce bound into the ID token; ID tokens are verified (asymmetric
//     algorithms only, iss/aud/azp/exp/iat/nonce);
//   - the redirect URI is always <public_url>/api/oidc/callback, never derived
//     from request headers;
//   - client polls are bound to the requesting device id+uuid;
//   - panel flows are bound to the browser by an HttpOnly cookie (only its
//     sha256 is stored on the state row). The IdP returns to the public host;
//     the callback stores the result and hands off to the admin host with a
//     single-use ticket (HandoffTTL), where the cookie is verified before any
//     session is created or identity linked (login CSRF protection, works when
//     the panel and the client API are served from different hosts);
//   - an identity is linked to an existing user by email only when the IdP
//     asserts email_verified=true and the provider enables
//     link_by_verified_email.
package oidc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/loginlog"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// DefaultStateTTL is the lifetime of a pending OAuth flow.
const DefaultStateTTL = 5 * time.Minute

// CallbackPath is the redirect URI path registered at every IdP (legacy
// compatible: production IdP registrations contain exactly this path).
const CallbackPath = "/api/oidc/callback"

// Errors returned to callers (messages are safe to show).
var (
	ErrUnknownProvider = errors.New("unknown or disabled identity provider")
	ErrNotConfigured   = errors.New("OIDC is not available: public URL is not configured")
	// ErrProviderUnavailable means discovery or secret decryption failed.
	ErrProviderUnavailable = errors.New("identity provider is unavailable")
)

// WebLoginFunc completes a panel OIDC login: it must create the session
// (rotating any existing one), set cookies and write the redirect response.
// redirectPath is already validated as a safe relative path.
type WebLoginFunc func(w http.ResponseWriter, r *http.Request, user *store.User, redirectPath string)

// ClientLoginFunc is called after a client token was issued via OIDC (e.g. to
// bind the device to the user). Optional.
type ClientLoginFunc func(ctx context.Context, user *store.User, tokenID int64, st *store.OAuthState)

// Options configure a Service.
type Options struct {
	Store *store.Store
	AEAD  *crypto.AEAD
	JWT   *auth.ClientJWT
	Log   *slog.Logger
	// PublicURL is the configured external base URL (no trailing slash).
	PublicURL string
	// AdminURL is the panel base URL (default PublicURL); panel flows are
	// handed off there after the IdP callback.
	AdminURL string
	// TokenLifetime is the sliding lifetime of client tokens; JWTLifetime the
	// fixed JWT exp lifetime (0 = TokenLifetime).
	TokenLifetime time.Duration
	JWTLifetime   time.Duration
	// StateTTL defaults to DefaultStateTTL.
	StateTTL time.Duration
	// HTTPClient talks to identity providers (default: 10 s timeout).
	HTTPClient *http.Client
	// SecureCookies sets the Secure attribute on the browser-binding cookie.
	SecureCookies bool
	WebLogin      WebLoginFunc
	OnClientLogin ClientLoginFunc
	// LoginEvents records login attempts (default: loginlog.DB on the store).
	LoginEvents loginlog.Recorder
	// GitHub overrides the GitHub endpoints (tests); GoogleIssuer the Google issuer.
	GitHub       *GitHubEndpoints
	GoogleIssuer string
}

// Service runs OAuth/OIDC flows. Safe for concurrent use.
type Service struct {
	st            *store.Store
	aead          *crypto.AEAD
	jwt           *auth.ClientJWT
	log           *slog.Logger
	publicURL     string
	adminURL      string
	tokenLifetime time.Duration
	jwtLifetime   time.Duration
	stateTTL      time.Duration
	hc            *http.Client
	secure        bool
	webLogin      WebLoginFunc
	onClientLogin ClientLoginFunc
	events        loginlog.Recorder
	github        GitHubEndpoints
	googleIssuer  string
	cache         *cache
}

// NewService builds a Service.
func NewService(o Options) *Service {
	s := &Service{
		st: o.Store, aead: o.AEAD, jwt: o.JWT, log: o.Log,
		publicURL:     strings.TrimRight(o.PublicURL, "/"),
		adminURL:      strings.TrimRight(o.AdminURL, "/"),
		tokenLifetime: o.TokenLifetime, jwtLifetime: o.JWTLifetime, stateTTL: o.StateTTL,
		hc: o.HTTPClient, secure: o.SecureCookies, webLogin: o.WebLogin, onClientLogin: o.OnClientLogin,
		events: o.LoginEvents, github: DefaultGitHub, googleIssuer: GoogleIssuer, cache: newCache(),
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.adminURL == "" {
		s.adminURL = s.publicURL
	}
	if s.stateTTL <= 0 {
		s.stateTTL = DefaultStateTTL
	}
	if s.hc == nil {
		s.hc = &http.Client{Timeout: 10 * time.Second}
	}
	if s.tokenLifetime <= 0 {
		s.tokenLifetime = 4380 * time.Hour
	}
	if s.jwtLifetime <= 0 {
		s.jwtLifetime = s.tokenLifetime
	}
	if s.events == nil {
		s.events = loginlog.DB{Q: o.Store.Querier(), Now: o.Store.Now}
	}
	if o.GitHub != nil {
		s.github = *o.GitHub
	}
	if o.GoogleIssuer != "" {
		s.googleIssuer = o.GoogleIssuer
	}
	return s
}

func (s *Service) now() time.Time { return s.st.Now() }

// redirectURI is the single callback URL registered at every IdP.
func (s *Service) redirectURI() string { return s.publicURL + CallbackPath }

// CallbackURL returns the redirect URI admins must register at the IdP
// ("" when the public URL is not configured).
func (s *Service) CallbackURL() string {
	if s.publicURL == "" {
		return ""
	}
	return s.redirectURI()
}

// ClientDevice identifies the RustDesk device starting a client flow.
type ClientDevice struct {
	ID, UUID, Name, OS, Type string
}

type begun struct {
	state   string
	url     string
	binding string // web flows: browser-binding cookie value
}

// begin creates a pending state for provider slug and returns the IdP URL.
func (s *Service) begin(ctx context.Context, slug string, tmpl store.OAuthState) (*begun, error) {
	if s.publicURL == "" {
		return nil, ErrNotConfigured
	}
	p, err := s.st.IdentityProviders.GetBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (!p.Enabled || !SupportedKind(p.Kind))) {
		return nil, ErrUnknownProvider
	}
	if err != nil {
		return nil, err
	}
	cfg, err := s.resolve(ctx, p)
	if err != nil {
		s.log.WarnContext(ctx, "oidc: provider unavailable", slog.String("provider", p.Slug), slog.Any("err", err))
		return nil, ErrProviderUnavailable
	}
	state, err := auth.NewOpaqueToken()
	if err != nil {
		return nil, err
	}
	nonce, err := crypto.RandomToken(24)
	if err != nil {
		return nil, err
	}
	var verifier, binding string
	st := tmpl
	if st.Flow == store.OAuthFlowWeb {
		b, err := auth.NewOpaqueToken()
		if err != nil {
			return nil, err
		}
		binding, st.BindingSHA256 = b.Plain, b.Hash
	}
	st.StateSHA256 = state.Hash
	st.ProviderID = &p.ID
	st.Nonce = nonce
	if p.PKCEEnabled {
		if verifier, err = crypto.RandomToken(32); err != nil { // 43 chars, RFC 7636 §4.1
			return nil, err
		}
	}
	st.ExpiresAt = s.now().Add(s.stateTTL)
	// The verifier's AD contains the row id, so insert first, then seal.
	err = s.st.Tx(ctx, func(tx *store.Store) error {
		if err := tx.OAuthStates.Create(ctx, &st); err != nil {
			return err
		}
		if verifier == "" {
			return nil
		}
		enc, err := s.aead.SealString(verifier, crypto.OAuthStatePKCEVerifierAD(st.ID))
		if err != nil {
			return err
		}
		return tx.OAuthStates.SetPKCEVerifier(ctx, st.ID, enc)
	})
	if err != nil {
		return nil, err
	}
	return &begun{state: state.Plain, binding: binding, url: cfg.authCodeURL(s.redirectURI(), state.Plain, nonce, verifier)}, nil
}

// BeginClient starts a RustDesk client login (POST /api/oidc/auth). The
// returned code is the poll code (= the OAuth state).
func (s *Service) BeginClient(ctx context.Context, slug string, dev ClientDevice) (code, authURL string, err error) {
	b, err := s.begin(ctx, slug, store.OAuthState{Action: store.OAuthLogin, Flow: store.OAuthFlowClient,
		DeviceRustdeskID: dev.ID, DeviceUUID: dev.UUID, DeviceName: dev.Name, DeviceOS: dev.OS, DeviceType: dev.Type})
	if err != nil {
		return "", "", err
	}
	return b.state, b.url, nil
}

// BindingCookie binds a panel flow to the browser that started it.
const BindingCookie = "rdpanel_oauth"

// BeginWebLogin starts a panel login and sets the browser-binding cookie.
// It returns the IdP URL the browser must navigate to.
func (s *Service) BeginWebLogin(ctx context.Context, w http.ResponseWriter, slug, next string) (string, error) {
	b, err := s.begin(ctx, slug, store.OAuthState{Action: store.OAuthLogin, Flow: store.OAuthFlowWeb,
		DeviceType: loginlog.ClientWebAdmin, RedirectPath: SafeRedirectPath(next, "/")})
	if err != nil {
		return "", err
	}
	s.setBinding(w, b.binding)
	return b.url, nil
}

// BeginBind starts linking an identity of provider slug to userID.
func (s *Service) BeginBind(ctx context.Context, w http.ResponseWriter, slug string, userID int64, next string) (string, error) {
	b, err := s.begin(ctx, slug, store.OAuthState{Action: store.OAuthBind, Flow: store.OAuthFlowWeb,
		DeviceType: loginlog.ClientWebAdmin, InitiatorUserID: &userID, RedirectPath: SafeRedirectPath(next, DefaultBindReturn)})
	if err != nil {
		return "", err
	}
	s.setBinding(w, b.binding)
	return b.url, nil
}

func (s *Service) setBinding(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: BindingCookie, Value: value, Path: "/api/", MaxAge: int((s.stateTTL + HandoffTTL) / time.Second),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearBinding(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: BindingCookie, Value: "", Path: "/api/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

// DefaultBindReturn is where the browser returns after linking an identity.
const DefaultBindReturn = "/my/profile"

// SafeRedirectPath returns p when it is a same-origin relative path
// ("/x?y#z", not "//host", "/\host" or anything with a scheme), else def.
func SafeRedirectPath(p, def string) string {
	if p == "" || len(p) > 1024 || p[0] != '/' {
		return def
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == '\\') {
		return def
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return def
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return def
	}
	return p
}

// withQuery appends key=value to a relative path.
func withQuery(p, key, value string) string {
	frag := ""
	if i := strings.IndexByte(p, '#'); i >= 0 {
		p, frag = p[:i], p[i:]
	}
	sep := "?"
	if strings.Contains(p, "?") {
		sep = "&"
	}
	return p + sep + url.QueryEscape(key) + "=" + url.QueryEscape(value) + frag
}
