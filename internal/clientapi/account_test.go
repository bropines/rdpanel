package clientapi

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/store"
)

func loginBody(user, pw string) string {
	b, _ := json.Marshal(map[string]any{"username": user, "password": pw, "id": "123456789", "uuid": "dXVpZA==",
		"autoLogin": true, "type": "account", "deviceInfo": map[string]string{"os": "windows", "type": "client", "name": "PC"}})
	return string(b)
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := e.user("alice", "correct horse", func(u *store.User) { u.Role = store.RoleAdmin; u.Note = "n" })
	e.device("123456789", "dXVpZA==")

	r := e.mustDo("POST", "/api/login", loginBody("alice", "correct horse"), "", 200)
	var out AuthBody
	_ = json.Unmarshal([]byte(r.body), &out)
	if out.Type != "access_token" || strings.Count(out.AccessToken, ".") != 2 {
		t.Fatalf("login: %s", r.body)
	}
	golden(t, "login_ok", scrub(r.body, out.AccessToken, "<token>"))

	// The token is an HS256 JWT {user_id, exp}; only its sha256 is stored.
	claims, err := e.deps.JWT.Verify(out.AccessToken)
	if err != nil || int64(claims.UserID) != alice.ID || !claims.ExpiresAt.Time.Equal(e.clk.now().Add(168*time.Hour)) {
		t.Fatalf("claims %+v %v", claims, err)
	}
	tok, err := e.st.AuthTokens.GetActive(ctx, store.TokenClient, crypto.HashToken(out.AccessToken))
	if err != nil || tok.DeviceRustdeskID != "123456789" || tok.DeviceUUID != "dXVpZA==" || tok.ClientPlatform != "windows" ||
		!tok.ExpiresAt.Equal(e.clk.now().Add(4380*time.Hour)) {
		t.Fatalf("token row %+v %v", tok, err)
	}
	// Login binds the device and records a login event.
	d, _ := e.st.Devices.GetByRustdeskID(ctx, "123456789")
	if d.OwnerUserID == nil || *d.OwnerUserID != alice.ID {
		t.Fatal("device not bound to user")
	}
	evs, _ := e.st.LoginEvents.List(ctx, store.LoginEventFilter{})
	if len(evs) != 1 || evs[0].Result != store.LoginSuccess || *evs[0].TokenID != tok.ID || evs[0].ClientKind != "client" {
		t.Fatalf("events %+v", evs)
	}

	// Case-insensitive username.
	e.mustDo("POST", "/api/login", loginBody("ALICE", "correct horse"), "", 200)

	// Failures: 400 {"error"} (never 401 here).
	e.golden("login_bad_password", e.do("POST", "/api/login", loginBody("alice", "wrong"), ""), 400)
	e.golden("login_bad_password", e.do("POST", "/api/login", loginBody("nobody", "wrong"), ""), 400)
	e.golden("login_unsupported_type", e.do("POST", "/api/login", `{"username":"alice","password":"x","type":"sms_code"}`, ""), 400)
	e.golden("invalid_request", e.do("POST", "/api/login", `{"username":`, ""), 400)
	e.golden("invalid_request", e.do("POST", "/api/login", "", ""), 400)

	e.user("off", "pw-disabled", func(u *store.User) { u.Status = store.UserDisabled })
	e.golden("login_disabled", e.do("POST", "/api/login", loginBody("off", "pw-disabled"), ""), 400)
	e.user("oidconly", "", nil)
	e.mustDo("POST", "/api/login", loginBody("oidconly", ""), "", 400)

	evs, _ = e.st.LoginEvents.List(ctx, store.LoginEventFilter{Result: store.LoginFailure})
	if len(evs) < 3 || evs[len(evs)-1].UsernameAttempted != "alice" || evs[len(evs)-1].FailureReason != "bad_credentials" {
		t.Fatalf("failure events %+v", evs)
	}
}

func TestLoginPasswordDisabledSetting(t *testing.T) {
	e := newEnv(t)
	e.user("carol", "secret-pass", nil)
	e.user("boss", "secret-pass", func(u *store.User) { u.Role = store.RoleAdmin })
	_ = e.st.Settings.Set(context.Background(), store.SettingPasswordLoginEnabled, false, nil)
	r := e.do("POST", "/api/login", loginBody("carol", "secret-pass"), "")
	if r.code != 400 || !strings.Contains(r.body, "Password login is disabled") {
		t.Fatalf("user: %d %s", r.code, r.body)
	}
	e.mustDo("POST", "/api/login", loginBody("boss", "secret-pass"), "", 200)
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.user("bob", "secret-pass", nil)
	for i := 0; i < 4; i++ {
		e.mustDo("POST", "/api/login", loginBody("bob", "nope"), "", 400)
	}
	r := e.do("POST", "/api/login", loginBody("bob", "secret-pass"), "")
	if r.code != 429 || !strings.Contains(r.body, `"error":"Too many failed attempts`) || r.hdr.Get("Retry-After") == "" {
		t.Fatalf("locked: %d %s", r.code, r.body)
	}
	e.clk.advance(2 * time.Second)
	e.mustDo("POST", "/api/login", loginBody("bob", "secret-pass"), "", 200)
}

func TestLoginLegacyHashRehash(t *testing.T) {
	e := newEnv(t)
	sum := md5.Sum([]byte("legacypw" + "rustdesk-api"))
	u := e.user("old", "", func(u *store.User) {
		u.PasswordHash, u.PasswordAlgo = hex.EncodeToString(sum[:]), "legacy_md5_rustdesk_api"
	})
	e.login("old", "legacypw", "", "")
	got, _ := e.st.Users.GetByID(context.Background(), u.ID)
	if got.PasswordAlgo != "argon2id" || !cheapHasher.Verify(got.PasswordHash, "legacypw") {
		t.Fatalf("not rehashed: %s", got.PasswordAlgo)
	}
}

func TestLoginTOTP(t *testing.T) {
	e := newEnv(t)
	u := e.user("carol", "pw-carol-1", nil)
	e.totp.codes[u.ID] = "123456"

	r := e.mustDo("POST", "/api/login", loginBody("carol", "pw-carol-1"), "", 200)
	var ch AuthBody
	_ = json.Unmarshal([]byte(r.body), &ch)
	if ch.Secret == "" || ch.AccessToken != "" {
		t.Fatalf("challenge %s", r.body)
	}
	golden(t, "login_tfa_challenge", scrub(r.body, ch.Secret, "<secret>"))

	step2 := func(code, secret, user string) resp {
		b, _ := json.Marshal(map[string]any{"username": user, "type": "email_code", "verificationCode": code,
			"tfaCode": code, "secret": secret, "id": "1", "uuid": "u"})
		return e.do("POST", "/api/login", string(b), "")
	}
	e.golden("login_tfa_bad_code", step2("000000", ch.Secret, "carol"), 400)
	e.mustDo("POST", "/api/login", `{"username":"carol","type":"email_code","tfaCode":"123456","secret":"forged"}`, "", 400)
	e.mustDo("POST", "/api/login", string(mustJSON(map[string]any{"username": "mallory", "type": "email_code",
		"tfaCode": "123456", "secret": ch.Secret})), "", 400)
	r = step2("123456", ch.Secret, "carol")
	if r.code != 200 || !strings.Contains(r.body, `"type":"access_token"`) {
		t.Fatalf("step 2: %d %s", r.code, r.body)
	}
	// Challenges expire.
	r = e.mustDo("POST", "/api/login", loginBody("carol", "pw-carol-1"), "", 200)
	_ = json.Unmarshal([]byte(r.body), &ch)
	e.clk.advance(6 * time.Minute)
	e.mustDo("POST", "/api/login", string(mustJSON(map[string]any{"username": "carol", "type": "email_code",
		"tfaCode": "123456", "secret": ch.Secret})), "", 400)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestCurrentUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("dave", "pw-dave-123", func(u *store.User) { u.AvatarURL = "/avatar/d.png" })
	tok := e.login("dave", "pw-dave-123", "1", "u")

	e.golden("current_user", e.do("POST", "/api/currentUser", `{"id":"1","uuid":"u"}`, tok), 200)
	e.golden("current_user", e.do("POST", "/api/currentUser", "", tok), 200) // empty body accepted

	for _, hdr := range []string{"", "Basic " + tok, "Bearer", "Bearer  ", tok, "Bearer a b", "Token " + tok} {
		r := e.doHeader("POST", "/api/currentUser", hdr)
		if r.code != 401 || r.body != `{"error":"Unauthorized"}` {
			t.Fatalf("header %q: %d %s", hdr, r.code, r.body)
		}
	}
	// Case-insensitive scheme.
	if r := e.doHeader("POST", "/api/currentUser", "bearer "+tok); r.code != 200 {
		t.Fatalf("lowercase bearer: %d", r.code)
	}
	// A JWT signed with another key, or not in the DB, is rejected.
	forged, _ := e.deps.JWT.Mint(uint32(u.ID), e.clk.now().Add(time.Hour))
	e.golden("unauthorized", e.do("POST", "/api/currentUser", "", forged), 401)

	// Sliding expiry: extended once less than a third of the lifetime is left.
	life := 4380 * time.Hour
	tok2 := e.login("dave", "pw-dave-123", "1", "u")
	_, _ = e.st.Querier().ExecContext(ctx, `UPDATE auth_tokens SET expires_at = ? WHERE token_sha256 = ?`,
		e.clk.now().Add(time.Hour).UnixMilli(), crypto.HashToken(tok2))
	e.mustDo("POST", "/api/currentUser", "", tok2, 200)
	row, _ := e.st.AuthTokens.GetActive(ctx, store.TokenClient, crypto.HashToken(tok2))
	if !row.ExpiresAt.Equal(e.clk.now().Add(life)) {
		t.Fatalf("not slid: %v", row.ExpiresAt)
	}

	// JWT expiry is enforced even though the DB row slides.
	e.clk.advance(169 * time.Hour)
	e.mustDo("POST", "/api/currentUser", "", tok2, 401)
	e.clk.advance(-169 * time.Hour)

	// Disabled users are logged out (401), never 400.
	u.Status = store.UserDisabled
	_ = e.st.Users.Update(ctx, u)
	e.golden("unauthorized", e.do("POST", "/api/currentUser", "", tok), 401)
}

func (e *env) doHeader(method, path, auth string) resp {
	e.t.Helper()
	req := newReq(method, path, "")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return e.serve(req)
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u := e.user("erin", "pw-erin-123", nil)
	e.device("555000555", "ZXJpbg==")
	tok := e.login("erin", "pw-erin-123", "555000555", "ZXJpbg==")
	if d, _ := e.st.Devices.GetByRustdeskID(ctx, "555000555"); d.OwnerUserID == nil || *d.OwnerUserID != u.ID {
		t.Fatal("not bound")
	}
	e.empty("POST", "/api/logout", `{"id":"555000555","uuid":"ZXJpbg=="}`, tok)
	e.mustDo("POST", "/api/currentUser", "", tok, 401)
	if d, _ := e.st.Devices.GetByRustdeskID(ctx, "555000555"); d.OwnerUserID != nil {
		t.Fatal("device still bound after logout")
	}
	// Already invalid / missing tokens: still 200 empty.
	e.empty("POST", "/api/logout", "", tok)
	e.empty("POST", "/api/logout", "", "")
}

func TestNotFoundStaysJSON(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/api/nope", "", "")
	if r.code != 404 || !strings.Contains(r.body, `"not_found"`) {
		t.Fatalf("%d %s", r.code, r.body)
	}
	if r := e.do("GET", "/api/login", "", ""); r.code != 405 {
		t.Fatalf("405 expected: %d", r.code)
	}
}
