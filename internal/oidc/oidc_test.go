package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/oidc"
	"github.com/bropines/rustdesk-panel/internal/oidc/oidctest"
	"github.com/bropines/rustdesk-panel/internal/store"
)

const publicURL = "https://desk.example.test"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t        *testing.T
	st       *store.Store
	clk      *clock
	svc      *oidc.Service
	idp      *oidctest.IdP
	gh       *oidctest.IdP
	prov     *store.IdentityProvider
	ghProv   *store.IdentityProvider
	h        http.Handler
	jwt      *auth.ClientJWT
	webUsers []*store.User
}

func newEnv(t *testing.T, mutate func(p *store.IdentityProvider)) *env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	st := store.New(d).WithClock(clk.now)
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	jwtSvc, _ := auth.NewClientJWT([]byte("jwt-key"), clk.now)

	e := &env{t: t, st: st, clk: clk, jwt: jwtSvc}
	e.idp = oidctest.New(t, "panel-client", "s3cr3t")
	e.idp.Now = clk.now
	e.gh = oidctest.NewGitHub(t, "gh-client", "gh-secret")

	e.prov = &store.IdentityProvider{Slug: "authentik", Kind: store.ProviderOIDC, DisplayName: "Authentik",
		ClientID: "panel-client", Issuer: e.idp.Issuer(), PKCEEnabled: true, PKCEMethod: "S256", Enabled: true}
	if mutate != nil {
		mutate(e.prov)
	}
	e.addProvider(aead, e.prov, "s3cr3t")
	e.ghProv = &store.IdentityProvider{Slug: "github", Kind: store.ProviderGitHub, ClientID: "gh-client",
		PKCEEnabled: true, PKCEMethod: "S256", Enabled: true, AutoRegister: true, SortOrder: 1}
	e.addProvider(aead, e.ghProv, "gh-secret")

	e.svc = oidc.NewService(oidc.Options{Store: st, AEAD: aead, JWT: jwtSvc, PublicURL: publicURL,
		TokenLifetime: time.Hour, JWTLifetime: time.Hour,
		GitHub: &oidc.GitHubEndpoints{AuthURL: e.gh.Server.URL + "/login/oauth/authorize",
			TokenURL: e.gh.Server.URL + "/login/oauth/access_token", APIURL: e.gh.Server.URL},
		WebLogin: func(w http.ResponseWriter, r *http.Request, u *store.User, next string) {
			e.webUsers = append(e.webUsers, u)
			http.Redirect(w, r, next, http.StatusFound)
		},
	})
	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) { oidc.Register(api, oidc.RouteDeps{Service: e.svc}) })
	e.h = r
	return e
}

func (e *env) addProvider(aead *crypto.AEAD, p *store.IdentityProvider, secret string) {
	ctx := context.Background()
	if err := e.st.IdentityProviders.Create(ctx, p); err != nil {
		e.t.Fatal(err)
	}
	enc, err := aead.SealString(secret, store.ProviderSecretAD(p.ID))
	if err != nil {
		e.t.Fatal(err)
	}
	p.ClientSecretEnc = enc
	if err := e.st.IdentityProviders.Update(ctx, p); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) do(method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) user(name, email string) *store.User {
	u := &store.User{Username: name, Email: email}
	if err := e.st.Users.Create(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// startClient runs POST /api/oidc/auth and returns code and url.
func (e *env) startClient(op string) (string, string) {
	e.t.Helper()
	rec := e.do("POST", "/api/oidc/auth", `{"op":"`+op+`","id":"123456789","uuid":"dXVpZA==",
		"deviceInfo":{"os":"windows","type":"client","name":"HOST"},"apiDomain":"https://evil.example"}`)
	var resp map[string]string
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &resp) != nil || resp["code"] == "" {
		e.t.Fatalf("oidc/auth: %d %s", rec.Code, rec.Body.String())
	}
	u, err := url.Parse(resp["url"])
	if err != nil || !u.IsAbs() {
		e.t.Fatalf("url not absolute: %q", resp["url"])
	}
	if got := u.Query().Get("redirect_uri"); got != publicURL+"/api/oidc/callback" {
		e.t.Fatalf("redirect_uri %q (apiDomain must be ignored)", got)
	}
	return resp["code"], resp["url"]
}

func (e *env) callback(q url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return e.do("GET", "/api/oidc/callback?"+q.Encode(), "", cookies...)
}

func (e *env) poll(code, id, uuid string) string {
	e.t.Helper()
	q := url.Values{"code": {code}, "id": {id}, "uuid": {uuid}}
	rec := e.do("GET", "/api/oidc/auth-query?"+q.Encode(), "")
	if rec.Code != 200 {
		e.t.Fatalf("auth-query status %d", rec.Code)
	}
	return strings.TrimSpace(rec.Body.String())
}

// clientLogin runs the whole client flow up to the callback.
func (e *env) clientLogin(op string, id oidctest.Identity) (code string, cb *httptest.ResponseRecorder) {
	e.t.Helper()
	code, authURL := e.startClient(op)
	idp := e.idp
	if op == "github" {
		idp = e.gh
	}
	q, err := idp.Authorize(authURL, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return code, e.callback(q)
}

const pendingBody = `{"error":"No authed oidc is found"}`
const invalidBody = `{"error":"Invalid or expired code"}`

func TestLoginOptions(t *testing.T) {
	e := newEnv(t, nil)
	rec := e.do("GET", "/api/login-options", "")
	want := `["common-oidc/[{\"name\":\"authentik\"},{\"name\":\"github\"}]","oidc/authentik","oidc/github"]`
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != want {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	e.prov.Enabled = false
	_ = e.st.IdentityProviders.Update(context.Background(), e.prov)
	e.ghProv.Enabled = false
	_ = e.st.IdentityProviders.Update(context.Background(), e.ghProv)
	rec = e.do("GET", "/api/login-options", "")
	if strings.TrimSpace(rec.Body.String()) != `[]` {
		t.Fatalf("empty: %s", rec.Body.String())
	}
}

func TestClientFlowEndToEnd(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice := e.user("alice", "alice@example.test")
	if err := e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: alice.ID, ProviderID: e.prov.ID, Subject: "sub-alice"}); err != nil {
		t.Fatal(err)
	}

	code, authURL := e.startClient("authentik")
	if got := e.poll(code, "123456789", "dXVpZA=="); got != pendingBody {
		t.Fatalf("pending: %s", got)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" || u.Query().Get("nonce") == "" {
		t.Fatalf("pkce/nonce missing: %s", authURL)
	}
	q, err := e.idp.Authorize(authURL, oidctest.Identity{Subject: "sub-alice", Name: "Alice A."})
	if err != nil {
		t.Fatal(err)
	}
	cb := e.callback(q)
	if cb.Code != 200 || !strings.Contains(cb.Body.String(), "Login successful") {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body.String())
	}
	// Replayed callback (same state) is refused.
	if rec := e.callback(q); rec.Code != 400 {
		t.Fatalf("replay: %d", rec.Code)
	}
	// Wrong device: refused without consuming the code.
	if got := e.poll(code, "123456789", "b3RoZXI="); got != invalidBody {
		t.Fatalf("foreign uuid: %s", got)
	}
	if got := e.poll(code, "999", "dXVpZA=="); got != invalidBody {
		t.Fatalf("foreign id: %s", got)
	}
	got := e.poll(code, "123456789", "dXVpZA==")
	var body struct {
		AccessToken string         `json:"access_token"`
		Type        string         `json:"type"`
		User        map[string]any `json:"user"`
	}
	if err := json.Unmarshal([]byte(got), &body); err != nil || body.Type != "access_token" || body.AccessToken == "" {
		t.Fatalf("success: %s", got)
	}
	if body.User["name"] != "alice" || body.User["status"] != float64(1) || body.User["is_admin"] != false {
		t.Fatalf("user: %v", body.User)
	}
	info, ok := body.User["info"].(map[string]any)
	if !ok || info["email_verification"] != false || info["email_alarm_notification"] != false {
		t.Fatalf("info must be an object: %v", body.User["info"])
	}
	claims, err := e.jwt.Verify(body.AccessToken)
	if err != nil || int64(claims.UserID) != alice.ID {
		t.Fatalf("jwt: %v %+v", err, claims)
	}
	tok, err := e.st.AuthTokens.GetActive(ctx, store.TokenClient, crypto.HashToken(body.AccessToken))
	if err != nil || tok.DeviceRustdeskID != "123456789" || tok.DeviceUUID != "dXVpZA==" || tok.ClientPlatform != "windows" {
		t.Fatalf("token row: %v %+v", err, tok)
	}
	// The code is single use.
	if got := e.poll(code, "123456789", "dXVpZA=="); got != invalidBody {
		t.Fatalf("second poll: %s", got)
	}
	var n int
	_ = e.st.Querier().QueryRowContext(ctx, `SELECT count(*) FROM login_events WHERE method='oidc' AND result='success' AND user_id=?`, alice.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("login events = %d", n)
	}
}

func TestClientFlowUnknownProviderAndValidation(t *testing.T) {
	e := newEnv(t, nil)
	rec := e.do("POST", "/api/oidc/auth", `{"op":"nope","id":"1","uuid":"u"}`)
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("%s", rec.Body.String())
	}
	rec = e.do("POST", "/api/oidc/auth", `{"op":"authentik"}`)
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("missing device: %s", rec.Body.String())
	}
	if got := e.poll("garbage", "1", "u"); got != invalidBody {
		t.Fatalf("garbage code: %s", got)
	}
}

func TestStateExpires(t *testing.T) {
	e := newEnv(t, nil)
	e.user("alice", "")
	code, authURL := e.startClient("authentik")
	e.clk.advance(6 * time.Minute)
	if got := e.poll(code, "123456789", "dXVpZA=="); got != invalidBody {
		t.Fatalf("expired poll: %s", got)
	}
	q, _ := e.idp.Authorize(authURL, oidctest.Identity{Subject: "x"})
	if rec := e.callback(q); rec.Code != 400 {
		t.Fatalf("expired callback: %d", rec.Code)
	}
}

func pollResult(t *testing.T, e *env, code string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(e.poll(code, "123456789", "dXVpZA==")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLinkingRules(t *testing.T) {
	type tc struct {
		name       string
		linkEmail  bool
		autoReg    bool
		verified   bool
		wantUser   string // "existing", "new", "" (error)
		wantErrSub string
	}
	cases := []tc{
		{"verified email links when enabled", true, false, true, "existing", ""},
		{"unverified email never links", true, false, false, "", "No account is linked"},
		{"unverified email + auto-register creates a new user", true, true, false, "new", ""},
		{"verified email but linking disabled", false, false, true, "", "No account is linked"},
		{"linking disabled + auto-register creates new user", false, true, true, "new", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(p *store.IdentityProvider) {
				p.LinkByVerifiedEmail, p.AutoRegister = c.linkEmail, c.autoReg
			})
			existing := e.user("bob", "Bob@Example.test")
			code, cb := e.clientLogin("authentik", oidctest.Identity{Subject: "sub-bob", Email: "bob@example.test",
				EmailVerified: c.verified, PreferredUsername: "bob", Name: "Bob"})
			if cb.Code != 200 {
				t.Fatalf("callback %d", cb.Code)
			}
			m := pollResult(t, e, code)
			ctx := context.Background()
			ident, identErr := e.st.UserIdentities.GetByProviderSubject(ctx, e.prov.ID, "sub-bob")
			switch c.wantUser {
			case "":
				if s, _ := m["error"].(string); !strings.Contains(s, c.wantErrSub) {
					t.Fatalf("want error %q, got %v", c.wantErrSub, m)
				}
				if identErr == nil {
					t.Fatal("identity must not be linked")
				}
			case "existing":
				if m["access_token"] == nil || identErr != nil || ident.UserID != existing.ID {
					t.Fatalf("want link to existing: %v %v", m, identErr)
				}
			case "new":
				if m["access_token"] == nil || identErr != nil || ident.UserID == existing.ID {
					t.Fatalf("want new user: %v %v", m, identErr)
				}
				nu, _ := e.st.Users.GetByID(ctx, ident.UserID)
				if nu.Username == "bob" || nu.HasPassword() {
					t.Fatalf("new user %+v", nu)
				}
				if !c.verified && nu.Email != "" {
					t.Fatal("unverified email must not be copied to users.email")
				}
			}
		})
	}
}

func TestDisabledUserCannotLogin(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	u := e.user("carol", "")
	_ = e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: u.ID, ProviderID: e.prov.ID, Subject: "c"})
	u.Status = store.UserDisabled
	_ = e.st.Users.Update(ctx, u)
	code, _ := e.clientLogin("authentik", oidctest.Identity{Subject: "c"})
	if m := pollResult(t, e, code); m["error"] != "This account is disabled." {
		t.Fatalf("%v", m)
	}
}

func TestBadIDTokens(t *testing.T) {
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	for name, setup := range map[string]func(e *env){
		"wrong signing key": func(e *env) { e.idp.SignWith = other },
		"nonce mismatch":    func(e *env) { e.idp.NonceOverride = "attacker-nonce" },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			ctx := context.Background()
			u := e.user("dave", "")
			_ = e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: u.ID, ProviderID: e.prov.ID, Subject: "d"})
			setup(e)
			code, cb := e.clientLogin("authentik", oidctest.Identity{Subject: "d"})
			if !strings.Contains(cb.Body.String(), "Login failed") {
				t.Fatalf("callback: %s", cb.Body.String())
			}
			if m := pollResult(t, e, code); m["access_token"] != nil || m["error"] == nil {
				t.Fatalf("%v", m)
			}
		})
	}
}

func TestIdPDenied(t *testing.T) {
	e := newEnv(t, nil)
	code, authURL := e.startClient("authentik")
	u, _ := url.Parse(authURL)
	rec := e.callback(url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "cancelled or denied") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if m := pollResult(t, e, code); !strings.Contains(m["error"].(string), "denied") {
		t.Fatalf("%v", m)
	}
}

func TestGitHubFlow(t *testing.T) {
	e := newEnv(t, nil)
	code, cb := e.clientLogin("github", oidctest.Identity{GitHubID: 4242, GitHubLogin: "Octo Cat",
		Email: "octo@example.test", EmailVerified: true, Name: "Octo"})
	if cb.Code != 200 || !strings.Contains(cb.Body.String(), "Login successful") {
		t.Fatalf("callback: %d %s", cb.Code, cb.Body.String())
	}
	m := pollResult(t, e, code)
	if m["access_token"] == nil {
		t.Fatalf("%v", m)
	}
	ctx := context.Background()
	ident, err := e.st.UserIdentities.GetByProviderSubject(ctx, e.ghProv.ID, "4242")
	if err != nil || !ident.EmailVerified || ident.Email != "octo@example.test" {
		t.Fatalf("%v %+v", err, ident)
	}
	u, _ := e.st.Users.GetByID(ctx, ident.UserID)
	if u.Username != "octocat" || u.Email != "octo@example.test" {
		t.Fatalf("user %+v", u)
	}
}

func TestWebFlowBrowserBinding(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	u := e.user("erin", "")
	_ = e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: u.ID, ProviderID: e.prov.ID, Subject: "erin"})

	start := func() (string, *http.Cookie) {
		rec := httptest.NewRecorder()
		authURL, err := e.svc.BeginWebLogin(ctx, rec, "authentik", "/devices?x=1")
		if err != nil {
			t.Fatal(err)
		}
		var c *http.Cookie
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == oidc.BindingCookie {
				c = ck
			}
		}
		if c == nil || !c.HttpOnly {
			t.Fatal("binding cookie missing")
		}
		return authURL, c
	}
	// Without the binding cookie (login CSRF): refused.
	authURL, _ := start()
	q, _ := e.idp.Authorize(authURL, oidctest.Identity{Subject: "erin"})
	rec := e.callback(q)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=oidc_session_mismatch" {
		t.Fatalf("no cookie: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// With it: WebLogin is called with the safe redirect path.
	authURL, c := start()
	q, _ = e.idp.Authorize(authURL, oidctest.Identity{Subject: "erin"})
	rec = e.callback(q, c)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/devices?x=1" || len(e.webUsers) != 1 || e.webUsers[0].ID != u.ID {
		t.Fatalf("with cookie: %d %s %v", rec.Code, rec.Header().Get("Location"), e.webUsers)
	}
	// Open redirect attempt falls back to "/".
	rec2 := httptest.NewRecorder()
	authURL, err := e.svc.BeginWebLogin(ctx, rec2, "authentik", "//evil.example/x")
	if err != nil {
		t.Fatal(err)
	}
	q, _ = e.idp.Authorize(authURL, oidctest.Identity{Subject: "erin"})
	rec = e.callback(q, rec2.Result().Cookies()[0])
	if rec.Header().Get("Location") != "/" {
		t.Fatalf("open redirect: %s", rec.Header().Get("Location"))
	}
}

func TestBindFlow(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	frank := e.user("frank", "")
	other := e.user("other", "")
	_ = e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: other.ID, ProviderID: e.prov.ID, Subject: "taken"})

	bind := func(sub string) string {
		rec := httptest.NewRecorder()
		authURL, err := e.svc.BeginBind(ctx, rec, "authentik", frank.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		q, _ := e.idp.Authorize(authURL, oidctest.Identity{Subject: sub, Email: "f@example.test", EmailVerified: true})
		return e.callback(q, rec.Result().Cookies()[0]).Header().Get("Location")
	}
	if loc := bind("taken"); loc != "/my/security?link_error=already_linked" {
		t.Fatalf("taken: %s", loc)
	}
	if loc := bind("frank-sub"); loc != "/my/security?linked=authentik" {
		t.Fatalf("bind: %s", loc)
	}
	ident, err := e.st.UserIdentities.GetByProviderSubject(ctx, e.prov.ID, "frank-sub")
	if err != nil || ident.UserID != frank.ID {
		t.Fatalf("%v %+v", err, ident)
	}
	if loc := bind("frank-second"); loc != "/my/security?link_error=already_linked" {
		t.Fatalf("second identity of same provider: %s", loc)
	}
}

func TestSafeRedirectPath(t *testing.T) {
	for in, want := range map[string]string{
		"/devices":             "/devices",
		"/a?b=c#d":             "/a?b=c#d",
		"":                     "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"javascript:alert(1)":  "/",
		"devices":              "/",
		"/a\nb":                "/",
	} {
		if got := oidc.SafeRedirectPath(in, "/"); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestDiscoverIssuerMismatch(t *testing.T) {
	e := newEnv(t, nil)
	p := *e.prov
	p.Issuer = e.idp.Issuer() + "/other"
	if _, err := e.svc.Discover(context.Background(), &p); err == nil {
		t.Fatal("expected discovery failure")
	}
	m, err := e.svc.Discover(context.Background(), e.prov)
	if err != nil || m.TokenEndpoint != e.idp.Issuer()+"/token" {
		t.Fatalf("%v %+v", err, m)
	}
}

func TestNotConfiguredWithoutPublicURL(t *testing.T) {
	e := newEnv(t, nil)
	svc := oidc.NewService(oidc.Options{Store: e.st, JWT: e.jwt})
	if _, _, err := svc.BeginClient(context.Background(), "authentik", oidc.ClientDevice{ID: "1", UUID: "u"}); err != oidc.ErrNotConfigured {
		t.Fatalf("%v", err)
	}
}
