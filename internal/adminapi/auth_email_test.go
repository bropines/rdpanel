package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/mail/mailtest"
	"github.com/bropines/rdpanel/internal/mailauth"
	"github.com/bropines/rdpanel/internal/store"
)

// withMail configures the panel-managed SMTP server through the admin API
// and returns the fake server.
func withMail(t *testing.T, e *tenv) *mailtest.Server {
	t.Helper()
	srv := mailtest.Start(t, mailtest.WithAuth("mailer", "smtp-pass"))
	e.user("root", "root-password", store.RoleAdmin)
	c := e.client()
	if code, m := c.login("root", "root-password"); code != 200 {
		t.Fatalf("admin login: %d %v", code, m)
	}
	cfg := srv.Config()
	code, m, _ := c.do(http.MethodPut, "/settings/email", map[string]any{"host": cfg.Host, "port": cfg.Port,
		"username": "mailer", "password": "smtp-pass", "from": "Panel <panel@example.test>", "encryption": "none"})
	if code != 200 || m["source"] != "settings" || m["password_set"] != true || m["enabled"] != true {
		t.Fatalf("save smtp: %d %v", code, m)
	}
	return srv
}

func (e *tenv) emailUser(name, pw, addr string, verified bool) *store.User {
	e.t.Helper()
	u := e.user(name, pw, store.RoleUser)
	u.Email = addr
	if err := e.st.Users.Update(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	if verified {
		now := e.clk.now()
		if err := e.st.Users.SetEmailVerified(context.Background(), u.ID, addr, &now); err != nil {
			e.t.Fatal(err)
		}
	}
	fresh, _ := e.st.Users.GetByID(context.Background(), u.ID)
	return fresh
}

func TestEmailSettingsAndTestMessage(t *testing.T) {
	e := newTEnv(t)
	c := e.client()
	if code, m, _ := c.do(http.MethodGet, "/auth/options", nil); code != 200 || m["password_reset"] != false {
		t.Fatalf("options before: %d %v", code, m)
	}
	srv := withMail(t, e)
	admin := e.client()
	admin.login("root", "root-password")

	raw, err := e.st.Settings.GetRaw(context.Background(), mailauth.SettingSMTP)
	if err != nil || strings.Contains(string(raw), "smtp-pass") {
		t.Fatalf("stored smtp settings leak the password: %s %v", raw, err)
	}
	// Keep the password when omitted.
	code, m, _ := admin.do(http.MethodPut, "/settings/email", map[string]any{"default_locale": "ru", "port": srv.Port})
	if code != 200 || m["password_set"] != true || m["default_locale"] != "ru" {
		t.Fatalf("partial update: %d %v", code, m)
	}
	code, m, _ = admin.do(http.MethodPost, "/settings/email/test", map[string]any{"to": "ops@example.test"})
	if code != 200 || m["to"] != "ops@example.test" {
		t.Fatalf("test e-mail: %d %v", code, m)
	}
	got := srv.Last(t)
	if got.User != "mailer" || got.Pass != "smtp-pass" || got.To[0] != "ops@example.test" {
		t.Fatalf("delivered %+v", got)
	}
	if code, m, _ = admin.do(http.MethodPost, "/settings/email/test", map[string]any{"to": "bad"}); code != 400 {
		t.Fatalf("bad recipient: %d %v", code, m)
	}
	srv.RejectRecipients(true)
	e.clk.advance(time.Hour) // limiter backoff
	if code, m, _ = admin.do(http.MethodPost, "/settings/email/test", map[string]any{"to": "ops@example.test"}); code != 502 ||
		!strings.Contains(m["error"].(map[string]any)["message"].(string), "RCPT") {
		t.Fatalf("smtp failure: %d %v", code, m)
	}
	if code, m, _ = admin.do(http.MethodPut, "/settings/email", map[string]any{"from": "not an address"}); code != 400 {
		t.Fatalf("invalid from: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodGet, "/auth/options", nil); code != 200 || m["password_reset"] != true {
		t.Fatalf("options after: %d %v", code, m)
	}
	// Non-admins cannot read the settings.
	e.user("bob", "bob-password", store.RoleUser)
	u := e.client()
	u.login("bob", "bob-password")
	if code, _, _ := u.do(http.MethodGet, "/settings/email", nil); code != 403 {
		t.Fatalf("user reads smtp settings: %d", code)
	}
}

func TestEmailSettingsFromConfigAreReadOnly(t *testing.T) {
	srv := mailtest.Start(t)
	cfg := config.Defaults()
	cfg.PublicURL = "http://panel.test"
	cfg.SMTP = srv.Config()
	e2 := newTEnvWithConfig(t, cfg)
	e2.user("root", "root-password", store.RoleAdmin)
	c := e2.client()
	c.login("root", "root-password")
	code, m, _ := c.do(http.MethodGet, "/settings/email", nil)
	if code != 200 || m["source"] != "config" || m["host"] != srv.Host {
		t.Fatalf("get: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodPut, "/settings/email", map[string]any{"host": "other.example"}); code != 400 {
		t.Fatalf("read-only: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodPut, "/settings/email", map[string]any{"default_locale": "ru"}); code != 200 {
		t.Fatalf("locale stays editable: %d %v", code, m)
	}
}

func TestEmailVerificationAndSecondFactor(t *testing.T) {
	e := newTEnv(t)
	srv := withMail(t, e)
	e.emailUser("alice", "alice-password", "alice@example.test", false)
	c := e.client()
	if code, m := c.login("alice", "alice-password"); code != 200 || m["mfa_required"] != false {
		t.Fatalf("login: %d %v", code, m)
	}
	code, m, _ := c.do(http.MethodGet, "/me/security", nil)
	if code != 200 || m["email_verified"] != false || m["email_available"] != true {
		t.Fatalf("security: %d %v", code, m)
	}
	// Opting in needs a verified address.
	if code, m, _ = c.do(http.MethodPut, "/me/security/email-2fa", map[string]any{"enabled": true}); code != 409 {
		t.Fatalf("enable unverified: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodPost, "/me/security/email/verify", nil); code != 204 {
		t.Fatalf("send verification: %d %v", code, m)
	}
	msg := srv.Last(t)
	if msg.To[0] != "alice@example.test" || msg.Code() == "" {
		t.Fatalf("verification mail: %+v", msg)
	}
	// Cooldown.
	if code, _, resp := c.do(http.MethodPost, "/me/security/email/verify", nil); code != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("cooldown: %d", code)
	}
	if code, m, _ = c.do(http.MethodPost, "/me/security/email/confirm", map[string]string{"code": "000000"}); code != 400 {
		t.Fatalf("wrong code: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodPost, "/me/security/email/confirm", map[string]string{"code": msg.Code()}); code != 200 || m["email_verified"] != true {
		t.Fatalf("confirm: %d %v", code, m)
	}
	if code, m, _ = c.do(http.MethodPut, "/me/security/email-2fa", map[string]any{"enabled": true}); code != 200 || m["email_2fa_enabled"] != true {
		t.Fatalf("enable: %d %v", code, m)
	}

	// Sign in again: an e-mailed code is owed.
	c2 := e.client()
	code, m = c2.login("alice", "alice-password")
	if code != 200 || m["mfa_required"] != true || fmt.Sprint(m["mfa_methods"]) != "[email]" || m["email_hint"] != "a***e@example.test" {
		t.Fatalf("login with email 2fa: %d %v", code, m)
	}
	loginMail := srv.Wait(t, 2)[1]
	if code, _, _ := c2.do(http.MethodGet, "/api/protected", nil); code != 401 {
		t.Fatalf("pending session reached a protected route: %d", code)
	}
	if code, m, _ = c2.do(http.MethodPost, "/auth/mfa", map[string]string{"code": "999999"}); code != 401 || errCode(m) != "code_invalid" {
		t.Fatalf("wrong login code: %d %v", code, m)
	}
	// Resend: cooldown, then a new code that replaces the old one.
	if code, _, _ := c2.do(http.MethodPost, "/auth/mfa/resend", nil); code != 429 {
		t.Fatalf("resend cooldown: %d", code)
	}
	e.clk.advance(61 * time.Second)
	if code, m, _ = c2.do(http.MethodPost, "/auth/mfa/resend", nil); code != 204 {
		t.Fatalf("resend: %d %v", code, m)
	}
	newMail := srv.Wait(t, 3)[2]
	if newMail.Code() == loginMail.Code() {
		t.Skip("random codes collided")
	}
	if code, _, _ := c2.do(http.MethodPost, "/auth/mfa", map[string]string{"code": loginMail.Code()}); code != 401 {
		t.Fatalf("old code still valid: %d", code)
	}
	if code, m, _ = c2.do(http.MethodPost, "/auth/mfa", map[string]string{"code": newMail.Code()}); code != 200 || m["me"] == nil {
		t.Fatalf("mfa: %d %v", code, m)
	}
	if code, _, _ := c2.do(http.MethodGet, "/api/protected", nil); code != http.StatusTeapot {
		t.Fatalf("after mfa: %d", code)
	}

	// Disabling needs the password.
	if code, m, _ = c2.do(http.MethodPut, "/me/security/email-2fa", map[string]any{"enabled": false}); code != 401 {
		t.Fatalf("disable without reauth: %d %v", code, m)
	}
	// Changing the address clears verification and the factor.
	if code, m, _ = c2.do(http.MethodPatch, "/me", map[string]any{"email": "alice2@example.test", "locale": "ru"}); code != 200 ||
		m["email_verified"] != false || m["email_2fa_enabled"] != false {
		t.Fatalf("change email: %d %v", code, m)
	}
	if code, m = e.client().login("alice", "alice-password"); code != 200 || m["mfa_required"] != false {
		t.Fatalf("login after address change: %d %v", code, m)
	}
}

func TestEmail2FAPolicy(t *testing.T) {
	e := newTEnv(t)
	srv := withMail(t, e)
	e.emailUser("carol", "carol-password", "carol@example.test", true)
	e.emailUser("dan", "dan-password", "dan@example.test", false)
	admin := e.client()
	admin.login("root", "root-password")
	if code, m, _ := admin.do(http.MethodPatch, "/settings", map[string]any{"auth": map[string]any{"require_email_2fa": true}}); code != 200 ||
		m["auth"].(map[string]any)["require_email_2fa"] != true {
		t.Fatalf("policy: %d %v", code, m)
	}
	c := e.client()
	if code, m := c.login("carol", "carol-password"); code != 200 || fmt.Sprint(m["mfa_methods"]) != "[email]" {
		t.Fatalf("policy login: %d %v", code, m)
	}
	if code, m, _ := c.do(http.MethodPost, "/auth/mfa", map[string]string{"code": srv.Last(t).Code()}); code != 200 {
		t.Fatalf("mfa: %d %v", code, m)
	}
	// Users without a verified address are not locked out.
	if code, m := e.client().login("dan", "dan-password"); code != 200 || m["mfa_required"] != false {
		t.Fatalf("unverified user: %d %v", code, m)
	}
	// The policy cannot be opted out of.
	if code, m, _ := c.do(http.MethodPut, "/me/security/email-2fa", map[string]any{"enabled": false, "password": "carol-password"}); code != 200 {
		t.Fatalf("no-op disable: %d %v", code, m)
	}
	code, m, _ := c.do(http.MethodGet, "/me/security", nil)
	if code != 200 || m["email_2fa_required"] != true {
		t.Fatalf("security: %d %v", code, m)
	}
	// Admin marks dan's address unverified/verified.
	dan, _ := e.st.Users.GetByUsername(context.Background(), "dan")
	if code, m, _ := admin.do(http.MethodPatch, "/users/"+idStr(dan.ID), map[string]any{"email_verified": true}); code != 200 || m["email_verified"] != true {
		t.Fatalf("admin verify: %d %v", code, m)
	}
}

func TestPasswordResetByEmail(t *testing.T) {
	e := newTEnv(t)
	srv := withMail(t, e)
	alice := e.emailUser("alice", "old-password", "alice@example.test", true)
	e.emailUser("eve", "eve-password", "eve@example.test", false)
	old := e.client()
	if code, _ := old.login("alice", "old-password"); code != 200 {
		t.Fatal("login")
	}
	before := len(srv.Messages())

	anon := e.client()
	for _, login := range []string{"nobody", "eve", "alice@example.test"} {
		code, m, _ := anon.do(http.MethodPost, "/auth/password/forgot", map[string]string{"login": login})
		if code != 202 {
			t.Fatalf("forgot %q: %d %v", login, code, m)
		}
	}
	msgs := srv.Wait(t, before+1)
	time.Sleep(100 * time.Millisecond) // let stray background sends land
	if n := len(srv.Messages()); n != before+1 {
		t.Fatalf("want exactly one reset mail (verified alice), got %d", n-before)
	}
	link := msgs[before].Link()
	u, err := url.Parse(link)
	if err != nil || u.Path != "/reset-password" || u.Query().Get("token") == "" {
		t.Fatalf("reset link %q", link)
	}
	token := u.Query().Get("token")
	if code, m, _ := anon.do(http.MethodPost, "/auth/password/reset", map[string]string{"token": token, "new_password": "short"}); code != 400 {
		t.Fatalf("short password: %d %v", code, m)
	}
	if code, m, _ := anon.do(http.MethodPost, "/auth/password/reset", map[string]string{"token": token, "new_password": "brand-new-password"}); code != 204 {
		t.Fatalf("reset: %d %v", code, m)
	}
	if code, m, _ := anon.do(http.MethodPost, "/auth/password/reset", map[string]string{"token": token, "new_password": "another-password"}); code != 400 || errCode(m) != "invalid_token" {
		t.Fatalf("token reuse: %d %v", code, m)
	}
	if code, _, _ := old.do(http.MethodGet, "/auth/me", nil); code != 401 {
		t.Fatalf("old session survived the reset: %d", code)
	}
	if code, _ := e.client().login("alice", "brand-new-password"); code != 200 {
		t.Fatalf("login with new password: %d", code)
	}
	var n int
	_ = e.st.Querier().QueryRowContext(context.Background(),
		`SELECT count(*) FROM admin_audit WHERE action = 'user.password_reset_email' AND target_id = ?`, idStr(alice.ID)).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	// Expired tokens are refused.
	e.clk.advance(2 * time.Minute)
	anon.do(http.MethodPost, "/auth/password/forgot", map[string]string{"login": "alice"})
	msgs = srv.Wait(t, before+2)
	u, _ = url.Parse(msgs[before+1].Link())
	e.clk.advance(31 * time.Minute)
	if code, _, _ := e.client().do(http.MethodPost, "/auth/password/reset", map[string]string{"token": u.Query().Get("token"), "new_password": "expired-password"}); code != 400 {
		t.Fatalf("expired token: %d", code)
	}
}
