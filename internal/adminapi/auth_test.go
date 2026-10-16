package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/oidc"
	"github.com/bropines/rdpanel/internal/oidc/oidctest"
	"github.com/bropines/rdpanel/internal/store"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type tenv struct {
	t    *testing.T
	st   *store.Store
	clk  *testClock
	srv  *httptest.Server
	deps Deps
	aead *crypto.AEAD
	idp  *oidctest.IdP
}

var cheapHasher = &auth.Hasher{Params: auth.Argon2Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}

func newTEnv(t *testing.T) *tenv {
	t.Helper()
	return newTEnvWithConfig(t, nil)
}

// newTEnvWithConfig is newTEnv with a custom configuration (nil: defaults).
func newTEnvWithConfig(t *testing.T, cfg *config.Config) *tenv {
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
	clk := &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	st := store.New(d).WithClock(clk.now)
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	jwtSvc, _ := auth.NewClientJWT([]byte("k"), clk.now)
	if cfg == nil {
		cfg = config.Defaults()
		cfg.PublicURL = "http://panel.test"
		cfg.WebAuthn.Origins = []string{"http://panel.test"}
	}

	e := &tenv{t: t, st: st, clk: clk, aead: aead}
	e.deps = Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: cfg, Store: st, AEAD: aead,
		JWT: jwtSvc, Hasher: cheapHasher,
		Limiter: auth.NewBackoff(auth.BackoffConfig{Free: 3, Base: time.Second, Max: time.Minute, Forget: time.Hour}, clk.now)}
	e.deps.OIDC = oidc.NewService(oidc.Options{Store: st, AEAD: aead, JWT: jwtSvc, PublicURL: cfg.PublicURL,
		WebLogin: OIDCWebLogin(e.deps)})
	e.idp = oidctest.New(t, "cid", "csecret")
	e.idp.Now = clk.now

	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Route("/v1/admin", func(r chi.Router) { Register(r, e.deps) })
		api.With(SessionMiddleware(e.deps), RequireUser).Get("/protected", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		oidc.Register(api, oidc.RouteDeps{Service: e.deps.OIDC})
	})
	e.srv = httptest.NewServer(r)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *tenv) user(name, password string, role store.Role) *store.User {
	e.t.Helper()
	u := &store.User{Username: name, Role: role}
	if password != "" {
		h, algo, err := cheapHasher.Hash(password)
		if err != nil {
			e.t.Fatal(err)
		}
		u.PasswordHash, u.PasswordAlgo = h, string(algo)
	}
	if err := e.st.Users.Create(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

type client struct {
	e      *tenv
	hc     *http.Client
	noCSRF bool
}

func (e *tenv) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, hc: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

func (c *client) cookie(name string) string {
	u, _ := url.Parse(c.e.srv.URL + "/api/")
	for _, ck := range c.hc.Jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}

func (c *client) setCookie(name, value string) {
	u, _ := url.Parse(c.e.srv.URL + "/")
	c.hc.Jar.SetCookies(u, []*http.Cookie{{Name: name, Value: value, Path: "/"}})
}

// do calls an admin API path (or an absolute /api path when it starts with "/api").
func (c *client) do(method, path string, body any) (int, map[string]any, *http.Response) {
	c.e.t.Helper()
	full := c.e.srv.URL + "/api/v1/admin" + path
	if strings.HasPrefix(path, "/api") {
		full = c.e.srv.URL + path
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, full, rd)
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet && !c.noCSRF {
		if c.cookie(CSRFCookie) == "" {
			c.do(http.MethodGet, "/auth/csrf", nil)
		}
		req.Header.Set(CSRFHeader, c.cookie(CSRFCookie))
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m == nil {
		m = map[string]any{"_raw": string(raw)}
	}
	return resp.StatusCode, m, resp
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

func (c *client) login(user, pw string) (int, map[string]any) {
	code, m, _ := c.do(http.MethodPost, "/auth/login", map[string]string{"username": user, "password": pw})
	return code, m
}

func TestCSRFRequired(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.noCSRF = true
	code, m, _ := c.do(http.MethodPost, "/auth/login", map[string]string{"username": "alice", "password": "correct horse"})
	if code != 403 || errCode(m) != "csrf_failed" {
		t.Fatalf("no token: %d %v", code, m)
	}
	c.do(http.MethodGet, "/auth/csrf", nil)
	c.setCookie(CSRFCookie, strings.Repeat("a", 43))
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/admin/auth/login", strings.NewReader(`{}`))
	req.Header.Set(CSRFHeader, strings.Repeat("b", 43))
	resp, _ := c.hc.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("mismatch: %d", resp.StatusCode)
	}
	c.noCSRF = false
	if code, m := c.login("alice", "correct horse"); code != 200 {
		t.Fatalf("with csrf: %d %v", code, m)
	}
}

func TestPasswordLoginMeLogout(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleAdmin)
	c := e.client()
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("anonymous me: %d", code)
	}
	csrfBefore := func() string { c.do(http.MethodGet, "/auth/csrf", nil); return c.cookie(CSRFCookie) }()
	code, m, resp := c.do(http.MethodPost, "/auth/login", map[string]string{"username": "Alice", "password": "correct horse"})
	if code != 200 || m["mfa_required"] != false {
		t.Fatalf("login: %d %v", code, m)
	}
	var sess *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == SessionCookie {
			sess = ck
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie: %+v", sess)
	}
	me := m["me"].(map[string]any)
	if me["user"].(map[string]any)["username"] != "alice" || me["csrf_token"] != c.cookie(CSRFCookie) || me["csrf_token"] == csrfBefore {
		t.Fatalf("me / csrf rotation: %v", me)
	}
	if code, m, _ := c.do(http.MethodGet, "/auth/me", nil); code != 200 || m["user"].(map[string]any)["role"] != "admin" {
		t.Fatalf("me: %d %v", code, m)
	}
	if code, _, _ := c.do(http.MethodGet, "/api/protected", nil); code != http.StatusTeapot {
		t.Fatalf("protected: %d", code)
	}
	if code, _, _ := c.do(http.MethodPost, "/auth/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("me after logout: %d", code)
	}
	var n int
	_ = e.st.Querier().QueryRowContext(context.Background(),
		`SELECT count(*) FROM login_events WHERE result='success' AND method='password' AND client_kind='webadmin'`).Scan(&n)
	if n != 1 {
		t.Fatalf("login events: %d", n)
	}
}

func TestGenericErrorsAndRateLimit(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	e.user("nopw", "", store.RoleUser)
	c := e.client()
	for _, tc := range [][2]string{{"alice", "wrong"}, {"ghost", "whatever"}, {"nopw", "x"}} {
		code, m := c.login(tc[0], tc[1])
		if code != 401 || errCode(m) != "invalid_credentials" {
			t.Fatalf("%v: %d %v", tc, code, m)
		}
		if m["error"].(map[string]any)["message"] != msgBadCredentials {
			t.Fatal("message must be generic")
		}
	}
	c.login("alice", "wrong again") // 4th failure from this IP (Free=3) starts the backoff
	code, m, resp := c.do(http.MethodPost, "/auth/login", map[string]string{"username": "alice", "password": "correct horse"})
	if code != 429 || errCode(m) != "rate_limited" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("rate limit: %d %v", code, m)
	}
	e.clk.advance(2 * time.Second)
	if code, _ := c.login("alice", "correct horse"); code != 200 {
		t.Fatalf("after backoff: %d", code)
	}
	// Per-username limiting across IPs: alice's counter was reset by the success.
	for i := 0; i < 4; i++ {
		c.login("alice", "bad")
	}
	if code, _ := c.login("alice", "bad"); code != 429 {
		t.Fatalf("user limit: %d", code)
	}
}

func enrollTOTP(t *testing.T, e *tenv, c *client) (string, []string) {
	t.Helper()
	code, m, _ := c.do(http.MethodPost, "/me/security/totp/enroll", nil)
	if code != 200 || !strings.HasPrefix(m["otpauth_uri"].(string), "otpauth://totp/") {
		t.Fatalf("enroll: %d %v", code, m)
	}
	secret := m["secret"].(string)
	if code, m, _ := c.do(http.MethodPost, "/me/security/totp/confirm", map[string]string{"code": "000000"}); code != 401 || errCode(m) != "totp_invalid" {
		t.Fatalf("wrong confirm: %d %v", code, m)
	}
	otp, _ := auth.TOTPCode(secret, e.clk.now())
	code, m, _ = c.do(http.MethodPost, "/me/security/totp/confirm", map[string]string{"code": otp})
	if code != 200 {
		t.Fatalf("confirm: %d %v", code, m)
	}
	var codes []string
	for _, v := range m["recovery_codes"].([]any) {
		codes = append(codes, v.(string))
	}
	if len(codes) != auth.RecoveryCodeCount {
		t.Fatalf("codes: %v", codes)
	}
	return secret, codes
}

func TestMFAFlow(t *testing.T) {
	e := newTEnv(t)
	u := e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	secret, recovery := enrollTOTP(t, e, c)
	var enc []byte
	_ = e.st.Querier().QueryRowContext(context.Background(), `SELECT secret_enc FROM totp_secrets WHERE user_id=?`, u.ID).Scan(&enc)
	if bytes.Contains(enc, []byte(secret)) {
		t.Fatal("TOTP secret stored in plain text")
	}
	c.do(http.MethodPost, "/auth/logout", nil)

	code, m := c.login("alice", "correct horse")
	if code != 200 || m["mfa_required"] != true || m["me"] != nil {
		t.Fatalf("login step 1: %d %v", code, m)
	}
	pending := c.cookie(SessionCookie)
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("pending session must not be authenticated: %d", code)
	}
	if code, _, _ := c.do(http.MethodGet, "/api/protected", nil); code != 401 {
		t.Fatalf("pending protected: %d", code)
	}
	if code, m, _ := c.do(http.MethodPost, "/auth/mfa", map[string]string{"code": "123456"}); code != 401 || errCode(m) != "totp_invalid" {
		t.Fatalf("wrong code: %d %v", code, m)
	}
	// The code that confirmed enrolment cannot be replayed.
	otp, _ := auth.TOTPCode(secret, e.clk.now())
	if code, _, _ := c.do(http.MethodPost, "/auth/mfa", map[string]string{"code": otp}); code != 401 {
		t.Fatalf("replayed code accepted: %d", code)
	}
	e.clk.advance(30 * time.Second)
	otp, _ = auth.TOTPCode(secret, e.clk.now())
	code, m, _ = c.do(http.MethodPost, "/auth/mfa", map[string]string{"code": otp})
	if code != 200 || m["me"] == nil {
		t.Fatalf("mfa: %d %v", code, m)
	}
	if c.cookie(SessionCookie) == pending {
		t.Fatal("session not rotated after MFA")
	}
	if _, err := e.st.AuthTokens.GetActive(context.Background(), store.TokenWeb, crypto.HashToken(pending)); err == nil {
		t.Fatal("pending session still active")
	}
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 200 {
		t.Fatalf("me: %d", code)
	}

	// Recovery code works once, also inline in /auth/login.
	c2 := e.client()
	code, m, _ = c2.do(http.MethodPost, "/auth/login", map[string]string{"username": "alice", "password": "correct horse", "totp_code": strings.ToUpper(recovery[0])})
	if code != 200 || m["me"] == nil {
		t.Fatalf("recovery login: %d %v", code, m)
	}
	c3 := e.client()
	code, m, _ = c3.do(http.MethodPost, "/auth/login", map[string]string{"username": "alice", "password": "correct horse", "totp_code": recovery[0]})
	if code != 401 || errCode(m) != "totp_invalid" {
		t.Fatalf("reused recovery code: %d %v", code, m)
	}
	code, m, _ = c.do(http.MethodGet, "/me/security", nil)
	if code != 200 || m["totp_enabled"] != true || m["recovery_codes_remaining"] != float64(auth.RecoveryCodeCount-1) {
		t.Fatalf("security: %v", m)
	}

	// TOTPService (client /api/login verifier) shares state.
	svc := NewTOTPService(e.st, e.aead)
	if ok, _ := svc.Enrolled(context.Background(), uint32(u.ID)); !ok {
		t.Fatal("enrolled")
	}
	e.clk.advance(30 * time.Second)
	otp, _ = auth.TOTPCode(secret, e.clk.now())
	if ok, _ := svc.Verify(context.Background(), uint32(u.ID), otp); !ok {
		t.Fatal("verify")
	}
	if ok, _ := svc.Verify(context.Background(), uint32(u.ID), otp); ok {
		t.Fatal("replay accepted")
	}

	// Disable requires re-authentication.
	if code, _, _ := c.do(http.MethodPost, "/me/security/totp/disable", map[string]string{"password": "nope"}); code != 401 {
		t.Fatalf("disable without reauth: %d", code)
	}
	if code, _, _ := c.do(http.MethodPost, "/me/security/totp/disable", map[string]string{"password": "correct horse"}); code != 204 {
		t.Fatalf("disable: %d", code)
	}
	if code, m := e.client().login("alice", "correct horse"); code != 200 || m["mfa_required"] != false {
		t.Fatalf("login after disable: %v", m)
	}
}

func TestSessionExpiryAndRotation(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	first := c.cookie(SessionCookie)
	c.login("alice", "correct horse")
	if c.cookie(SessionCookie) == first {
		t.Fatal("session not rotated on login")
	}
	if _, err := e.st.AuthTokens.GetActive(context.Background(), store.TokenWeb, crypto.HashToken(first)); err == nil {
		t.Fatal("old session still active after re-login")
	}
	// Idle timeout (12 h) slides with activity...
	for i := 0; i < 20; i++ {
		e.clk.advance(11 * time.Hour)
		if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 200 {
			if e.clk.now().Sub(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)) < 7*24*time.Hour {
				t.Fatalf("expired early at step %d: %d", i, code)
			}
			// ...until the 7 day absolute cap.
			return
		}
	}
	t.Fatal("absolute expiry not enforced")
}

func TestIdleExpiry(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	e.clk.advance(12*time.Hour + time.Minute)
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("idle: %d", code)
	}
}

func TestSessionFixation(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.setCookie(SessionCookie, "attacker-chosen-value")
	c.login("alice", "correct horse")
	if c.cookie(SessionCookie) == "attacker-chosen-value" {
		t.Fatal("server accepted a client-chosen session id")
	}
}

func TestDisabledUserSessionDropped(t *testing.T) {
	e := newTEnv(t)
	u := e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	u.Status = store.UserDisabled
	_ = e.st.Users.Update(context.Background(), u)
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("%d", code)
	}
	if code, _ := e.client().login("alice", "correct horse"); code != 401 {
		t.Fatalf("disabled login: %d", code)
	}
}

func TestForcedEnrollment(t *testing.T) {
	e := newTEnv(t)
	u := e.user("admin", "correct horse", store.RoleAdmin)
	_ = e.st.Settings.Set(context.Background(), store.SettingRequireAdmin2FA, true, nil)
	c := e.client()
	_, m := c.login("admin", "correct horse")
	if m["me"].(map[string]any)["must_enroll_totp"] != true {
		t.Fatalf("%v", m)
	}
	if code, m, _ := c.do(http.MethodGet, "/identity-providers", nil); code != 403 || errCode(m) != "totp_enrollment_required" {
		t.Fatalf("restricted: %d %v", code, m)
	}
	enrollTOTP(t, e, c)
	if code, _, _ := c.do(http.MethodGet, "/identity-providers", nil); code != 200 {
		t.Fatalf("after enrolment: %d", code)
	}
	if code, m, _ := c.do(http.MethodPost, "/me/security/totp/disable", map[string]string{"password": "correct horse"}); code != 403 {
		t.Fatalf("policy must forbid disabling: %d %v", code, m)
	}
	_ = u
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	e := newTEnv(t)
	u := e.user("alice", "correct horse", store.RoleUser)
	a, b := e.client(), e.client()
	a.login("alice", "correct horse")
	b.login("alice", "correct horse")
	ctx := context.Background()
	client := &store.AuthToken{UserID: u.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken("client-tok"),
		ExpiresAt: e.clk.now().Add(time.Hour)}
	_ = e.st.AuthTokens.Create(ctx, client)

	code, m, _ := a.do(http.MethodGet, "/me/security/sessions", nil)
	_ = m
	if code != 200 {
		t.Fatalf("sessions: %d", code)
	}
	if code, _, _ := a.do(http.MethodPost, "/me/security/password", map[string]string{"current_password": "bad", "new_password": "new password 1"}); code != 401 {
		t.Fatalf("wrong current: %d", code)
	}
	if code, _, _ := a.do(http.MethodPost, "/me/security/password", map[string]string{"current_password": "correct horse", "new_password": "short"}); code != 400 {
		t.Fatalf("short: %d", code)
	}
	if code, _, _ := a.do(http.MethodPost, "/me/security/password", map[string]string{"current_password": "correct horse", "new_password": "new password 1"}); code != 204 {
		t.Fatalf("change: %d", code)
	}
	if code, _, _ := a.do(http.MethodGet, "/auth/me", nil); code != 200 {
		t.Fatalf("current session must survive: %d", code)
	}
	if code, _, _ := b.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("other session must be revoked: %d", code)
	}
	if _, err := e.st.AuthTokens.GetActive(ctx, store.TokenClient, crypto.HashToken("client-tok")); err == nil {
		t.Fatal("client token must be revoked")
	}
	if code, _ := e.client().login("alice", "new password 1"); code != 200 {
		t.Fatal("new password")
	}
}

func TestListAndRevokeSessions(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	e.user("bob", "correct horse", store.RoleUser)
	a, a2, bob := e.client(), e.client(), e.client()
	a.login("alice", "correct horse")
	a2.login("alice", "correct horse")
	bob.login("bob", "correct horse")
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/admin/me/security/sessions", nil)
	resp, _ := a.hc.Do(req)
	var env struct{ Items []map[string]any }
	_ = json.NewDecoder(resp.Body).Decode(&env)
	resp.Body.Close()
	list := env.Items
	if len(list) != 2 {
		t.Fatalf("sessions: %v", list)
	}
	var other float64
	for _, s := range list {
		if s["current"] == false {
			other = s["id"].(float64)
		}
	}
	// Bob cannot revoke Alice's sessions.
	if code, _, _ := bob.do(http.MethodDelete, "/me/security/sessions/"+jsonNum(other), nil); code != 404 {
		t.Fatalf("foreign revoke: %d", code)
	}
	if code, _, _ := a.do(http.MethodDelete, "/me/security/sessions/"+jsonNum(other), nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _, _ := a2.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("revoked session still works: %d", code)
	}
}

func jsonNum(f float64) string { b, _ := json.Marshal(int64(f)); return string(b) }

func TestIdentityProvidersCRUD(t *testing.T) {
	e := newTEnv(t)
	e.user("admin", "correct horse", store.RoleAdmin)
	e.user("user", "correct horse", store.RoleUser)
	anon, usr, adm := e.client(), e.client(), e.client()
	usr.login("user", "correct horse")
	adm.login("admin", "correct horse")
	if code, _, _ := anon.do(http.MethodGet, "/identity-providers", nil); code != 401 {
		t.Fatalf("anon: %d", code)
	}
	if code, _, _ := usr.do(http.MethodGet, "/identity-providers", nil); code != 403 {
		t.Fatalf("user: %d", code)
	}
	body := map[string]any{"slug": "authentik", "kind": "oidc", "display_name": "Authentik", "client_id": "cid",
		"client_secret": "csecret", "issuer": e.idp.Issuer(), "scopes": []string{}, "link_by_verified_email": true}
	if code, m, _ := adm.do(http.MethodPost, "/identity-providers", map[string]any{"slug": "x y", "kind": "oidc", "client_id": "c", "issuer": "nope"}); code != 400 {
		t.Fatalf("validation: %d %v", code, m)
	}
	code, m, _ := adm.do(http.MethodPost, "/identity-providers", body)
	if code != 201 || m["has_client_secret"] != true || m["callback_url"] != "http://panel.test/api/oidc/callback" || m["pkce_enabled"] != true {
		t.Fatalf("create: %d %v", code, m)
	}
	if _, leaked := m["client_secret"]; leaked {
		t.Fatal("secret returned")
	}
	if code, _, _ := adm.do(http.MethodPost, "/identity-providers", body); code != 409 {
		t.Fatalf("duplicate slug: %d", code)
	}
	id := jsonNum(m["id"].(float64))
	// Secret encrypted at rest and bound to the row.
	p, _ := e.st.IdentityProviders.GetBySlug(context.Background(), "authentik")
	if bytes.Contains(p.ClientSecretEnc, []byte("csecret")) {
		t.Fatal("secret stored in plain text")
	}
	if s, err := e.aead.OpenString(p.ClientSecretEnc, crypto.IdentityProviderClientSecretAD(p.ID)); err != nil || s != "csecret" {
		t.Fatalf("decrypt: %v", err)
	}
	// Update without client_secret keeps it.
	delete(body, "client_secret")
	body["display_name"] = "Renamed"
	code, m, _ = adm.do(http.MethodPatch, "/identity-providers/"+id, map[string]any{"display_name": "Renamed"})
	if code != 200 || m["display_name"] != "Renamed" || m["has_client_secret"] != true {
		t.Fatalf("update: %d %v", code, m)
	}
	code, m, _ = adm.do(http.MethodGet, "/identity-providers/"+id, nil)
	raw, _ := json.Marshal(m)
	if code != 200 || strings.Contains(string(raw), "csecret") {
		t.Fatalf("get leaks secret: %s", raw)
	}
	// Test discovery.
	code, m, _ = adm.do(http.MethodPost, "/identity-providers/"+id+"/test", nil)
	if code != 200 || m["ok"] != true || m["token_endpoint"] != e.idp.Issuer()+"/token" {
		t.Fatalf("test: %d %v", code, m)
	}
	// Public provider list for the login page.
	resp, _ := anon.hc.Get(e.srv.URL + "/api/v1/admin/auth/providers")
	var optEnv struct{ Items []map[string]any }
	_ = json.NewDecoder(resp.Body).Decode(&optEnv)
	resp.Body.Close()
	opts := optEnv.Items
	if len(opts) != 1 || opts[0]["slug"] != "authentik" {
		t.Fatalf("providers: %v", opts)
	}
	// Audit rows never contain the secret.
	var details string
	_ = e.st.Querier().QueryRowContext(context.Background(), `SELECT group_concat(details) FROM admin_audit WHERE action LIKE 'identity_provider.%'`).Scan(&details)
	if details == "" || strings.Contains(details, "csecret") {
		t.Fatalf("audit: %q", details)
	}
	if code, _, _ := adm.do(http.MethodDelete, "/identity-providers/"+id, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := adm.do(http.MethodGet, "/identity-providers/"+id, nil); code != 404 {
		t.Fatalf("after delete: %d", code)
	}
}

func (e *tenv) provider(link, autoReg bool) *store.IdentityProvider {
	p := &store.IdentityProvider{Slug: "authentik", Kind: store.ProviderOIDC, ClientID: "cid", Issuer: e.idp.Issuer(),
		PKCEEnabled: true, PKCEMethod: "S256", Enabled: true, LinkByVerifiedEmail: link, AutoRegister: autoReg}
	ctx := context.Background()
	if err := e.st.IdentityProviders.Create(ctx, p); err != nil {
		e.t.Fatal(err)
	}
	p.ClientSecretEnc, _ = e.aead.SealString("csecret", crypto.IdentityProviderClientSecretAD(p.ID))
	_ = e.st.IdentityProviders.Update(ctx, p)
	return p
}

// oidcLogin runs the panel OIDC flow through the HTTP API.
func (c *client) oidcLogin(next string, id oidctest.Identity) *http.Response {
	c.e.t.Helper()
	code, m, _ := c.do(http.MethodPost, "/auth/oidc/authentik/start", map[string]string{"next": next})
	if code != 200 {
		c.e.t.Fatalf("start: %d %v", code, m)
	}
	q, err := c.e.idp.Authorize(m["url"].(string), id)
	if err != nil {
		c.e.t.Fatal(err)
	}
	resp, err := c.hc.Get(c.e.srv.URL + "/api/oidc/callback?" + q.Encode())
	if err != nil {
		c.e.t.Fatal(err)
	}
	resp.Body.Close()
	return c.followHandoff(resp)
}

// followHandoff follows the callback's redirect to the admin-host completion
// endpoint (the test server serves both hosts).
func (c *client) followHandoff(resp *http.Response) *http.Response {
	c.e.t.Helper()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Path != "/api/v1/admin/auth/oidc/complete" {
		return resp
	}
	resp, err = c.hc.Get(c.e.srv.URL + loc.RequestURI())
	if err != nil {
		c.e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestPanelOIDCLogin(t *testing.T) {
	e := newTEnv(t)
	p := e.provider(false, false)
	u := e.user("alice", "correct horse", store.RoleUser)
	_ = e.st.UserIdentities.Create(context.Background(), &store.UserIdentity{UserID: u.ID, ProviderID: p.ID, Subject: "alice-sub"})
	c := e.client()
	resp := c.oidcLogin("/devices", oidctest.Identity{Subject: "alice-sub"})
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/devices" {
		t.Fatalf("redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if code, m, _ := c.do(http.MethodGet, "/auth/me", nil); code != 200 || m["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("me: %d %v", code, m)
	}
	// Unknown identity without auto-register: back to the login page with an error.
	c2 := e.client()
	resp = c2.oidcLogin("/", oidctest.Identity{Subject: "stranger"})
	if resp.Header.Get("Location") != "/login?error=oidc_no_account" {
		t.Fatalf("stranger: %s", resp.Header.Get("Location"))
	}
	if code, _, _ := c2.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatal("stranger must not be logged in")
	}
}

func TestPanelOIDCLoginWithTOTPNeedsSecondFactor(t *testing.T) {
	e := newTEnv(t)
	p := e.provider(false, false)
	u := e.user("alice", "correct horse", store.RoleUser)
	_ = e.st.UserIdentities.Create(context.Background(), &store.UserIdentity{UserID: u.ID, ProviderID: p.ID, Subject: "alice-sub"})
	c := e.client()
	c.login("alice", "correct horse")
	secret, _ := enrollTOTP(t, e, c)
	c.do(http.MethodPost, "/auth/logout", nil)
	resp := c.oidcLogin("/x", oidctest.Identity{Subject: "alice-sub"})
	if !strings.HasPrefix(resp.Header.Get("Location"), "/login?mfa=1&next=%2Fx") {
		t.Fatalf("mfa redirect: %s", resp.Header.Get("Location"))
	}
	if code, _, _ := c.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("pending: %d", code)
	}
	e.clk.advance(30 * time.Second)
	otp, _ := auth.TOTPCode(secret, e.clk.now())
	if code, _, _ := c.do(http.MethodPost, "/auth/mfa", map[string]string{"code": otp}); code != 200 {
		t.Fatalf("mfa: %d", code)
	}
}

func TestLinkAndUnlinkIdentity(t *testing.T) {
	e := newTEnv(t)
	e.provider(false, false)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	code, m, _ := c.do(http.MethodPost, "/me/security/identities/link/authentik", map[string]string{})
	if code != 200 {
		t.Fatalf("link start: %d %v", code, m)
	}
	q, _ := e.idp.Authorize(m["url"].(string), oidctest.Identity{Subject: "alice-sub", Email: "a@example.test", EmailVerified: true})
	resp, _ := c.hc.Get(e.srv.URL + "/api/oidc/callback?" + q.Encode())
	resp.Body.Close()
	resp = c.followHandoff(resp)
	if resp.Header.Get("Location") != "/my/profile?linked=authentik" {
		t.Fatalf("link: %s", resp.Header.Get("Location"))
	}
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/admin/me/security/identities", nil)
	resp, _ = c.hc.Do(req)
	var idEnv struct{ Items []map[string]any }
	_ = json.NewDecoder(resp.Body).Decode(&idEnv)
	resp.Body.Close()
	ids := idEnv.Items
	if len(ids) != 1 || ids[0]["provider_slug"] != "authentik" || ids[0]["subject"] != "alice-sub" ||
		ids[0]["provider_name"] == nil {
		t.Fatalf("identities: %v", ids)
	}
	if code, _, _ := c.do(http.MethodDelete, "/me/security/identities/"+jsonNum(ids[0]["id"].(float64)), nil); code != 204 {
		t.Fatalf("unlink: %d", code)
	}

	// An OIDC-only user cannot unlink their last identity.
	ctx := context.Background()
	p, _ := e.st.IdentityProviders.GetBySlug(ctx, "authentik")
	only := e.user("oidconly", "", store.RoleUser)
	_ = e.st.UserIdentities.Create(ctx, &store.UserIdentity{UserID: only.ID, ProviderID: p.ID, Subject: "only"})
	c2 := e.client()
	c2.oidcLogin("/", oidctest.Identity{Subject: "only"})
	list, _ := e.st.UserIdentities.ListForUser(ctx, only.ID)
	if code, m, _ := c2.do(http.MethodDelete, "/me/security/identities/"+jsonNum(float64(list[0].ID)), nil); code != 409 {
		t.Fatalf("last method: %d %v", code, m)
	}
}

func TestRuntimeAuthSettingsEnforced(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	alice := e.user("alice", "correct horse", store.RoleUser)
	e.user("root", "correct horse", store.RoleAdmin)

	// Session lifetimes from the settings apply to new sessions.
	_ = e.st.Settings.Set(ctx, store.SettingSessionAbsoluteDays, 2, nil)
	_ = e.st.Settings.Set(ctx, store.SettingSessionIdleHours, 3, nil)
	if code, _ := e.client().login("alice", "correct horse"); code != 200 {
		t.Fatalf("login: %d", code)
	}
	toks, _ := e.st.AuthTokens.ListActiveForUser(ctx, alice.ID, store.TokenWeb)
	if len(toks) != 1 || toks[0].AbsoluteExpiresAt == nil ||
		!toks[0].AbsoluteExpiresAt.Equal(e.clk.now().Add(48*time.Hour)) || !toks[0].ExpiresAt.Equal(e.clk.now().Add(3*time.Hour)) {
		t.Fatalf("lifetimes: %+v", toks)
	}

	// Password login disabled: users are refused (after the password check),
	// admins keep break-glass access.
	_ = e.st.Settings.Set(ctx, store.SettingPasswordLoginEnabled, false, nil)
	if code, m := e.client().login("alice", "wrong"); code != 401 {
		t.Fatalf("wrong password must stay generic: %d %v", code, m)
	}
	if code, m := e.client().login("alice", "correct horse"); code != 403 || errCode(m) != "password_login_disabled" {
		t.Fatalf("disabled: %d %v", code, m)
	}
	if code, _ := e.client().login("root", "correct horse"); code != 200 {
		t.Fatalf("admin break-glass: %d", code)
	}
}

func TestProfileAndRevokeOthers(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	a, b := e.client(), e.client()
	a.login("alice", "correct horse")
	b.login("alice", "correct horse")
	code, m, _ := a.do(http.MethodPatch, "/me", map[string]any{"display_name": " Alice A ", "email": "a@example.test"})
	if code != 200 || m["display_name"] != "Alice A" || m["email"] != "a@example.test" {
		t.Fatalf("profile: %d %v", code, m)
	}
	if code, m, _ := a.do(http.MethodPatch, "/me", map[string]any{"email": "not an email"}); code != 400 {
		t.Fatalf("bad email: %d %v", code, m)
	}
	if code, m, _ := a.do(http.MethodPatch, "/me", map[string]any{"role": "admin"}); code != 400 {
		t.Fatalf("role must not be settable: %d %v", code, m)
	}
	if code, m, _ := a.do(http.MethodPatch, "/me", map[string]any{"email": nil}); code != 200 || m["email"] != nil {
		t.Fatalf("clear email: %d %v", code, m)
	}
	code, m, _ = a.do(http.MethodPost, "/me/security/sessions/revoke-others", nil)
	if code != 200 || m["revoked"] != float64(1) {
		t.Fatalf("revoke-others: %d %v", code, m)
	}
	if code, _, _ := b.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("other session must be revoked: %d", code)
	}
	if code, _, _ := a.do(http.MethodGet, "/auth/me", nil); code != 200 {
		t.Fatalf("current session must survive: %d", code)
	}
}
