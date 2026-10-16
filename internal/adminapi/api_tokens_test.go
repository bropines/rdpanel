package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/store"
)

// bearer calls an admin API path with "Authorization: Bearer token", no
// cookies and no CSRF header.
func (e *tenv) bearer(method, path, token string, body any) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+"/api/v1/admin"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// loggedIn returns a cookie client logged in as a fresh user.
func (e *tenv) loggedIn(name string, role store.Role) (*client, *store.User) {
	e.t.Helper()
	u := e.user(name, "correct horse", role)
	c := e.client()
	if code, m := c.login(name, "correct horse"); code != http.StatusOK {
		e.t.Fatalf("login %s: %d %v", name, code, m)
	}
	return c, u
}

// mint creates an API token through the cookie session.
func (c *client) mint(name, scope string, days *int) (string, int64) {
	c.e.t.Helper()
	code, m, _ := c.do(http.MethodPost, "/me/api-tokens", map[string]any{"name": name, "scope": scope, "expires_in_days": days})
	if code != http.StatusCreated {
		c.e.t.Fatalf("create token: %d %v", code, m)
	}
	tok, _ := m["token"].(string)
	meta, _ := m["api_token"].(map[string]any)
	id, _ := meta["id"].(float64)
	return tok, int64(id)
}

func TestAPITokenLifecycle(t *testing.T) {
	e := newTEnv(t)
	c, admin := e.loggedIn("root", store.RoleAdmin)

	tok, id := c.mint("ci", "write", nil)
	if !auth.IsAPIToken(tok) || !strings.HasPrefix(tok, "rdp_") || len(tok) != 44 {
		t.Fatalf("token format %q", tok)
	}

	// The list never contains the secret.
	code, m, _ := c.do(http.MethodGet, "/me/api-tokens", nil)
	items, _ := m["items"].([]any)
	if code != 200 || len(items) != 1 {
		t.Fatalf("list: %d %v", code, m)
	}
	it := items[0].(map[string]any)
	if it["prefix"] != tok[:auth.APITokenDisplayLen] || it["scope"] != "write" || it["expires_at"] != nil || it["last_used_at"] != nil {
		t.Fatalf("item %v", it)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), tok) {
		t.Fatal("list leaks the token")
	}

	// Bearer works on admin routes, inherits the admin role, needs no CSRF.
	if code, m := e.bearer(http.MethodGet, "/users", tok, nil); code != 200 {
		t.Fatalf("GET /users: %d %v", code, m)
	}
	if code, m := e.bearer(http.MethodPost, "/user-groups", tok, map[string]any{"name": "ops"}); code != http.StatusCreated {
		t.Fatalf("POST /user-groups: %d %v", code, m)
	}
	if code, m := e.bearer(http.MethodGet, "/auth/me", tok, nil); code != 200 || m["user"].(map[string]any)["username"] != "root" {
		t.Fatalf("me: %d %v", code, m)
	}

	// last_used is recorded, throttled to once per minute.
	got, _ := e.st.APITokens.Get(context.Background(), id)
	if got.LastUsedAt == nil || got.LastUsedIP == "" {
		t.Fatalf("last used not recorded: %+v", got)
	}
	first := *got.LastUsedAt
	e.clk.advance(10 * time.Second)
	e.bearer(http.MethodGet, "/users", tok, nil)
	got, _ = e.st.APITokens.Get(context.Background(), id)
	if !got.LastUsedAt.Equal(first) {
		t.Fatal("last_used_at not throttled")
	}
	e.clk.advance(time.Minute)
	e.bearer(http.MethodGet, "/users", tok, nil)
	got, _ = e.st.APITokens.Get(context.Background(), id)
	if got.LastUsedAt.Equal(first) {
		t.Fatal("last_used_at not updated after a minute")
	}

	// Audit: create, plus requests made with the token are marked.
	entries, err := e.st.AdminAudit.List(context.Background(), store.AuditFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var created, viaToken bool
	for _, a := range entries {
		if a.Action == "api_token.create" && a.ActorUserID != nil && *a.ActorUserID == admin.ID {
			created = true
		}
		if a.Action == "user_group.create" && fmt.Sprint(a.Details["via_api_token"]) == fmt.Sprint(id) {
			viaToken = true
		}
	}
	if !created || !viaToken {
		t.Fatalf("audit: created=%v viaToken=%v %+v", created, viaToken, entries)
	}

	// Revoke: the token stops working at once.
	if code, m, _ := c.do(http.MethodDelete, fmt.Sprintf("/me/api-tokens/%d", id), nil); code != http.StatusNoContent {
		t.Fatalf("revoke: %d %v", code, m)
	}
	if code, _ := e.bearer(http.MethodGet, "/users", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
	if code, m, _ := c.do(http.MethodGet, "/me/api-tokens", nil); code != 200 || len(m["items"].([]any)) != 0 {
		t.Fatalf("list after revoke: %v", m)
	}
}

func TestAPITokenReadScope(t *testing.T) {
	e := newTEnv(t)
	c, _ := e.loggedIn("root", store.RoleAdmin)
	tok, _ := c.mint("ro", "read", nil)
	if code, _ := e.bearer(http.MethodGet, "/users", tok, nil); code != 200 {
		t.Fatalf("read GET: %d", code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		if code, body := e.bearer(m, "/user-groups", tok, map[string]any{"name": "x"}); code != http.StatusForbidden || errCode(body) != "insufficient_scope" {
			t.Fatalf("read %s: %d %v", m, code, body)
		}
	}
}

func TestAPITokenRejected(t *testing.T) {
	e := newTEnv(t)
	c, _ := e.loggedIn("root", store.RoleAdmin)
	days := 1
	tok, _ := c.mint("short", "write", &days)

	// Unknown and malformed tokens.
	other, _ := auth.NewAPIToken()
	for _, bad := range []string{other.Plain, "rdp_short", "not-a-token"} {
		if code, m := e.bearer(http.MethodGet, "/users", bad, nil); code != http.StatusUnauthorized || errCode(m) != "unauthorized" {
			t.Fatalf("bad %q: %d %v", bad, code, m)
		}
	}
	// Failures are rate-limited per IP like logins (3 free, then backoff).
	e.bearer(http.MethodGet, "/users", other.Plain, nil)
	if code, m := e.bearer(http.MethodGet, "/users", other.Plain, nil); code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d %v", code, m)
	}
	e.clk.advance(2 * time.Hour) // forget the failures

	// A valid bearer is not downgraded to the cookie when both are sent, and
	// an invalid bearer never falls back to the cookie.
	c.hc.Transport = headerRT{"Authorization": "Bearer " + other.Plain}
	if code, _, _ := c.do(http.MethodGet, "/users", nil); code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer + cookie: %d", code)
	}
	c.hc.Transport = nil
	e.clk.advance(2 * time.Hour)

	// Expired.
	if code, _ := e.bearer(http.MethodGet, "/users", tok, nil); code != 200 {
		t.Fatalf("fresh: %d", code)
	}
	e.clk.advance(25 * time.Hour)
	if code, _ := e.bearer(http.MethodGet, "/users", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("expired: %d", code)
	}

}

type headerRT map[string]string

func (h headerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestAPITokenDisabledUserAndRole(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	uc, u := e.loggedIn("bob", store.RoleUser)
	tok, _ := uc.mint("bob", "write", nil)

	// The token has the owner's role: no admin routes.
	if code, _ := e.bearer(http.MethodGet, "/devices", tok, nil); code != 200 {
		t.Fatalf("user GET /devices: %d", code)
	}
	if code, m := e.bearer(http.MethodGet, "/users", tok, nil); code != http.StatusForbidden || errCode(m) != "forbidden" {
		t.Fatalf("user GET /users: %d %v", code, m)
	}
	// Promotion applies at request time.
	u.Role = store.RoleAdmin
	if err := e.st.Users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.bearer(http.MethodGet, "/users", tok, nil); code != 200 {
		t.Fatalf("promoted GET /users: %d", code)
	}
	// Disabled owner: denied.
	u.Status = store.UserDisabled
	if err := e.st.Users.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.bearer(http.MethodGet, "/devices", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("disabled: %d", code)
	}
}

func TestAPITokenCannotManageCredentials(t *testing.T) {
	e := newTEnv(t)
	c, admin := e.loggedIn("root", store.RoleAdmin)
	tok, id := c.mint("ci", "write", nil)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/me/api-tokens"},
		{http.MethodDelete, fmt.Sprintf("/me/api-tokens/%d", id)},
		{http.MethodDelete, fmt.Sprintf("/users/%d/api-tokens/%d", admin.ID, id)},
		{http.MethodGet, "/me/security"},
		{http.MethodGet, "/me/security/sessions"},
		{http.MethodPost, "/me/security/password"},
	}
	for _, tc := range cases {
		code, m := e.bearer(tc.method, tc.path, tok, map[string]any{"name": "x", "scope": "write"})
		if code != http.StatusForbidden || errCode(m) != "cookie_session_required" {
			t.Fatalf("%s %s: %d %v", tc.method, tc.path, code, m)
		}
	}
	// Listing is fine.
	if code, _ := e.bearer(http.MethodGet, "/me/api-tokens", tok, nil); code != 200 {
		t.Fatalf("list via token: %d", code)
	}
}

func TestAPITokenValidationAndAdmin(t *testing.T) {
	e := newTEnv(t)
	ac, _ := e.loggedIn("root", store.RoleAdmin)
	uc, u := e.loggedIn("bob", store.RoleUser)

	for _, body := range []map[string]any{
		{"name": "", "scope": "write"},
		{"name": "x", "scope": "admin"},
		{"name": "x", "scope": "read", "expires_in_days": 0},
		{"name": "x", "scope": "read", "expires_in_days": 4000},
		{"name": strings.Repeat("n", 65), "scope": "read"},
	} {
		if code, m, _ := uc.do(http.MethodPost, "/me/api-tokens", body); code != http.StatusBadRequest {
			t.Fatalf("%v: %d %v", body, code, m)
		}
	}
	days := 90
	tok, id := uc.mint("bob-ci", "read", &days)

	// Users cannot reach other users' tokens.
	if code, _, _ := uc.do(http.MethodGet, fmt.Sprintf("/users/%d/api-tokens", u.ID), nil); code != http.StatusForbidden {
		t.Fatalf("user admin list: %d", code)
	}
	if code, _, _ := ac.do(http.MethodDelete, fmt.Sprintf("/me/api-tokens/%d", id), nil); code != http.StatusNotFound {
		t.Fatalf("foreign /me revoke: %d", code)
	}

	// Admin lists and revokes.
	code, m, _ := ac.do(http.MethodGet, fmt.Sprintf("/users/%d/api-tokens", u.ID), nil)
	items, _ := m["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["expires_at"] == nil {
		t.Fatalf("admin list: %d %v", code, m)
	}
	if code, _, _ := ac.do(http.MethodDelete, fmt.Sprintf("/users/%d/api-tokens/%d", u.ID+100, id), nil); code != http.StatusNotFound {
		t.Fatalf("wrong user: %d", code)
	}
	if code, _, _ := ac.do(http.MethodDelete, fmt.Sprintf("/users/%d/api-tokens/%d", u.ID, id), nil); code != http.StatusNoContent {
		t.Fatalf("admin revoke: %d", code)
	}
	if code, _ := e.bearer(http.MethodGet, "/devices", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("after admin revoke: %d", code)
	}
	if code, _, _ := ac.do(http.MethodDelete, fmt.Sprintf("/users/%d/api-tokens/%d", u.ID, id), nil); code != http.StatusNotFound {
		t.Fatalf("double revoke: %d", code)
	}
}
