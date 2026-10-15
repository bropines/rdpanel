// Package oidctest provides fake identity providers (generic OIDC with
// discovery + signed ID tokens, and GitHub) for tests.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Identity is what the fake IdP asserts for the next login.
type Identity struct {
	Subject           string
	Email             string
	EmailVerified     bool
	Name              string
	PreferredUsername string
	// GitHub only.
	GitHubID    int64
	GitHubLogin string
}

type grant struct {
	id          Identity
	nonce       string
	challenge   string
	method      string
	redirectURI string
	used        bool
}

// IdP is a fake OpenID Provider (or GitHub when created with NewGitHub).
type IdP struct {
	Server       *httptest.Server
	Key          *rsa.PrivateKey
	KID          string
	ClientID     string
	ClientSecret string
	// Now is the clock for token timestamps (default time.Now).
	Now func() time.Time

	// SignWith, when set, signs ID tokens with another key (bad signature tests).
	SignWith *rsa.PrivateKey
	// NonceOverride, when set, replaces the nonce in ID tokens.
	NonceOverride string

	github bool
	mu     sync.Mutex
	grants map[string]*grant
	access map[string]Identity
	n      int
	// TokenRequests counts token endpoint calls.
	TokenRequests int
}

// New starts a fake generic OIDC provider.
func New(t testing.TB, clientID, clientSecret string) *IdP {
	return start(t, clientID, clientSecret, false)
}

// NewGitHub starts a fake GitHub OAuth app endpoint set.
func NewGitHub(t testing.TB, clientID, clientSecret string) *IdP {
	return start(t, clientID, clientSecret, true)
}

func start(t testing.TB, clientID, clientSecret string, github bool) *IdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &IdP{Key: key, KID: "test-key-1", ClientID: clientID, ClientSecret: clientSecret, Now: time.Now,
		github: github, grants: map[string]*grant{}, access: map[string]Identity{}}
	mux := http.NewServeMux()
	if github {
		mux.HandleFunc("/login/oauth/access_token", f.token)
		mux.HandleFunc("/user", f.githubUser)
		mux.HandleFunc("/user/emails", f.githubEmails)
	} else {
		mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
		mux.HandleFunc("/jwks", f.jwks)
		mux.HandleFunc("/token", f.token)
		mux.HandleFunc("/userinfo", f.userinfo)
	}
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// Issuer is the issuer URL (OIDC).
func (f *IdP) Issuer() string { return f.Server.URL }

func (f *IdP) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                f.Issuer(),
		"authorization_endpoint":                f.Issuer() + "/authorize",
		"token_endpoint":                        f.Issuer() + "/token",
		"userinfo_endpoint":                     f.Issuer() + "/userinfo",
		"jwks_uri":                              f.Issuer() + "/jwks",
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256", "plain"},
	})
}

func (f *IdP) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := f.Key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": f.KID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// Authorize simulates the user approving the login at authURL (the URL the
// service returned) and returns the callback query (state, code).
func (f *IdP) Authorize(authURL string, id Identity) (url.Values, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if q.Get("client_id") != f.ClientID {
		return nil, errors.New("client_id mismatch")
	}
	if q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("redirect_uri") == "" {
		return nil, errors.New("bad authorization request")
	}
	if !f.github && q.Get("nonce") == "" {
		return nil, errors.New("missing nonce")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	code := "code-" + strconv.Itoa(f.n)
	f.grants[code] = &grant{id: id, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"),
		method: q.Get("code_challenge_method"), redirectURI: q.Get("redirect_uri")}
	return url.Values{"state": {q.Get("state")}, "code": {code}}, nil
}

func (f *IdP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TokenRequests++
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenErr(w, "invalid_request")
		return
	}
	id, secret, ok := r.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != f.ClientID || secret != f.ClientSecret {
		tokenErr(w, "invalid_client")
		return
	}
	if !f.github && r.PostForm.Get("grant_type") != "authorization_code" {
		tokenErr(w, "unsupported_grant_type")
		return
	}
	g := f.grants[r.PostForm.Get("code")]
	if g == nil || g.used || g.redirectURI != r.PostForm.Get("redirect_uri") {
		tokenErr(w, "invalid_grant")
		return
	}
	g.used = true
	if g.challenge != "" {
		v := r.PostForm.Get("code_verifier")
		want := v
		if g.method == "S256" {
			sum := sha256.Sum256([]byte(v))
			want = base64.RawURLEncoding.EncodeToString(sum[:])
		}
		if v == "" || want != g.challenge {
			tokenErr(w, "invalid_grant")
			return
		}
	}
	f.n++
	at := "at-" + strconv.Itoa(f.n)
	f.access[at] = g.id
	resp := map[string]any{"access_token": at, "token_type": "Bearer"}
	if !f.github {
		resp["id_token"] = f.idToken(g)
	}
	writeJSON(w, resp)
}

func (f *IdP) idToken(g *grant) string {
	now := f.Now()
	nonce := g.nonce
	if f.NonceOverride != "" {
		nonce = f.NonceOverride
	}
	claims := jwt.MapClaims{
		"iss": f.Issuer(), "sub": g.id.Subject, "aud": f.ClientID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce,
		"name": g.id.Name, "preferred_username": g.id.PreferredUsername,
	}
	if g.id.Email != "" {
		claims["email"] = g.id.Email
		claims["email_verified"] = g.id.EmailVerified
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.KID
	key := f.Key
	if f.SignWith != nil {
		key = f.SignWith
	}
	s, err := tok.SignedString(key)
	if err != nil {
		panic(err)
	}
	return s
}

func (f *IdP) identity(r *http.Request) (Identity, bool) {
	at := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.access[at]
	return id, ok
}

func (f *IdP) userinfo(w http.ResponseWriter, r *http.Request) {
	id, ok := f.identity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"sub": id.Subject, "name": id.Name, "preferred_username": id.PreferredUsername})
}

func (f *IdP) githubUser(w http.ResponseWriter, r *http.Request) {
	id, ok := f.identity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"id": id.GitHubID, "login": id.GitHubLogin, "name": id.Name,
		"avatar_url": "https://avatars.example.test/" + id.GitHubLogin})
}

func (f *IdP) githubEmails(w http.ResponseWriter, r *http.Request) {
	id, ok := f.identity(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, []map[string]any{
		{"email": "noreply-" + id.GitHubLogin + "@users.noreply.example.test", "primary": false, "verified": true},
		{"email": id.Email, "primary": true, "verified": id.EmailVerified},
	})
}

func tokenErr(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = fmt.Fprintf(w, `{"error":%q}`, code)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
