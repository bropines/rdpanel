package oidc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// GoogleIssuer is the fixed issuer of kind "google" providers.
const GoogleIssuer = "https://accounts.google.com"

// GitHubEndpoints are the OAuth endpoints of GitHub (overridable in tests).
type GitHubEndpoints struct {
	AuthURL  string
	TokenURL string
	APIURL   string // base for /user and /user/emails
}

// DefaultGitHub are the public github.com endpoints.
var DefaultGitHub = GitHubEndpoints{
	AuthURL:  "https://github.com/login/oauth/authorize",
	TokenURL: "https://github.com/login/oauth/access_token",
	APIURL:   "https://api.github.com",
}

// Profile is the identity asserted by a provider after a successful login.
type Profile struct {
	Subject       string `json:"subject"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified"`
	Username      string `json:"username,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
}

// providerConfig is a provider row resolved to concrete endpoints.
type providerConfig struct {
	row          *store.IdentityProvider
	clientSecret string
	issuer       string // OIDC kinds only
	authURL      string
	tokenURL     string
	userinfoURL  string
	jwksURI      string
	basicAuth    bool // client_secret_basic (else client_secret_post)
	scopes       []string
}

// SupportedKind reports whether kind can be used for logins.
func SupportedKind(k store.ProviderKind) bool {
	return k == store.ProviderGitHub || k == store.ProviderGoogle || k == store.ProviderOIDC
}

func (s *Service) issuerOf(p *store.IdentityProvider) string {
	if p.Kind == store.ProviderGoogle {
		return s.googleIssuer
	}
	return strings.TrimSpace(p.Issuer)
}

// resolve turns a provider row into endpoints, running discovery for OIDC kinds.
func (s *Service) resolve(ctx context.Context, p *store.IdentityProvider) (*providerConfig, error) {
	if !SupportedKind(p.Kind) {
		return nil, fmt.Errorf("provider kind %q is not supported", p.Kind)
	}
	cfg := &providerConfig{row: p}
	if len(p.ClientSecretEnc) > 0 {
		sec, err := s.aead.OpenString(p.ClientSecretEnc, store.ProviderSecretAD(p.ID))
		if err != nil {
			return nil, errors.New("cannot decrypt client secret")
		}
		cfg.clientSecret = sec
	}
	switch p.Kind {
	case store.ProviderGitHub:
		cfg.authURL, cfg.tokenURL = s.github.AuthURL, s.github.TokenURL
		cfg.scopes = []string{"read:user", "user:email"}
	default:
		cfg.issuer = s.issuerOf(p)
		m, err := s.cache.discover(ctx, s.hc, cfg.issuer, false, s.now())
		if err != nil {
			return nil, err
		}
		cfg.authURL, cfg.tokenURL, cfg.userinfoURL, cfg.jwksURI = m.AuthorizationEndpoint, m.TokenEndpoint,
			m.UserinfoEndpoint, m.JWKSURI
		cfg.basicAuth = len(m.TokenEndpointAuthMethods) == 0 || slices.Contains(m.TokenEndpointAuthMethods, "client_secret_basic")
		cfg.scopes = p.Scopes
		if len(cfg.scopes) == 0 {
			cfg.scopes = []string{"openid", "profile", "email"}
		}
		if !slices.Contains(cfg.scopes, "openid") {
			cfg.scopes = append([]string{"openid"}, cfg.scopes...)
		}
	}
	return cfg, nil
}

// Discover runs (uncached) discovery for a provider row: the admin "test" action.
func (s *Service) Discover(ctx context.Context, p *store.IdentityProvider) (*Metadata, error) {
	switch p.Kind {
	case store.ProviderGitHub:
		return &Metadata{Issuer: "https://github.com", AuthorizationEndpoint: s.github.AuthURL,
			TokenEndpoint: s.github.TokenURL, UserinfoEndpoint: s.github.APIURL + "/user"}, nil
	case store.ProviderGoogle, store.ProviderOIDC:
		return s.cache.discover(ctx, s.hc, s.issuerOf(p), true, s.now())
	default:
		return nil, fmt.Errorf("provider kind %q is not supported", p.Kind)
	}
}

// pkceChallenge returns the S256 code challenge of verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *providerConfig) authCodeURL(redirectURI, state, nonce, verifier string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.row.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(c.scopes, " "))
	q.Set("state", state)
	if c.row.Kind != store.ProviderGitHub && nonce != "" {
		q.Set("nonce", nonce)
	}
	if verifier != "" {
		if c.row.PKCEMethod == "plain" {
			q.Set("code_challenge", verifier)
			q.Set("code_challenge_method", "plain")
		} else {
			q.Set("code_challenge", pkceChallenge(verifier))
			q.Set("code_challenge_method", "S256")
		}
	}
	sep := "?"
	if strings.Contains(c.authURL, "?") {
		sep = "&"
	}
	return c.authURL + sep + q.Encode()
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// exchange trades an authorization code for tokens.
func (s *Service) exchange(ctx context.Context, c *providerConfig, code, redirectURI, verifier string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	if !c.basicAuth {
		form.Set("client_id", c.row.ClientID)
		form.Set("client_secret", c.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.basicAuth {
		// RFC 6749 §2.3.1: credentials are form-urlencoded before base64.
		req.SetBasicAuth(url.QueryEscape(c.row.ClientID), url.QueryEscape(c.clientSecret))
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token endpoint: HTTP %d, unparseable response", resp.StatusCode)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token endpoint: %s", tr.Error)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint: HTTP %d without access token", resp.StatusCode)
	}
	return &tr, nil
}

// flexBool accepts JSON true/false and the strings "true"/"false"
// (some IdPs encode email_verified as a string).
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(data)), `"`)
	v, err := strconv.ParseBool(s)
	if err != nil {
		*b = false
		return nil //nolint:nilerr // unknown encodings mean "not verified"
	}
	*b = flexBool(v)
	return nil
}

type idClaims struct {
	jwt.RegisteredClaims
	Nonce             string   `json:"nonce"`
	AZP               string   `json:"azp"`
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Picture           string   `json:"picture"`
}

var idTokenAlgs = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}

// verifyIDToken checks signature (asymmetric algorithms only), issuer,
// audience, azp, expiry, iat and nonce.
func (s *Service) verifyIDToken(ctx context.Context, c *providerConfig, raw, nonce string) (*idClaims, error) {
	var claims idClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return s.cache.keyFor(ctx, s.hc, c.jwksURI, kid, s.now())
	},
		jwt.WithValidMethods(idTokenAlgs),
		jwt.WithAudience(c.row.ClientID),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(time.Minute),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return nil, fmt.Errorf("id_token: %w", err)
	}
	iss := claims.Issuer
	if iss != c.issuer && strings.TrimRight(iss, "/") != strings.TrimRight(c.issuer, "/") {
		return nil, errors.New("id_token: issuer mismatch")
	}
	if len(claims.Audience) > 1 && claims.AZP != c.row.ClientID {
		return nil, errors.New("id_token: azp mismatch")
	}
	if nonce == "" || !crypto.EqualString(claims.Nonce, nonce) {
		return nil, errors.New("id_token: nonce mismatch")
	}
	if claims.Subject == "" {
		return nil, errors.New("id_token: missing sub")
	}
	return &claims, nil
}

// fetchProfile completes the login at the provider and returns the identity.
func (s *Service) fetchProfile(ctx context.Context, c *providerConfig, code, verifier, nonce string) (*Profile, error) {
	tr, err := s.exchange(ctx, c, code, s.redirectURI(), verifier)
	if err != nil {
		return nil, err
	}
	if c.row.Kind == store.ProviderGitHub {
		return s.githubProfile(ctx, tr.AccessToken)
	}
	if tr.IDToken == "" {
		return nil, errors.New("token endpoint returned no id_token")
	}
	claims, err := s.verifyIDToken(ctx, c, tr.IDToken, nonce)
	if err != nil {
		return nil, err
	}
	p := &Profile{Subject: claims.Subject, Email: claims.Email, EmailVerified: bool(claims.EmailVerified),
		Username: claims.PreferredUsername, DisplayName: claims.Name, AvatarURL: claims.Picture}
	if c.userinfoURL != "" {
		var ui struct {
			Sub               string   `json:"sub"`
			Email             string   `json:"email"`
			EmailVerified     flexBool `json:"email_verified"`
			Name              string   `json:"name"`
			PreferredUsername string   `json:"preferred_username"`
			Picture           string   `json:"picture"`
		}
		if err := getJSON(ctx, s.hc, c.userinfoURL, map[string]string{"Authorization": "Bearer " + tr.AccessToken}, &ui); err == nil {
			// OIDC Core §5.3.2: userinfo sub must equal the ID token sub.
			if ui.Sub != claims.Subject {
				return nil, errors.New("userinfo: sub mismatch")
			}
			if p.Email == "" && ui.Email != "" {
				p.Email, p.EmailVerified = ui.Email, bool(ui.EmailVerified)
			}
			p.Username = firstNonEmpty(p.Username, ui.PreferredUsername)
			p.DisplayName = firstNonEmpty(p.DisplayName, ui.Name)
			p.AvatarURL = firstNonEmpty(p.AvatarURL, ui.Picture)
		}
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	if p.Email == "" {
		p.EmailVerified = false
	}
	return p, nil
}

func (s *Service) githubProfile(ctx context.Context, accessToken string) (*Profile, error) {
	hdr := map[string]string{"Authorization": "Bearer " + accessToken, "Accept": "application/vnd.github+json"}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, s.hc, s.github.APIURL+"/user", hdr, &u); err != nil {
		return nil, fmt.Errorf("github user: %w", err)
	}
	if u.ID <= 0 {
		return nil, errors.New("github user: missing id")
	}
	p := &Profile{Subject: strconv.FormatInt(u.ID, 10), Username: u.Login, DisplayName: u.Name, AvatarURL: u.AvatarURL}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, s.hc, s.github.APIURL+"/user/emails", hdr, &emails); err == nil {
		for _, e := range emails {
			if e.Primary {
				p.Email, p.EmailVerified = strings.ToLower(strings.TrimSpace(e.Email)), e.Verified
				break
			}
		}
	}
	if p.Email == "" {
		p.EmailVerified = false
	}
	return p, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
