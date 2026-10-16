package adminapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/oidc/oidctest"
	"github.com/bropines/rdpanel/internal/store"
)

// vauth is a minimal software authenticator (ES256, "none" attestation)
// standing in for a browser + platform authenticator.
type vauth struct {
	t      *testing.T
	rpID   string
	origin string
	key    *ecdsa.PrivateKey
	credID []byte
	handle []byte
	// counter is the signature counter reported next (incremented per use
	// when step > 0; synced passkeys report 0).
	counter uint32
	step    uint32
	// uv: report user verification; be/bs: backup eligible/state flags.
	uv, be, bs bool
}

func newVAuth(t *testing.T) *vauth {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &vauth{t: t, rpID: "panel.test", origin: "http://panel.test", key: key, credID: id, uv: true, step: 1}
}

var b64 = base64.RawURLEncoding

const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
)

func (a *vauth) flags() byte {
	f := byte(flagUP)
	if a.uv {
		f |= flagUV
	}
	if a.be {
		f |= flagBE
	}
	if a.bs {
		f |= flagBS
	}
	return f
}

func (a *vauth) authData(attested bool) []byte {
	h := sha256.Sum256([]byte(a.rpID))
	out := append([]byte{}, h[:]...)
	f := a.flags()
	if attested {
		f |= flagAT
	}
	out = append(out, f)
	out = binary.BigEndian.AppendUint32(out, a.counter)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.credID)))
		out = append(out, a.credID...)
		x, y := a.key.X.FillBytes(make([]byte, 32)), a.key.Y.FillBytes(make([]byte, 32))
		cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
			PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
			Curve:         int64(webauthncose.P256), XCoord: x, YCoord: y})
		if err != nil {
			a.t.Fatal(err)
		}
		out = append(out, cose...)
	}
	return out
}

func (a *vauth) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": a.origin, "crossOrigin": false})
	return b
}

// publicKey extracts options.publicKey from a begin response.
func publicKey(t *testing.T, m map[string]any) (string, map[string]any) {
	t.Helper()
	opts, ok := m["options"].(map[string]any)
	if !ok {
		t.Fatalf("no options: %v", m)
	}
	pk := opts["publicKey"].(map[string]any)
	return m["ceremony_id"].(string), pk
}

// create answers navigator.credentials.create().
func (a *vauth) create(pk map[string]any) map[string]any {
	user := pk["user"].(map[string]any)
	h, err := b64.DecodeString(user["id"].(string))
	if err != nil {
		a.t.Fatal(err)
	}
	a.handle = h
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(true)})
	if err != nil {
		a.t.Fatal(err)
	}
	cd := a.clientData("webauthn.create", pk["challenge"].(string))
	return map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"authenticatorAttachment": "platform",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att),
			"transports": []string{"internal", "hybrid"}},
	}
}

// get answers navigator.credentials.get().
func (a *vauth) get(pk map[string]any) map[string]any {
	a.counter += a.step
	ad := a.authData(false)
	cd := a.clientData("webauthn.get", pk["challenge"].(string))
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	return map[string]any{
		"id": b64.EncodeToString(a.credID), "rawId": b64.EncodeToString(a.credID), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.handle)},
	}
}

func registerPasskey(t *testing.T, c *client, a *vauth, reauth map[string]string, name string) map[string]any {
	t.Helper()
	code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/begin", reauth)
	if code != 200 {
		t.Fatalf("register begin: %d %v", code, m)
	}
	id, pk := publicKey(t, m)
	if pk["rp"].(map[string]any)["id"] != "panel.test" {
		t.Fatalf("rp: %v", pk["rp"])
	}
	sel := pk["authenticatorSelection"].(map[string]any)
	if sel["residentKey"] != "required" {
		t.Fatalf("authenticatorSelection: %v", sel)
	}
	code, m, _ = c.do(http.MethodPost, "/me/security/passkeys/register/finish",
		map[string]any{"ceremony_id": id, "name": name, "credential": a.create(pk)})
	if code != 201 {
		t.Fatalf("register finish: %d %v", code, m)
	}
	return m
}

func passkeyLogin(t *testing.T, c *client, a *vauth) (int, map[string]any) {
	t.Helper()
	code, m, _ := c.do(http.MethodPost, "/auth/passkey/begin", nil)
	if code != 200 {
		return code, m
	}
	id, pk := publicKey(t, m)
	if pk["userVerification"] != "required" {
		t.Fatalf("usernameless login must require UV: %v", pk)
	}
	if _, ok := pk["allowCredentials"]; ok {
		t.Fatalf("usernameless login must not list credentials: %v", pk)
	}
	code, m, _ = c.do(http.MethodPost, "/auth/passkey/finish", map[string]any{"ceremony_id": id, "credential": a.get(pk)})
	return code, m
}

func countEvents(t *testing.T, e *tenv, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.Querier().QueryRowContext(context.Background(), `SELECT count(*) FROM login_events WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countAudit(t *testing.T, e *tenv, action string) int {
	t.Helper()
	var n int
	if err := e.st.Querier().QueryRowContext(context.Background(), `SELECT count(*) FROM admin_audit WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPasskeyRegisterAndUsernamelessLogin(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")

	if code, m, _ := c.do(http.MethodGet, "/me/security", nil); code != 200 || m["passkeys_available"] != true || m["passkey_count"] != float64(0) {
		t.Fatalf("security: %d %v", code, m)
	}
	// Adding a passkey requires re-authentication.
	if code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/begin", map[string]string{"password": "nope"}); code != 401 || errCode(m) != "reauth_failed" {
		t.Fatalf("bad reauth: %d %v", code, m)
	}
	a := newVAuth(t)
	pk := registerPasskey(t, c, a, map[string]string{"password": "correct horse"}, "  MacBook  ")
	if pk["name"] != "MacBook" || pk["last_used_at"] != nil || pk["clone_warning"] != false {
		t.Fatalf("passkey: %v", pk)
	}
	// The same authenticator cannot be registered twice: it is excluded and,
	// if forced, conflicts.
	code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/begin", map[string]string{"password": "correct horse"})
	id, opts := publicKey(t, m)
	if code != 200 || len(opts["excludeCredentials"].([]any)) != 1 {
		t.Fatalf("exclusions: %d %v", code, opts)
	}
	if code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/finish",
		map[string]any{"ceremony_id": id, "credential": a.create(opts)}); code != 409 {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	if countAudit(t, e, "user.passkey_add") != 1 {
		t.Fatal("audit user.passkey_add")
	}
	c.do(http.MethodPost, "/auth/logout", nil)

	// Usernameless sign-in from a fresh browser.
	c2 := e.client()
	if code, m, _ := c2.do(http.MethodGet, "/auth/providers", nil); code != 200 || m["passkey"] != true {
		t.Fatalf("providers: %d %v", code, m)
	}
	code, m = passkeyLogin(t, c2, a)
	if code != 200 || m["mfa_required"] != false || m["me"].(map[string]any)["user"].(map[string]any)["username"] != "alice" {
		t.Fatalf("passkey login: %d %v", code, m)
	}
	if code, _, _ := c2.do(http.MethodGet, "/api/protected", nil); code != http.StatusTeapot {
		t.Fatalf("protected: %d", code)
	}
	if countEvents(t, e, `method = 'passkey' AND result = 'success'`) != 1 {
		t.Fatal("login event")
	}
	_, m, _ = c2.do(http.MethodGet, "/me/security/passkeys", nil)
	items := m["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["last_used_at"] == nil {
		t.Fatalf("list: %v", m)
	}
}

func TestPasskeyLoginRejections(t *testing.T) {
	e := newTEnv(t)
	u := e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	a := newVAuth(t)
	registerPasskey(t, c, a, map[string]string{"password": "correct horse"}, "key")

	resetLimits := func() {
		e.deps.Limiter.Success(limitIP + "127.0.0.1")
		e.deps.Limiter.Success(userKey("alice"))
	}
	try := func(name string, mutate func(a *vauth), want int) {
		t.Helper()
		resetLimits()
		b := *a
		mutate(&b)
		code, m := passkeyLogin(t, e.client(), &b)
		if code != want {
			t.Fatalf("%s: %d %v", name, code, m)
		}
		a.counter = b.counter
	}
	// No user verification: a usernameless login must verify the user.
	try("no uv", func(a *vauth) { a.uv = false }, 401)
	// Phishing origin / foreign RP.
	try("origin", func(a *vauth) { a.origin = "https://evil.test" }, 401)
	try("rp id", func(a *vauth) { a.rpID = "evil.test" }, 401)
	// Unknown credential and a key that does not match.
	try("unknown credential", func(a *vauth) { a.credID = []byte("0123456789abcdef") }, 401)
	try("wrong key", func(a *vauth) { a.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }, 401)
	// Wrong user handle for the credential.
	try("user handle", func(a *vauth) { a.handle = []byte("someone-else-entirely") }, 401)

	// Ceremonies are single use and expire.
	resetLimits()
	c2 := e.client()
	_, m, _ := c2.do(http.MethodPost, "/auth/passkey/begin", nil)
	id, pk := publicKey(t, m)
	cred := a.get(pk)
	if code, m, _ := c2.do(http.MethodPost, "/auth/passkey/finish", map[string]any{"ceremony_id": id, "credential": cred}); code != 200 {
		t.Fatalf("first use: %d %v", code, m)
	}
	if code, m, _ := c2.do(http.MethodPost, "/auth/passkey/finish", map[string]any{"ceremony_id": id, "credential": cred}); code != 401 || errCode(m) != "passkey_failed" {
		t.Fatalf("replay: %d %v", code, m)
	}
	_, m, _ = c2.do(http.MethodPost, "/auth/passkey/begin", nil)
	id, pk = publicKey(t, m)
	e.clk.advance(passkeyCeremonyTTL + time.Second)
	if code, _, _ := c2.do(http.MethodPost, "/auth/passkey/finish", map[string]any{"ceremony_id": id, "credential": a.get(pk)}); code != 401 {
		t.Fatalf("expired: %d", code)
	}

	// Disabled users cannot sign in.
	resetLimits()
	u.Status = store.UserDisabled
	if err := e.st.Users.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if code, m := passkeyLogin(t, e.client(), a); code != 401 {
		t.Fatalf("disabled: %d %v", code, m)
	}
	if n := countEvents(t, e, `method = 'passkey' AND result = 'failure'`); n < 7 {
		t.Fatalf("failure events: %d", n)
	}
	if n := countEvents(t, e, `method = 'passkey' AND failure_reason = 'disabled' AND user_id = ?`, u.ID); n != 1 {
		t.Fatalf("disabled event: %d", n)
	}
}

func TestPasskeyLoginRateLimited(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	a := newVAuth(t)
	registerPasskey(t, c, a, map[string]string{"password": "correct horse"}, "key")
	bad := *a
	bad.uv = false
	var limited bool
	for i := 0; i < 6 && !limited; i++ {
		code, _ := passkeyLogin(t, e.client(), &bad)
		limited = code == 429
	}
	if !limited {
		t.Fatal("expected 429 after repeated failures")
	}
	if code, _ := passkeyLogin(t, e.client(), a); code != 429 {
		t.Fatalf("still limited per IP: %d", code)
	}
}

func TestPasskeyCloneDetection(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	a := newVAuth(t)
	a.counter = 10
	registerPasskey(t, c, a, map[string]string{"password": "correct horse"}, "yubikey")
	if code, m := passkeyLogin(t, e.client(), a); code != 200 { // counter 11
		t.Fatalf("login: %d %v", code, m)
	}
	// A clone replays an older counter value.
	clone := *a
	clone.counter = 5
	if code, m := passkeyLogin(t, e.client(), &clone); code != 401 || errCode(m) != "passkey_failed" {
		t.Fatalf("clone: %d %v", code, m)
	}
	_, m, _ := c.do(http.MethodGet, "/me/security/passkeys", nil)
	if m["items"].([]any)[0].(map[string]any)["clone_warning"] != true {
		t.Fatalf("clone flag: %v", m)
	}
	// A flagged passkey stays blocked, even with a higher counter.
	a.counter = 100
	if code, _ := passkeyLogin(t, e.client(), a); code != 401 {
		t.Fatalf("flagged passkey accepted: %d", code)
	}
	if countAudit(t, e, "user.passkey_clone_warning") != 2 || countEvents(t, e, `failure_reason = 'passkey_clone_warning'`) != 2 {
		t.Fatal("clone audit/events")
	}

	// Synced passkeys always report 0: no false positives.
	b := newVAuth(t)
	b.counter, b.step, b.be, b.bs = 0, 0, true, true
	registerPasskey(t, c, b, map[string]string{"password": "correct horse"}, "phone")
	for i := 0; i < 2; i++ {
		if code, m := passkeyLogin(t, e.client(), b); code != 200 {
			t.Fatalf("synced login %d: %d %v", i, code, m)
		}
	}
}

func TestPasskeyAsSecondFactor(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	secret, _ := enrollTOTP(t, e, c)
	if code, m, _ := c.do(http.MethodPost, "/auth/mfa/passkey/begin", nil); code != 401 {
		t.Fatalf("mfa begin without pending login: %d %v", code, m)
	}
	// TOTP enrolled: the code re-authenticates too.
	e.clk.advance(30 * time.Second)
	a := newVAuth(t)
	a.uv = false // a security key without PIN is fine as a second factor
	otp, _ := auth.TOTPCode(secret, e.clk.now())
	code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/begin", map[string]string{"code": otp})
	if code != 200 {
		t.Fatalf("register begin with TOTP: %d %v", code, m)
	}
	id, pk := publicKey(t, m)
	if code, m, _ = c.do(http.MethodPost, "/me/security/passkeys/register/finish", map[string]any{"ceremony_id": id, "credential": a.create(pk)}); code != 201 {
		t.Fatalf("register: %d %v", code, m)
	}
	c.do(http.MethodPost, "/auth/logout", nil)

	c2 := e.client()
	code, m = c2.login("alice", "correct horse")
	methods, _ := json.Marshal(m["mfa_methods"])
	if code != 200 || m["mfa_required"] != true || string(methods) != `["totp","passkey"]` {
		t.Fatalf("login: %d %v", code, m)
	}
	// A usernameless-style assertion without UV is not enough on its own...
	if code, _ := passkeyLogin(t, e.client(), a); code != 401 {
		t.Fatalf("no-UV usernameless: %d", code)
	}
	// ...but completes the pending login as a second factor.
	code, m, _ = c2.do(http.MethodPost, "/auth/mfa/passkey/begin", nil)
	if code != 200 {
		t.Fatalf("mfa begin: %d %v", code, m)
	}
	id, pk = publicKey(t, m)
	if len(pk["allowCredentials"].([]any)) != 1 {
		t.Fatalf("allowCredentials: %v", pk)
	}
	// The ceremony is bound to the pending session.
	other := e.client()
	other.login("alice", "correct horse")
	if code, m, _ := other.do(http.MethodPost, "/auth/mfa/passkey/finish", map[string]any{"ceremony_id": id, "credential": a.get(pk)}); code != 401 {
		t.Fatalf("foreign session: %d %v", code, m)
	}
	_, m, _ = c2.do(http.MethodPost, "/auth/mfa/passkey/begin", nil)
	id, pk = publicKey(t, m)
	code, m, _ = c2.do(http.MethodPost, "/auth/mfa/passkey/finish", map[string]any{"ceremony_id": id, "credential": a.get(pk)})
	if code != 200 || m["me"] == nil {
		t.Fatalf("mfa finish: %d %v", code, m)
	}
	if code, _, _ := c2.do(http.MethodGet, "/api/protected", nil); code != http.StatusTeapot {
		t.Fatalf("protected: %d", code)
	}
}

func TestPasskeyMFAWithoutPasskeys(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c := e.client()
	c.login("alice", "correct horse")
	enrollTOTP(t, e, c)
	c.do(http.MethodPost, "/auth/logout", nil)
	_, m := c.login("alice", "correct horse")
	if methods, _ := json.Marshal(m["mfa_methods"]); string(methods) != `["totp"]` {
		t.Fatalf("methods: %s", methods)
	}
	if code, m, _ := c.do(http.MethodPost, "/auth/mfa/passkey/begin", nil); code != 409 || errCode(m) != "no_passkeys" {
		t.Fatalf("no passkeys: %d %v", code, m)
	}
}

func TestPasskeyManageAndAdminRevoke(t *testing.T) {
	e := newTEnv(t)
	e.user("root", "root password", store.RoleAdmin)
	bob := e.user("bob", "bob password", store.RoleUser)
	cb := e.client()
	cb.login("bob", "bob password")
	k1 := registerPasskey(t, cb, newVAuth(t), map[string]string{"password": "bob password"}, "one")
	k2 := registerPasskey(t, cb, newVAuth(t), map[string]string{"password": "bob password"}, "")
	if k2["name"] != defaultPasskeyName {
		t.Fatalf("default name: %v", k2)
	}
	id1, id2 := jsonNum(k1["id"].(float64)), jsonNum(k2["id"].(float64))
	if code, m, _ := cb.do(http.MethodPatch, "/me/security/passkeys/"+id1, map[string]string{"name": "Laptop"}); code != 200 || m["name"] != "Laptop" {
		t.Fatalf("rename: %d %v", code, m)
	}
	if code, _, _ := cb.do(http.MethodPatch, "/me/security/passkeys/"+id1, map[string]string{"name": " "}); code != 400 {
		t.Fatalf("empty rename: %d", code)
	}
	// Users cannot reach the admin routes.
	if code, _, _ := cb.do(http.MethodGet, "/users/"+jsonNum(float64(bob.ID))+"/passkeys", nil); code != 403 {
		t.Fatalf("user on admin route: %d", code)
	}

	ca := e.client()
	ca.login("root", "root password")
	// Someone else's passkey is not mine to delete.
	if code, _, _ := ca.do(http.MethodDelete, "/me/security/passkeys/"+id1, nil); code != 404 {
		t.Fatalf("foreign delete: %d", code)
	}
	code, m, _ := ca.do(http.MethodGet, "/users/"+jsonNum(float64(bob.ID))+"/passkeys", nil)
	if code != 200 || len(m["items"].([]any)) != 2 {
		t.Fatalf("admin list: %d %v", code, m)
	}
	if code, _, _ := ca.do(http.MethodDelete, "/users/"+jsonNum(float64(bob.ID))+"/passkeys/"+id1, nil); code != 204 {
		t.Fatalf("admin revoke: %d", code)
	}
	if code, _, _ := ca.do(http.MethodDelete, "/users/"+jsonNum(float64(bob.ID))+"/passkeys/"+id1, nil); code != 404 {
		t.Fatalf("revoke twice: %d", code)
	}
	if code, _, _ := cb.do(http.MethodDelete, "/me/security/passkeys/"+id2, nil); code != 204 {
		t.Fatalf("own delete: %d", code)
	}
	_, m, _ = cb.do(http.MethodGet, "/me/security/passkeys", nil)
	if len(m["items"].([]any)) != 0 {
		t.Fatalf("after delete: %v", m)
	}
	for action, want := range map[string]int{"user.passkey_add": 2, "user.passkey_rename": 1, "user.passkey_revoke": 1, "user.passkey_remove": 1} {
		if n := countAudit(t, e, action); n != want {
			t.Fatalf("audit %s: %d", action, n)
		}
	}
}

func TestPasskeyRegisterWithoutPasswordNeedsFreshSession(t *testing.T) {
	e := newTEnv(t)
	p := e.provider(false, false)
	u := e.user("carol", "", store.RoleUser)
	_ = e.st.UserIdentities.Create(context.Background(), &store.UserIdentity{UserID: u.ID, ProviderID: p.ID, Subject: "carol-sub"})
	c := e.client()
	c.oidcLogin("/", oidctest.Identity{Subject: "carol-sub"})
	registerPasskey(t, c, newVAuth(t), nil, "fresh")
	e.clk.advance(passkeyFreshSession + time.Minute)
	if code, m, _ := c.do(http.MethodPost, "/me/security/passkeys/register/begin", nil); code != 401 || errCode(m) != "reauth_required" {
		t.Fatalf("stale session: %d %v", code, m)
	}
}

func TestPasskeyCeremonyBoundToSession(t *testing.T) {
	e := newTEnv(t)
	e.user("alice", "correct horse", store.RoleUser)
	c1, c2 := e.client(), e.client()
	c1.login("alice", "correct horse")
	c2.login("alice", "correct horse")
	_, m, _ := c1.do(http.MethodPost, "/me/security/passkeys/register/begin", map[string]string{"password": "correct horse"})
	id, pk := publicKey(t, m)
	if code, m, _ := c2.do(http.MethodPost, "/me/security/passkeys/register/finish",
		map[string]any{"ceremony_id": id, "credential": newVAuth(t).create(pk)}); code != 401 || errCode(m) != "passkey_failed" {
		t.Fatalf("other session: %d %v", code, m)
	}
}

func TestPasskeysUnavailableWithoutURL(t *testing.T) {
	cfg := config.Defaults()
	if newPasskeys(cfg).available() {
		t.Fatal("no URL: passkeys must be off")
	}
	cfg.AdminURL = "https://10.0.0.5"
	if newPasskeys(cfg).available() {
		t.Fatal("IP host: passkeys must be off")
	}
	cfg.AdminURL = "https://panel.example.com"
	if !newPasskeys(cfg).available() {
		t.Fatal("admin URL: passkeys must be on")
	}

	e := newTEnv(t)
	d := e.deps
	d.Config = config.Defaults()
	core := newAuthCore(d)
	rec := httptest.NewRecorder()
	core.handlePasskeyLoginBegin(rec, httptest.NewRequest(http.MethodPost, "/auth/passkey/begin", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "passkeys_unavailable") {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	core.handleProviders(rec, httptest.NewRequest(http.MethodGet, "/auth/providers", nil))
	if !strings.Contains(rec.Body.String(), `"passkey":false`) {
		t.Fatalf("providers: %s", rec.Body)
	}
}
